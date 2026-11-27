package middleware

import (
	"net/http"
	"sync"
	"time"
)

// RateLimiter is a simple token bucket rate limiter per client IP.
type RateLimiter struct {
	clients map[string]*clientLimiter
	mu      sync.Mutex
	rate    float64 // requests per second
	burst   int     // max burst size
}

type clientLimiter struct {
	tokens   float64
	lastSeen time.Time
}

// NewRateLimiter creates a new rate limiter.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	return &RateLimiter{
		clients: make(map[string]*clientLimiter),
		rate:    rate,
		burst:   burst,
	}
}

// Allow checks if a request from the given client is allowed.
func (r *RateLimiter) Allow(clientIP string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	client, exists := r.clients[clientIP]
	if !exists {
		client = &clientLimiter{
			tokens:   float64(r.burst),
			lastSeen: time.Now(),
		}
		r.clients[clientIP] = client
	}

	now := time.Now()
	elapsed := now.Sub(client.lastSeen).Seconds()
	client.lastSeen = now

	client.tokens += elapsed * r.rate
	if client.tokens > float64(r.burst) {
		client.tokens = float64(r.burst)
	}

	if client.tokens < 1 {
		return false
	}
	client.tokens--
	return true
}

// RateLimitMiddleware returns middleware that limits requests per client IP.
func RateLimitMiddleware(rate float64, burst int) func(http.Handler) http.Handler {
	limiter := NewRateLimiter(rate, burst)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			clientIP := r.RemoteAddr
			if !limiter.Allow(clientIP) {
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
