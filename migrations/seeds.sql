-- ==========================================================================
-- Endpoint Lighthouse - Seed data (from export.csv, live system endpoints)
-- In-scope only per AGENTS.md: excludes IP range feeds, release feeds,
-- status pages, package registry metadata, static files, and malformed URLs.
-- Run after migrations/schema.sql.
-- ==========================================================================

PRAGMA foreign_keys = ON;

-- Endpoints (endpoint payload-schema drift monitor data plane)
INSERT OR IGNORE INTO endpoints (id, name, category, url, method, poll_interval_sec, is_active, created_at) VALUES
    -- Financial & Market Data
    ('frankfurter-usd', 'Frankfurter Exchange Rates API', 'finance', 'https://api.frankfurter.dev/v1/latest?base=USD', 'GET', 900, 1, '2026-11-26T00:00:00Z'),
    ('kraken-btcusd', 'Kraken BTC/USD Ticker', 'finance', 'https://api.kraken.com/0/public/Ticker?pair=XBTUSD', 'GET', 900, 1, '2026-11-26T00:00:00Z'),
    ('mempool-btc-fees', 'Mempool.space BTC Fees', 'finance', 'https://mempool.space/api/v1/fees/recommended', 'GET', 900, 1, '2026-11-26T00:00:00Z'),

    -- Registry metadata
    ('github-api-meta', 'GitHub API Meta', 'registry', 'https://api.github.com/meta', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),

    -- OpenAPI / Swagger specs (contract drift sources)
    ('petstore-swagger-spec', 'Petstore Swagger 2.0 Spec', 'openapi', 'https://petstore.swagger.io/v2/swagger.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('github-openapi', 'GitHub REST API OpenAPI Spec', 'openapi', 'https://raw.githubusercontent.com/github/rest-api-description/refs/heads/main/descriptions/api.github.com/api.github.com.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('github-enterprise-cloud-openapi', 'GitHub Enterprise Cloud OpenAPI Spec', 'openapi', 'https://raw.githubusercontent.com/github/rest-api-description/refs/heads/main/descriptions/ghec/ghec.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('cloudflare-openapi', 'Cloudflare API OpenAPI Spec', 'openapi', 'https://raw.githubusercontent.com/cloudflare/api-schemas/refs/heads/main/openapi.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('bitbucket-openapi', 'Bitbucket Cloud Swagger Spec', 'openapi', 'https://dac-static.atlassian.com/cloud/bitbucket/swagger.v3.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('huggingface-endpoints-openapi', 'Hugging Face Endpoints OpenAPI Spec', 'openapi', 'https://api.endpoints.huggingface.cloud/openapi.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('vercel-openapi', 'Vercel OpenAPI Spec', 'openapi', 'https://openapi.vercel.sh/', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('stripe-openapi', 'Stripe API OpenAPI Spec', 'openapi', 'https://raw.githubusercontent.com/stripe/openapi/refs/heads/master/latest/openapi.spec3.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('kubernetes-openapi', 'Kubernetes API OpenAPI Spec', 'openapi', 'https://raw.githubusercontent.com/kubernetes/kubernetes/refs/heads/master/api/openapi-spec/swagger.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('mongodb-openapi', 'MongoDB OpenAPI Spec', 'openapi', 'https://raw.githubusercontent.com/mongodb/openapi/refs/heads/main/openapi/v2.json', 'GET', 3600, 1, '2026-11-26T00:00:00Z'),
    ('docker-engine-api', 'Docker Engine API Swagger Spec', 'openapi', 'https://raw.githubusercontent.com/moby/moby/master/api/swagger.yaml', 'GET', 3600, 1, '2026-11-26T00:00:00Z');

-- Sources (AGENTS.md contract-drift engine spec sources)
-- One row per OpenAPI/Swagger spec endpoint above. spec_format: 'openapi3' or 'swagger2'.
INSERT OR IGNORE INTO sources (id, vendor, spec_url, spec_format, pinned_version, etag, content_hash, last_fetched) VALUES
    ('src_petstore', 'SmartBear', 'https://petstore.swagger.io/v2/swagger.json', 'swagger2', NULL, NULL, NULL, NULL),
    ('src_github', 'GitHub', 'https://raw.githubusercontent.com/github/rest-api-description/refs/heads/main/descriptions/api.github.com/api.github.com.json', 'openapi3', NULL, NULL, NULL, NULL),
    ('src_github_ghec', 'GitHub', 'https://raw.githubusercontent.com/github/rest-api-description/refs/heads/main/descriptions/ghec/ghec.json', 'openapi3', NULL, NULL, NULL, NULL),
    ('src_cloudflare', 'Cloudflare', 'https://raw.githubusercontent.com/cloudflare/api-schemas/refs/heads/main/openapi.json', 'openapi3', NULL, NULL, NULL, NULL),
    ('src_bitbucket', 'Atlassian', 'https://dac-static.atlassian.com/cloud/bitbucket/swagger.v3.json', 'swagger2', NULL, NULL, NULL, NULL),
    ('src_huggingface', 'Hugging Face', 'https://api.endpoints.huggingface.cloud/openapi.json', 'openapi3', NULL, NULL, NULL, NULL),
    ('src_vercel', 'Vercel', 'https://openapi.vercel.sh/', 'openapi3', NULL, NULL, NULL, NULL),
    ('src_stripe', 'Stripe', 'https://raw.githubusercontent.com/stripe/openapi/refs/heads/master/latest/openapi.spec3.json', 'openapi3', NULL, NULL, NULL, NULL),
    ('src_kubernetes', 'Kubernetes', 'https://raw.githubusercontent.com/kubernetes/kubernetes/refs/heads/master/api/openapi-spec/swagger.json', 'swagger2', NULL, NULL, NULL, NULL),
    ('src_mongodb', 'MongoDB', 'https://raw.githubusercontent.com/mongodb/openapi/refs/heads/main/openapi/v2.json', 'swagger2', NULL, NULL, NULL, NULL),
    ('src_docker_engine', 'Docker/Moby', 'https://raw.githubusercontent.com/moby/moby/master/api/swagger.yaml', 'swagger2', NULL, NULL, NULL, NULL);

-- Out of scope per AGENTS.md (intentionally NOT seeded):
--   openai-openapi   → wrong target (Discord spec, not OpenAI)  [see migrations/old.sql]
--   crates.io-openapi → malformed URL (missing scheme)           [see migrations/old.sql]
--   cloudflare-ips    → IP range feed (out of scope)             [AGENTS.md Scope]
--   docker-engine-release → release feed (out of scope)          [AGENTS.md Scope]
--   containerd-release → release feed (out of scope)             [AGENTS.md Scope]
