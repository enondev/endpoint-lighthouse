package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port                  string
	CFAPIToken            string
	CFAccountID           string
	CFD1DatabaseID        string
	CFKVNamespaceID       string
	RenderAPIURL          string
	AIWorkerURL           string
	InternalWebhookSecret string
	AIWorkerSecret        string
	InternalAPIKey        string
	MaxBodyBytes          int64
	RequestTimeout        time.Duration
	MaxStartupStagger     time.Duration
	RenderWarmInterval    time.Duration
	UserAgent             string
}

func Load() Config {
	loadDotEnv(".env", "../../.env")

	return Config{
		Port:                  getEnv("PORT", "8080"),
		CFAPIToken:            os.Getenv("CF_API_TOKEN"),
		CFAccountID:           os.Getenv("CF_ACCOUNT_ID"),
		CFD1DatabaseID:        os.Getenv("CF_D1_DATABASE_ID"),
		CFKVNamespaceID:       os.Getenv("CF_KV_NAMESPACE_ID"),
		RenderAPIURL:          os.Getenv("RENDER_API_URL"),
		AIWorkerURL:           os.Getenv("AI_WORKER_URL"),
		InternalWebhookSecret: os.Getenv("INTERNAL_WEBHOOK_SECRET"),
		AIWorkerSecret:        os.Getenv("AI_WORKER_SECRET"),
		InternalAPIKey:        os.Getenv("INTERNAL_API_KEY"),
		MaxBodyBytes:          int64(getEnvInt("MAX_BODY_BYTES", 5*1024*1024)),
		RequestTimeout:        time.Duration(getEnvInt("REQUEST_TIMEOUT_SEC", 15)) * time.Second,
		MaxStartupStagger:     time.Duration(getEnvInt("MAX_STARTUP_STAGGER_SEC", 15)) * time.Second,
		RenderWarmInterval:    time.Duration(getEnvInt("RENDER_WARM_INTERVAL_SEC", 600)) * time.Second,
		UserAgent:             getEnv("USER_AGENT", "EndpointLighthouse-Prober/0.1 (Go) - please get in touch to remove this endpoint from the drift observation (+https://github.com/enondev/endpoint-lighthouse)"),
	}
}

// loadDotEnv reads KEY=VALUE pairs from the first existing file without overriding existing OS env vars.
func loadDotEnv(paths ...string) {
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			key, val, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)
			val = strings.Trim(val, `"'`)

			if key != "" && os.Getenv(key) == "" {
				_ = os.Setenv(key, val)
			}
		}
		return
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil {
			return parsed
		}
	}
	return fallback
}
