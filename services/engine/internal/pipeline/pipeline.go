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
type D1Writer interface {
	InsertObservation(ctx context.Context, o Observation) (int, error)
	InsertDiff(ctx context.Context, d Diff) (int, error)
	UpdateOperationHash(ctx context.Context, opID int, hash string) error
}

// Emitter broadcasts diff events (e.g., SSE via api, webhook callback).
type Emitter interface {
	EmitDiff(ctx context.Context, d Diff) error
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
func New(logger *slog.Logger, fetcher *fetch.Fetcher, loader *normalize.SpecLoader, d1 D1Writer, emitter Emitter, prober Prober, differ Differ) *Pipeline {
	return &Pipeline{
		logger:  logger,
		fetcher: fetcher,
		loader:  loader,
		d1:      d1,
		emitter: emitter,
		prober:  prober,
		differ:  differ,
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

	// Step 4-7: per-operation probe, gate, hash, diff, emit
	emitted := 0
	for _, op := range ops {
		diffs, err := p.processOperation(ctx, src, op, normSpec)
		if err != nil {
			p.logger.Warn("operation processing skipped",
				slog.Int("operation_id", op.ID),
				slog.String("error", err.Error()))
			continue
		}
		emitted += len(diffs)
	}

	// Update source ETag
	src.ETag = result.ETag
	src.LastFetched = time.Now().UTC().Format(time.RFC3339)

	return emitted, nil
}

// processOperation handles steps 4-7 for a single operation.
func (p *Pipeline) processOperation(ctx context.Context, src Source, op Operation, normSpec *normalize.NormalizedSpec) ([]Diff, error) {
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
		p.logger.Warn("probe failed, recording inconclusive",
			slog.Int("operation_id", op.ID),
			slog.String("error", err.Error()))
		obs := Observation{
			ProbeID:    0, // set by D1
			ObservedAt: time.Now().UTC().Format(time.RFC3339),
			StatusCode: 0,
			ShapeHash:  "",
			BodySample: "",
		}
		if p.d1 != nil {
			if _, err := p.d1.InsertObservation(ctx, obs); err != nil {
				p.logger.Error("insert observation failed", slog.Int("operation_id", op.ID), slog.String("error", err.Error()))
			}
		}
		return nil, nil // no diff emitted
	}

	// Build live shape from probe response
	liveShape := scrub.ScrubValue(probeResult.Body)
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

	// Step 7: shape_hash unchanged? stop
	if specShapeHash == liveShapeHash && probeResult.Err == nil {
		p.logger.Debug("shape unchanged", slog.Int("operation_id", op.ID))
		return nil, nil
	}

	// Step 8: detailed contract diff
	var diffs []Diff
	if p.differ != nil {
		diffKinds, err := p.differ.Diff(specShape, liveShape)
		if err != nil {
			p.logger.Error("diff failed", slog.Int("operation_id", op.ID), slog.String("error", err.Error()))
			return nil, err
		}
		for _, dk := range diffKinds {
			diffs = append(diffs, Diff{
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

	// Step 9: emit
	if p.d1 != nil {
		for _, d := range diffs {
			if _, err := p.d1.InsertDiff(ctx, d); err != nil {
				p.logger.Error("insert diff failed", slog.Int("operation_id", op.ID), slog.String("error", err.Error()))
			}
		}
	}
	if p.emitter != nil {
		for _, d := range diffs {
			if err := p.emitter.EmitDiff(ctx, d); err != nil {
				p.logger.Error("emit diff failed", slog.Int("operation_id", op.ID), slog.String("error", err.Error()))
			}
		}
	}

	// Update operation contract hash
	if p.d1 != nil {
		if err := p.d1.UpdateOperationHash(ctx, op.ID, specShapeHash); err != nil {
			p.logger.Error("update operation hash failed", slog.Int("operation_id", op.ID), slog.String("error", err.Error()))
		}
	}

	return diffs, nil
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
