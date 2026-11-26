// Package releasetracking implements the release/version feed adapter.
//
// OUT OF SCOPE per AGENTS.md: Release/version feeds
// (/repos/{owner}/{repo}/releases/latest) are version feeds, not contracts.
// Use the GitHub release webhook or
// https://github.com/{owner}/{repo}/releases.atom instead. Polling a release
// endpoint to detect "version changed" is strictly worse than subscribing.
// If version tracking is needed, it belongs in a component_versions table,
// not sources.
//
// This stub exists to document the adapter pattern only; it is not wired
// into the engine.
package releasetracking

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enondev/endpoint-lighthouse-engine/internal/adapter"
)

// Adapter implements adapter.Adapter for release feeds (out of scope).
type Adapter struct {
	adapter.BaseAdapter
}

// New creates a release tracking adapter.
func New(logger *slog.Logger) *Adapter {
	return &Adapter{
		BaseAdapter: *adapter.NewBaseAdapter("release-tracking", logger),
	}
}

// Run is not implemented — release feeds are out of scope.
func (a *Adapter) Run(ctx context.Context, src adapter.Source) (adapter.DiffResult, error) {
	return adapter.DiffResult{}, fmt.Errorf("release-tracking adapter is out of scope per AGENTS.md")
}

// compile-time check
var _ adapter.Adapter = (*Adapter)(nil)
