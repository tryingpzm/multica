-- personal-agent the names of the groups and channels a Source's rows can choose from, by Source
-- (SA KV sources.labels): {"github": {"<chat id>": "<name>"}, ...}.
ALTER TABLE personal_agent ADD COLUMN IF NOT EXISTS labels JSONB NOT NULL DEFAULT '{}'::jsonb;
