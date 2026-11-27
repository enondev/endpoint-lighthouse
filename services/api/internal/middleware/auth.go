package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/enondev/endpoint-lighthouse-api/internal/cloudflare"
)

// accountIDKey is the context key for the authenticated account ID.
type accountIDKey struct{}

// accountKey is the context key for the full account record.
type accountKey struct{}

// Account represents an authenticated account.
type Account struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	APIKey         string `json:"api_key"`
	PlanTier       string `json:"plan_tier"`
	RateLimitTier  string `json:"rate_limit_tier"`
	WebhookLimit   int    `json:"webhook_endpoint_limit"`
	EndpointLimit  int    `json:"endpoint_limit"`
	IncidentLimit  int    `json:"incident_limit_monthly"`
}

// AuthMiddleware validates X-API-Key header and injects account context.
type AuthMiddleware struct {
	d1     *cloudflare.D1Client
	logger *slog.Logger
}

// NewAuthMiddleware creates a new auth middleware.
func NewAuthMiddleware(d1 *cloudflare.D1Client, logger *slog.Logger) *AuthMiddleware {
	return &AuthMiddleware{d1: d1, logger: logger}
}

// Middleware returns an http.Handler that validates the X-API-Key header.
// If no API key is provided, the request passes through unauthenticated
// (for backward compatibility with existing callers that don't use API keys).
// If an API key is provided but invalid, returns 401.
func (m *AuthMiddleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey := r.Header.Get("X-API-Key")
		if apiKey == "" {
			// No API key provided - allow request through without account context.
			// This maintains backward compatibility with existing callers.
			next.ServeHTTP(w, r)
			return
		}

		account, err := m.lookupAccountByAPIKey(r.Context(), apiKey)
		if err != nil {
			m.logger.Warn("api key lookup failed", slog.String("error", err.Error()))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if account == nil {
			m.logger.Warn("api key not found", slog.String("key_prefix", apiKey[:8]))
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}

		// Inject account into request context
		ctx := context.WithValue(r.Context(), accountKey{}, *account)
		ctx = context.WithValue(ctx, accountIDKey{}, account.ID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// lookupAccountByAPIKey looks up an account by its API key.
func (m *AuthMiddleware) lookupAccountByAPIKey(ctx context.Context, apiKey string) (*Account, error) {
	rows, err := m.d1.Query(ctx, `
		SELECT id, name, api_key, plan_tier, rate_limit_tier,
		       webhook_endpoint_limit, endpoint_limit, incident_limit_monthly,
		       created_at, updated_at
		FROM accounts
		WHERE api_key = ?
		LIMIT 1
	`, apiKey)
	if err != nil {
		return nil, fmt.Errorf("query accounts: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	row := rows[0]
	account := &Account{
		ID:            toString(row["id"]),
		Name:          toString(row["name"]),
		APIKey:        toString(row["api_key"]),
		PlanTier:      toString(row["plan_tier"]),
		RateLimitTier: toString(row["rate_limit_tier"]),
		WebhookLimit:  toInt(row["webhook_endpoint_limit"]),
		EndpointLimit: toInt(row["endpoint_limit"]),
		IncidentLimit: toInt(row["incident_limit_monthly"]),
	}
	return account, nil
}

// GetAccountFromContext returns the account from the request context.
func GetAccountFromContext(ctx context.Context) (*Account, bool) {
	acc, ok := ctx.Value(accountKey{}).(Account)
	if !ok {
		return nil, false
	}
	return &acc, true
}

// GetAccountIDFromContext returns the account ID from the request context.
func GetAccountIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(accountIDKey{}).(string)
	if !ok {
		return "", false
	}
	return id, true
}

// toString is a helper to safely convert interface{} to string.
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// toInt is a helper to convert interface{} to int.
func toInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
