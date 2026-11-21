-- personal-agent execution: the Router session and Worker agent of each personal agent, the durable
-- effect queue (SA src/app/effects.ts) and the Watchdog's findings. A ticket's Multica issue is pa_ticket.issue_id;
-- the run an Assignment last started is pa_assignment.task_id, enqueued at task_started_at.
ALTER TABLE personal_agent ADD COLUMN IF NOT EXISTS router_agent_id UUID NULL;
ALTER TABLE personal_agent ADD COLUMN IF NOT EXISTS worker_agent_id UUID NULL;
ALTER TABLE personal_agent ADD COLUMN IF NOT EXISTS router_session_id UUID NULL;
ALTER TABLE pa_assignment ADD COLUMN IF NOT EXISTS task_started_at TEXT NULL;
CREATE INDEX IF NOT EXISTS idx_pa_assignment_task ON pa_assignment (task_id) WHERE task_id IS NOT NULL;
CREATE UNIQUE INDEX IF NOT EXISTS idx_personal_agent_router_session ON personal_agent (router_session_id) WHERE router_session_id IS NOT NULL;

-- One queued side effect per (agent, kind, key): seq keeps its place in the queue across re-queues and restarts,
-- generation counts payload updates so a running attempt never deletes a newer payload.
CREATE TABLE IF NOT EXISTS pa_effect (
    agent_id   UUID    NOT NULL,
    kind       TEXT    NOT NULL,
    key        TEXT    NOT NULL,
    value      JSONB   NOT NULL,
    seq        BIGSERIAL,
    generation INTEGER NOT NULL DEFAULT 0,
    attempts   INTEGER NOT NULL DEFAULT 0,
    due        BIGINT  NOT NULL,
    since      TEXT    NOT NULL,
    error      TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (agent_id, kind, key)
);
CREATE INDEX IF NOT EXISTS idx_pa_effect_due ON pa_effect (kind, due, seq);

-- What the Watchdog found stuck: a run that does not end, or an open ticket whose Assignment has no run.
CREATE TABLE IF NOT EXISTS pa_watchdog (
    agent_id      UUID NOT NULL,
    ticket_id     TEXT NOT NULL,
    assignment_id TEXT NOT NULL,
    kind          TEXT NOT NULL CHECK (kind IN ('run_overdue', 'no_run')),
    since         TEXT NOT NULL,
    PRIMARY KEY (agent_id, ticket_id)
);
