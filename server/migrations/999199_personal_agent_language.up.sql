-- personal-agent the language the Router writes reasons in.
ALTER TABLE personal_agent ADD COLUMN IF NOT EXISTS language TEXT NOT NULL DEFAULT 'zh';
