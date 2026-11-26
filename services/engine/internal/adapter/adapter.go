// Package adapter defines the Adapter interface that all signal-type
// adapters implement. Per AGENTS.md: "Pipelines are split at the schedule /
// consumer / sink boundary, never at the code boundary. One engine, many
// adapters."
//
// The contract-drift adapter IS the engine. IP sync and release tracking
// are separate adapters that share the same engine; they do not fork it.
package adapter

import (
	"context"
	"log/slog"
)

// Source is a signal source to monitor (e.g., an OpenAPI spec URL, an IP
// range feed URL, a release feed URL).
type Source struct {
	ID      string
	Vendor  string
	URL     string
	Format  string // openapi3, swagger2, ip-range, release-atom, etc.
	Enabled bool
}

// DiffResult is the result of running an adapter for a single source.
type DiffResult struct {
	SourceID  string
	DiffCount int
	Errors    []error
}

// Adapter is implemented by each signal type. Adapters share the engine
// (fetch → gate → normalize → project → hash → diff → emit) but differ in
// what they fetch, how they normalize, and what they do with diffs.
// Do not fork the engine to accommodate a new signal type — add an adapter.
type Adapter interface {
	// Name returns the adapter name (e.g., "contract-drift", "ip-range",
	// "release-tracking").
	Name() string

	// Run executes one sweep cycle for the given source.
	Run(ctx context.Context, src Source) (DiffResult, error)
}

// BaseAdapter provides common fields that all adapters can embed.
type BaseAdapter struct {
	name   string
	Logger *slog.Logger
}

// Name returns the adapter name.
func (b *BaseAdapter) Name() string {
	return b.name
}

// NewBaseAdapter creates a BaseAdapter with the given name and logger.
func NewBaseAdapter(name string, logger *slog.Logger) *BaseAdapter {
	return &BaseAdapter{
		name:   name,
		Logger: logger,
	}
}
