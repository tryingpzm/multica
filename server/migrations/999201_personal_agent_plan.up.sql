-- personal-agent a plan the owner's executor proposes for an issue that needs them: one per issue and
-- issue status; it goes stale when the issue moves on.
CREATE TABLE IF NOT EXISTS pa_plan (
    id TEXT PRIMARY KEY,
    agent_id TEXT NOT NULL,
    owner_id TEXT NOT NULL,
    issue_id TEXT NOT NULL,
    issue_status TEXT NOT NULL,
    conclusion TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    actions JSONB NOT NULL DEFAULT '[]',
    state TEXT NOT NULL DEFAULT 'pending',
    reject_reason TEXT NOT NULL DEFAULT '',
    results JSONB NOT NULL DEFAULT '[]',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (agent_id, issue_id, issue_status)
);