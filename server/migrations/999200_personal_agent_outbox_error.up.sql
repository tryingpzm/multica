-- personal-agent why a reply did not go out, shown to its owner.
ALTER TABLE pa_outbox ADD COLUMN IF NOT EXISTS last_error TEXT NOT NULL DEFAULT '';
