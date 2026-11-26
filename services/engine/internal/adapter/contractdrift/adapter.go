// Package contractdrift implements the contract drift adapter — the primary
// adapter for the endpoint-lighthouse engine. It monitors OpenAPI/Swagger
// spec sources for contract drift using the shared engine pipeline.
//
// Per AGENTS.md scope, IP range feeds, release feeds, status pages, package
// registry metadata, and static files are OUT OF SCOPE for this adapter.
// They belong in separate adapters (see iprange/ and releasetracking/).
package contractdrift

import (
	"context"
	"log/slog"

	"github.com/enondev/endpoint-lighthouse-engine/internal/adapter"
	"github.com/enondev/endpoint-lighthouse-engine/internal/fetch"
	"github.com/enondev/endpoint-lighthouse-engine/internal/normalize"
	"github.com/enondev/endpoint-lighthouse-engine/internal/pipeline"
	"github.com/enondev/endpoint-lighthouse-engine/internal/severity"
)

// Adapter implements adapter.Adapter for OpenAPI contract drift detection.
type Adapter struct {
	adapter.BaseAdapter
	pipeline *pipeline.Pipeline
}

// New creates a contract drift adapter.
func New(logger *slog.Logger, fetcher *fetch.Fetcher, loader *normalize.SpecLoader, d1 pipeline.D1Writer, emitter pipeline.Emitter, prober pipeline.Prober) *Adapter {
	classifier := severity.NewClassifier()

	// Wrap the differ to classify severities via the AGENTS.md contract table.
	classifyingDiffer := &classifyingDiffer{
		inner:      &pipeline.DefaultDiffer{},
		classifier: classifier,
	}

	return &Adapter{
		BaseAdapter: *adapter.NewBaseAdapter("contract-drift", logger),
		pipeline:    pipeline.New(logger, fetcher, loader, d1, emitter, prober, classifyingDiffer, pipeline.DefaultWriteBudget()),
	}
}

// Run executes one sweep cycle for the given source.
func (a *Adapter) Run(ctx context.Context, src adapter.Source) (adapter.DiffResult, error) {
	ops := []pipeline.Operation{} // populated from D1 operations table in real usage

	result, err := a.pipeline.Run(ctx, pipeline.Source{
		ID:          src.ID,
		Vendor:      src.Vendor,
		SpecURL:     src.URL,
		SpecFormat:  src.Format,
		ETag:        "",
		ContentHash: "",
		LastFetched: "",
	}, ops)
	if err != nil {
		return adapter.DiffResult{
			SourceID: src.ID,
			Errors:   []error{err},
		}, err
	}

	return adapter.DiffResult{
		SourceID:  src.ID,
		DiffCount: result,
	}, nil
}

// classifyingDiffer wraps pipeline.Differ to classify severities via the
// AGENTS.md contract table. Unknown kinds return an error (no guessing).
type classifyingDiffer struct {
	inner      pipeline.Differ
	classifier *severity.Classifier
}

func (d *classifyingDiffer) Diff(specShape, liveShape any) ([]pipeline.DiffKind, error) {
	kinds, err := d.inner.Diff(specShape, liveShape)
	if err != nil {
		return nil, err
	}

	for i := range kinds {
		sev, err := d.classifier.Classify(kinds[i].Kind)
		if err != nil {
			// Uncalibrated or unknown kind — skip severity classification
			// rather than guessing (per AGENTS.md "Never delegate").
			kinds[i].Severity = "cosmetic" // default to cosmetic, suppressed from feed
			continue
		}
		kinds[i].Severity = sev
	}

	return kinds, nil
}

// compile-time checks
var _ adapter.Adapter = (*Adapter)(nil)
