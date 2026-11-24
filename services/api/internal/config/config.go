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
	RedisURL              string
	InternalWebhookSecret string
	SnapDeployWorkerURL   string
	CooldownTTL           time.Duration
	AllowedOrigin         string
	CFAPIToken            string
	CFAccountID           string
	CFD1DatabaseID        string
	ReconcilePeriod       time.Duration
}

func Load() Config {
	loadDotEnv(".env", "../../.env")

	ttlSec, err := strconv.Atoi(getEnv("DRIFT_COOLDOWN_SEC", "900"))
	if err != nil || ttlSec <= 0 {
		ttlSec = 900
	}

	rpMin, err := strconv.Atoi(getEnv("RECONCILE_PERIOD", "7"))
	if err != nil || rpMin <= 0 {
		rpMin = 7
	}

	return Config{
		Port:                  getEnv("PORT", "8080"),
		RedisURL:              getEnv("REDIS_URL", "redis://localhost:6379"),
		InternalWebhookSecret: getEnv("INTERNAL_WEBHOOK_SECRET", ""),
		SnapDeployWorkerURL:   getEnv("SNAPDEPLOY_WORKER_URL", ""),
		CooldownTTL:           time.Duration(ttlSec) * time.Second,
		AllowedOrigin:         getEnv("ALLOWED_ORIGIN", "*"),
		CFAPIToken:            os.Getenv("CF_API_TOKEN"),
		CFAccountID:           os.Getenv("CF_ACCOUNT_ID"),
		CFD1DatabaseID:        os.Getenv("CF_D1_DATABASE_ID"),
		ReconcilePeriod:       time.Duration(rpMin) * time.Minute,
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
