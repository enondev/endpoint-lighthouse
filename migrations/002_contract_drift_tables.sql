-- Migration 002: Contract drift tables (AGENTS.md engine)
-- Adds tables for OpenAPI/Swagger contract drift detection.
-- These tables are separate from the endpoint data plane tables
-- (endpoints, schema_baselines, probe_runs, drift_incidents) created in 001.

-- Table: sources -- vendor spec sources to monitor for contract drift
CREATE TABLE IF NOT EXISTS sources (
    id INTEGER PRIMARY KEY,
    vendor TEXT NOT NULL,
    spec_url TEXT NOT NULL UNIQUE,
    spec_format TEXT,
    pinned_version TEXT,
    etag TEXT,
    content_hash TEXT,
    last_fetched TEXT
);

-- Table: operations -- individual API operations extracted from each source spec
-- Join key between spec and live is (source_id, method, path_template)
CREATE TABLE IF NOT EXISTS operations (
    id INTEGER PRIMARY KEY,
    source_id INTEGER NOT NULL REFERENCES sources(id),
    method TEXT NOT NULL,
    path_template TEXT NOT NULL,
    operation_id TEXT,
    contract_hash TEXT,
    first_seen TEXT,
    last_seen TEXT,
    UNIQUE (source_id, method, path_template)
);

-- Table: probes -- live endpoint probing per operation
CREATE TABLE IF NOT EXISTS probes (
    id INTEGER PRIMARY KEY,
    operation_id INTEGER NOT NULL REFERENCES operations(id),
    live_url TEXT NOT NULL,
    auth_ref TEXT,
    last_probed TEXT,
    last_status INTEGER
);

-- Table: observations -- per-probe observation records
CREATE TABLE IF NOT EXISTS observations (
    id INTEGER PRIMARY KEY,
    probe_id INTEGER NOT NULL REFERENCES probes(id),
    observed_at TEXT NOT NULL,
    status_code INTEGER,
    content_type TEXT,
    shape_hash TEXT,
    body_sample TEXT
);

-- Table: diffs -- contract drift findings
CREATE TABLE IF NOT EXISTS diffs (
    id INTEGER PRIMARY KEY,
    operation_id INTEGER NOT NULL REFERENCES operations(id),
    detected_at TEXT NOT NULL,
    kind TEXT NOT NULL,
    severity TEXT,
    spec_side TEXT,
    live_side TEXT,
    status TEXT DEFAULT 'open'
);

-- Indexes for common query patterns
CREATE INDEX IF NOT EXISTS idx_operations_source_id ON operations(source_id);
CREATE INDEX IF NOT EXISTS idx_operations_method_path ON operations(method, path_template);
CREATE INDEX IF NOT EXISTS idx_probes_operation_id ON probes(operation_id);
CREATE INDEX IF NOT EXISTS idx_observations_probe_id ON observations(probe_id);
CREATE INDEX IF NOT EXISTS idx_diffs_operation_id ON diffs(operation_id);
CREATE INDEX IF NOT EXISTS idx_diffs_severity ON diffs(severity);
CREATE INDEX IF NOT EXISTS idx_diffs_status ON diffs(status);
