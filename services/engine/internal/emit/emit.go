// Package emit broadcasts contract drift diff events via SSE or webhook
// callback. Adapters implement the Emitter interface for each transport.
package emit

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
)

// DiffEvent is a contract drift finding broadcast to consumers.
type DiffEvent struct {
	OperationID int    `json:"operation_id"`
	DetectedAt  string `json:"detected_at"`
	Kind        string `json:"kind"`
	Severity    string `json:"severity"`
	Path        string `json:"path"`
	SpecSide    string `json:"spec_side"`
	LiveSide    string `json:"live_side"`
	Status      string `json:"status"` // open, acknowledged, resolved, inconclusive
}

// Emitter broadcasts diff events to downstream consumers.
type Emitter interface {
	EmitDiff(ctx context.Context, event DiffEvent) error
}

// SSEHub is the interface for the api SSE hub (decoupled from api internals).
type SSEHub interface {
	Broadcast(event any)
}

// SSEEmitter broadcasts diff events via Server-Sent Events to an api hub.
type SSEEmitter struct {
	logger *slog.Logger
	hub    SSEHub
}

// NewSSEEmitter creates an SSE emitter.
func NewSSEEmitter(logger *slog.Logger, hub SSEHub) *SSEEmitter {
	return &SSEEmitter{logger: logger, hub: hub}
}

// EmitDiff broadcasts a diff event via SSE.
func (e *SSEEmitter) EmitDiff(ctx context.Context, event DiffEvent) error {
	e.hub.Broadcast(event)
	e.logger.Debug("diff event broadcast via SSE",
		slog.String("kind", event.Kind),
		slog.String("severity", event.Severity),
		slog.String("status", event.Status),
	)
	return nil
}

// WebhookClient is the interface for HTTP POST (decoupled from http.Client).
type WebhookClient interface {
	Post(ctx context.Context, url string, body []byte, headers map[string]string) error
}

// WebhookEmitter broadcasts diff events via HTTP webhook callback.
type WebhookEmitter struct {
	logger   *slog.Logger
	client   WebhookClient
	endpoint string
	secret   string
}

// NewWebhookEmitter creates a webhook emitter.
func NewWebhookEmitter(logger *slog.Logger, client WebhookClient, endpoint, secret string) *WebhookEmitter {
	return &WebhookEmitter{
		logger:   logger,
		client:   client,
		endpoint: endpoint,
		secret:   secret,
	}
}

// EmitDiff broadcasts a diff event via webhook callback.
func (e *WebhookEmitter) EmitDiff(ctx context.Context, event DiffEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	headers := map[string]string{
		"Content-Type":      "application/json",
		"X-Internal-Secret": e.secret,
	}

	if err := e.client.Post(ctx, e.endpoint, body, headers); err != nil {
		return fmt.Errorf("webhook post: %w", err)
	}

	e.logger.Debug("diff event delivered via webhook",
		slog.String("kind", event.Kind),
		slog.String("severity", event.Severity),
	)
	return nil
}

// CompositeEmitter fans out to multiple emitters.
type CompositeEmitter struct {
	emitters []Emitter
	logger   *slog.Logger
}

// NewCompositeEmitter creates a composite emitter.
func NewCompositeEmitter(logger *slog.Logger, emitters ...Emitter) *CompositeEmitter {
	return &CompositeEmitter{
		emitters: emitters,
		logger:   logger,
	}
}

// EmitDiff fans out to all emitters.
func (e *CompositeEmitter) EmitDiff(ctx context.Context, event DiffEvent) error {
	var firstErr error
	for _, em := range e.emitters {
		if err := em.EmitDiff(ctx, event); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			e.logger.Error("emitter failed",
				slog.String("error", err.Error()),
			)
		}
	}
	return firstErr
}
