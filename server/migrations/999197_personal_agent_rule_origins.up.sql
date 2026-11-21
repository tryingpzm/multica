-- personal-agent each rule copied from the Router template: its template version and the rule as copied.
ALTER TABLE personal_agent ADD COLUMN IF NOT EXISTS rule_origins JSONB NOT NULL DEFAULT '{}'::jsonb;
