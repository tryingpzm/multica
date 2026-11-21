-- personal-agent a Source's polling state (SA ctx.state): the channel poll's cursors and last run.
ALTER TABLE personal_agent ADD COLUMN IF NOT EXISTS source_state JSONB NOT NULL DEFAULT '{}'::jsonb;
