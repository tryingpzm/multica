-- personal-agent: each Source's switch and the configuration that is not a scope row, keyed by
-- Source: {"github": {"enabled": true, "config": {"userId": "...", "names": ["..."]}}}. Scope rows stay in
-- pa_source_scope; Settings merges them into config.rows.
ALTER TABLE personal_agent ADD COLUMN IF NOT EXISTS sources JSONB NOT NULL DEFAULT '{}'::jsonb;
