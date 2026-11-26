// Package pipeline orchestrates the contract-drift pipeline per AGENTS.md:
//
//	fetch → gate → normalize → project → hash → diff → emit
//
// Steps 2 (ETag 304), 4 (probe fail → inconclusive), and 7 (shape_hash
// unchanged) are gates that decide whether to compare. Step 5 (normalize +
// scrub) is a transform that decides what is compared.
package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/enondev/endpoint-lighthouse-engine/internal/fetch"
	"github.com/enondev/endpoint-lighthouse-engine/internal/hash"
	"github.com/enondev/endpoint-lighthouse-engine/internal/normalize"
	"github.com/enondev/endpoint-lighthouse-engine/internal/scrub"
)

// Source represents a vendor spec source to monitor.
type Source struct {
	ID            string
	Vendor        string
	SpecURL       string
	SpecFormat    string
	PinnedVersion string
	ETag          string
	ContentHash   string
	LastFetched   string
}

// Operation represents a normalized API operation.
type Operation struct {
	ID           int
	SourceID     int
	Method       string
	Path         string
	OperationID  string
	ContractHash string
}

// ProbeResult is the outcome of probing a live endpoint.
type ProbeResult struct {
	OperationID int
	StatusCode  int
	LatencyMs   float64
	Body        []byte
	ContentType string
	Err         error // non-nil means the probe failed (outage, timeout, 5xx)
}

// Observation is a per-probe observation record.
type Observation struct {
	ProbeID     int
	ObservedAt  string
	StatusCode  int
	ContentType string
	ShapeHash   string
	BodySample  string
}

// Diff is a contract drift finding.
type Diff struct {
	OperationID int
	DetectedAt  string
	Kind        string
	Severity    string
	SpecSide    string
	LiveSide    string
	Status      string // open, acknowledged, resolved, inconclusive
}

// D1Writer abstracts D1 writes so the pipeline can be tested and the
// concrete D1 client can be injected (e.g., Cloudflare HTTP API client).
// Write methods are batched to mitigate Cloudflare D1 daily write limits.
type D1Writer interface {
	// InsertObservation writes a single observation. Callers should use
	// sampling (only on state change/error) to avoid burning write budget.
	InsertObservation(ctx context.Context, o Observation) (int, error)
	// InsertDiffs writes multiple diffs in a single batch.
	InsertDiffs(ctx context.Context, ds []Diff) (int, error)
	// UpdateOperationHashes updates contract hashes for multiple operations
	// in a single batch after a sweep completes.
	UpdateOperationHashes(ctx context.Context, hashes map[int]string) error
}

// Emitter broadcasts diff events (e.g., SSE via api, webhook callback).
type Emitter interface {
	// EmitDiffs broadcasts multiple diff events in a single batch.
	EmitDiffs(ctx context.Context, ds []Diff) error
}

// WriteBudget caps the number of D1 writes per sweep cycle to avoid
// exhausting daily write limits.
type WriteBudget struct {
	MaxObservationsPerSweep int
	MaxDiffsPerSweep        int
	MaxHashUpdatesPerSweep  int
}

// DefaultWriteBudget returns conservative defaults for D1 free-tier limits.
func DefaultWriteBudget() WriteBudget {
	return WriteBudget{
		MaxObservationsPerSweep: 50,
		MaxDiffsPerSweep:        100,
		MaxHashUpdatesPerSweep:  1000,
	}
}

// Pipeline runs the contract-drift pipeline for a single source.
type Pipeline struct {
	logger  *slog.Logger
	fetcher *fetch.Fetcher
	loader  *normalize.SpecLoader
	d1      D1Writer
	emitter Emitter
	prober  Prober
	differ  Differ
	budget  WriteBudget
	// lastShapeHash caches the last known shape hash per operation to
	// avoid writing observations on every successful probe when nothing
	// changed. Populated by the caller (e.g., from D1 query).
	lastShapeHash map[int]string
}

// Prober probes a live endpoint.
type Prober interface {
	Probe(ctx context.Context, op Operation) (ProbeResult, error)
}

// Differ computes contract diffs between spec and live shapes.
type Differ interface {
	Diff(specShape, liveShape any) ([]DiffKind, error)
}

// DiffKind represents a single contract change.
type DiffKind struct {
	Kind     string // field_added, field_removed, type_changed, required_added, enum_removed, status_removed, path_removed
	Severity string // breaking, additive, cosmetic
	Path     string
	SpecVal  any
	LiveVal  any
}

// New creates a Pipeline.
func New(logger *slog.Logger, fetcher *fetch.Fetcher, loader *normalize.SpecLoader, d1 D1Writer, emitter Emitter, prober Prober, differ Differ, budget WriteBudget) *Pipeline {
	if budget.MaxObservationsPerSweep == 0 {
		budget = DefaultWriteBudget()
	}
	return &Pipeline{
		logger:        logger,
		fetcher:       fetcher,
		loader:        loader,
		d1:            d1,
		emitter:       emitter,
		prober:        prober,
		differ:        differ,
		budget:        budget,
		lastShapeHash: make(map[int]string),
	}
}

// Run executes the full pipeline for a single source.
// Returns the number of diffs emitted.
func (p *Pipeline) Run(ctx context.Context, src Source, ops []Operation) (int, error) {
	// Step 1: fetch spec with ETag gate
	result, err := p.fetcher.Fetch(ctx, src.SpecURL, src.ETag)
	if err != nil {
		p.logger.Error("fetch failed", slog.String("url", src.SpecURL), slog.String("error", err.Error()))
		return 0, err
	}

	// Step 2: ETag / If-None-Match → 304? stop
	if result.NotModified {
		p.logger.Debug("spec unchanged (304)", slog.String("url", src.SpecURL))
		return 0, nil
	}

	// Step 3: normalize + scrub (transform - decides what is compared)
	doc, err := p.loader.LoadFromData(result.Body)
	if err != nil {
		p.logger.Error("spec parse failed", slog.String("url", src.SpecURL), slog.String("error", err.Error()))
		return 0, err
	}
	normSpec := normalize.NormalizeSpec(doc)

	// Collect writes for batching to mitigate D1 daily write limits.
	var (
		observations []Observation
		diffs        []Diff
		hashUpdates  = make(map[int]string)
	)

	// Step 4-7: per-operation probe, gate, hash, diff, emit
	for _, op := range ops {
		obs, opDiffs, newHash, err := p.processOperation(ctx, src, op, normSpec)
		if err != nil {
			p.logger.Warn("operation processing skipped",
				slog.Int("operation_id", op.ID),
				slog.String("error", err.Error()))
			continue
		}

		// Observation sampling: only write on state change or error.
		if obs != nil {
			lastHash, seen := p.lastShapeHash[op.ID]
			if !seen || lastHash != newHash || obs.StatusCode == 0 {
				observations = append(observations, *obs)
				if len(observations) >= p.budget.MaxObservationsPerSweep {
					p.logger.Warn("observation budget reached for sweep", slog.String("source_id", src.ID))
					break
				}
			}
			p.lastShapeHash[op.ID] = newHash
		}

		diffs = append(diffs, opDiffs...)
		if len(diffs) >= p.budget.MaxDiffsPerSweep {
			p.logger.Warn("diff budget reached for sweep", slog.String("source_id", src.ID))
			break
		}

		if newHash != "" {
			hashUpdates[op.ID] = newHash
		}
		if len(hashUpdates) >= p.budget.MaxHashUpdatesPerSweep {
			p.logger.Warn("hash update budget reached for sweep", slog.String("source_id", src.ID))
			break
		}
	}

	// Batch write observations (sampled)
	if p.d1 != nil && len(observations) > 0 {
		for _, o := range observations {
			if _, err := p.d1.InsertObservation(ctx, o); err != nil {
				p.logger.Error("insert observation failed", slog.Int("operation_id", o.ProbeID), slog.String("error", err.Error()))
			}
		}
	}

	// Batch write diffs
	if p.d1 != nil && len(diffs) > 0 {
		if _, err := p.d1.InsertDiffs(ctx, diffs); err != nil {
			p.logger.Error("insert diffs failed", slog.String("error", err.Error()))
		} else {
			// Emit diffs via emitter after successful D1 write
			if p.emitter != nil {
				if err := p.emitter.EmitDiffs(ctx, diffs); err != nil {
					p.logger.Error("emit diffs failed", slog.String("error", err.Error()))
				}
			}
		}
	}

	// Batch update operation contract hashes
	if p.d1 != nil && len(hashUpdates) > 0 {
		if err := p.d1.UpdateOperationHashes(ctx, hashUpdates); err != nil {
			p.logger.Error("update operation hashes failed", slog.String("error", err.Error()))
		}
	}

	// Update source ETag
	src.ETag = result.ETag
	src.LastFetched = time.Now().UTC().Format(time.RFC3339)

	return len(diffs), nil
}

// processOperation handles steps 4-7 for a single operation.
// Returns the observation (nil if no state change worth recording),
// the diffs found, and the new shape hash for the operation.
func (p *Pipeline) processOperation(ctx context.Context, src Source, op Operation, normSpec *normalize.NormalizedSpec) (*Observation, []Diff, string, error) {
	// Find the normalized operation matching this operation
	var specOp *normalize.Operation
	for i := range normSpec.Operations {
		if normSpec.Operations[i].Method == op.Method && normSpec.Operations[i].Path == op.Path {
			specOp = &normSpec.Operations[i]
			break
		}
	}

	// Step 4: probe live (single cheap request, no pagination)
	probeResult, err := p.prober.Probe(ctx, op)
	if err != nil {
		// Probe failed → status='inconclusive', stop (never emit diff from failed probe)
		// Always record failed probes (they represent state change)
		p.logger.Warn("probe failed, recording inconclusive",
			slog.Int("operation_id", op.ID),
			slog.String("error", err.Error()))
		obs := &Observation{
			ProbeID:    0, // set by D1
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
			StatusCode: 0,
			ShapeHash:  "",
			BodySample: "",
		}
		return obs, nil, "", nil
	}

	// Build live shape from probe response
	var liveBody any
	if err := json.Unmarshal(probeResult.Body, &liveBody); err != nil {
		// Body isn't JSON — hash raw bytes directly for shape comparison
		liveShapeHash := hash.ShapeHash(string(probeResult.Body))
		specShape := specOp
		if specOp == nil {
			specShape = nil
		}
		specShapeHash := hash.ShapeHash(specShape)
		if specShapeHash == liveShapeHash {
			p.logger.Debug("shape unchanged", slog.Int("operation_id", op.ID))
			return nil, nil, liveShapeHash, nil
		}
		obs := &Observation{
			ProbeID:     0,
			ObservedAt:  time.Now().UTC().Format(time.RFC3339),
			StatusCode:  probeResult.StatusCode,
			ContentType: probeResult.ContentType,
			ShapeHash:   liveShapeHash,
			BodySample:  bodySample(probeResult.Body),
		}
		return obs, nil, liveShapeHash, nil
	}
	liveShape := scrub.ScrubValue(liveBody)
	liveShapeHash := hash.ShapeHash(liveShape)

	// Build spec shape from normalized operation
	specShape := specOp
	if specOp == nil {
		// No matching spec operation - this is a path/method removal
		specShape = nil
	}
	specShapeHash := hash.ShapeHash(specShape)

	// Step 5-6: normalize + scrub + project to shape, hash
	// (already done above via scrub + hash)

	// Step 7: shape_hash unchanged? stop (no observation write needed)
	if specShapeHash == liveShapeHash && probeResult.Err == nil {
		p.logger.Debug("shape unchanged", slog.Int("operation_id", op.ID))
		return nil, nil, liveShapeHash, nil
	}

	// Step 8: detailed contract diff
	var opDiffs []Diff
	if p.differ != nil {
		diffKinds, err := p.differ.Diff(specShape, liveShape)
		if err != nil {
			p.logger.Error("diff failed", slog.Int("operation_id", op.ID), slog.String("error", err.Error()))
			return nil, nil, liveShapeHash, err
		}
		for _, dk := range diffKinds {
			opDiffs = append(opDiffs, Diff{
				OperationID: op.ID,
				DetectedAt:  time.Now().UTC().Format(time.RFC3339),
				Kind:        dk.Kind,
				Severity:    dk.Severity,
				SpecSide:    fmt.Sprintf("%v", dk.SpecVal),
				LiveSide:    fmt.Sprintf("%v", dk.LiveVal),
				Status:      "open",
			})
		}
	}

	// Observation for state change (hash changed or probe error)
	obs := &Observation{
		ProbeID:     0, // set by D1
		ObservedAt:  time.Now().UTC().Format(time.RFC3339),
		StatusCode:  probeResult.StatusCode,
		ContentType: probeResult.ContentType,
		ShapeHash:   liveShapeHash,
		BodySample:  bodySample(probeResult.Body),
	}

	return obs, opDiffs, liveShapeHash, nil
}

// bodySample returns a truncated sample of the body for storage.
// Limits body size to 256 bytes to avoid bloating the observations table.
func bodySample(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	maxLen := 256
	if len(body) > maxLen {
		body = body[:maxLen]
	}
	return string(body)
}

// DefaultDiffer implements Differ using a simple structural comparison.
type DefaultDiffer struct{}

func (d *DefaultDiffer) Diff(specShape, liveShape any) ([]DiffKind, error) {
	if specShape == nil && liveShape == nil {
		return nil, nil
	}
	if specShape == nil {
		return []DiffKind{{Kind: "path_added", Severity: "additive"}}, nil
	}
	if liveShape == nil {
		return []DiffKind{{Kind: "path_removed", Severity: "breaking"}}, nil
	}

	// Deep comparison would go here; for now return a shape-change placeholder
	return []DiffKind{{Kind: "shape_changed", Severity: "cosmetic"}}, nil
}

// compile-time check
var _ Differ = (*DefaultDiffer)(nil)
