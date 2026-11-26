// Package iprange implements the IP range feed adapter.
//
// OUT OF SCOPE per AGENTS.md: IP range feeds (/client/v4/ips,
// ip-ranges.amazonaws.com, cloud.google.com/ipranges) are data feeds, not
// contracts. They belong in a separate allowlist-sync pipeline with ETag
// caching that pushes into a config store. Adding them here produces
// permanent noise.
//
// This stub exists to document the adapter pattern only; it is not wired
// into the engine.
package iprange

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/enondev/endpoint-lighthouse-engine/internal/adapter"
)

// Adapter implements adapter.Adapter for IP range feeds (out of scope).
type Adapter struct {
	adapter.BaseAdapter
}

// New creates an IP range adapter.
func New(logger *slog.Logger) *Adapter {
	return &Adapter{
		BaseAdapter: *adapter.NewBaseAdapter("ip-range", logger),
	}
}

// Run is not implemented — IP range feeds are out of scope.
func (a *Adapter) Run(ctx context.Context, src adapter.Source) (adapter.DiffResult, error) {
	return adapter.DiffResult{}, fmt.Errorf("ip-range adapter is out of scope per AGENTS.md")
}

// compile-time check
var _ adapter.Adapter = (*Adapter)(nil)
