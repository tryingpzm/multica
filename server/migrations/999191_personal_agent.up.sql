-- personal-agent: durable state of each person's personal agent (design §5), a Go/PostgreSQL
-- translation of dsh-superagent-prototype's SQLite store. Aggregates keep SA's JSON structure in `value` so the
-- translated SA specs run against them unchanged; the key columns, pa_subscription, pa_eval, pa_assignment and
-- pa_report are projections the store rewrites in the same transaction, for the panel to query. Every row carries
-- agent_id; the store serializes one agent's writes with a transaction-scoped advisory lock. No foreign keys (repo
-- rule): workspace or agent teardown deletes by agent_id.
CREATE TABLE IF NOT EXISTS personal_agent (
    id                   UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         UUID        NOT NULL,
    user_id              UUID        NOT NULL,
    enabled              BOOLEAN     NOT NULL DEFAULT false,
    mode                 TEXT        NOT NULL DEFAULT 'observe' CHECK (mode IN ('off', 'observe', 'auto')),
    paused               BOOLEAN     NOT NULL DEFAULT false,
    router_runtime_id    UUID        NULL,
    worker_runtime_id    UUID        NULL,
    rules                JSONB       NULL,
    eval_timeout_seconds INTEGER     NOT NULL DEFAULT 120,
    settings_version     INTEGER     NOT NULL DEFAULT 1,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, user_id)
);

-- One receiving scope (group chat, channel, ...) of a Source: whether it receives every message or only mentions, and
-- its row of the admission table.
CREATE TABLE IF NOT EXISTS pa_source_scope (
    agent_id  UUID  NOT NULL,
    source    TEXT  NOT NULL,
    row_key   TEXT  NOT NULL,
    listen    TEXT  NOT NULL DEFAULT 'mention' CHECK (listen IN ('all', 'mention')),
    admission JSONB NOT NULL DEFAULT '{}'::jsonb,
    PRIMARY KEY (agent_id, source, row_key)
);

CREATE TABLE IF NOT EXISTS pa_allowed (
    agent_id UUID NOT NULL,
    source   TEXT NOT NULL,
    actor_id TEXT NOT NULL,
    name     TEXT NOT NULL DEFAULT '',
    kind     TEXT NOT NULL DEFAULT 'person' CHECK (kind IN ('person', 'bot')),
    PRIMARY KEY (agent_id, source, kind, actor_id)
);

CREATE TABLE IF NOT EXISTS pa_trigger (
    agent_id    UUID   NOT NULL,
    id          TEXT   NOT NULL,
    seq         BIGSERIAL,
    at          TEXT   NOT NULL,
    value       JSONB  NOT NULL,
    disposition JSONB  NULL,
    PRIMARY KEY (agent_id, id)
);
CREATE INDEX IF NOT EXISTS idx_pa_trigger_at ON pa_trigger (agent_id, at);

CREATE TABLE IF NOT EXISTS pa_trigger_thread (
    agent_id   UUID    NOT NULL,
    thread_key TEXT    NOT NULL,
    trigger_id TEXT    NOT NULL,
    at         TEXT    NOT NULL,
    human      BOOLEAN NOT NULL,
    PRIMARY KEY (agent_id, thread_key, trigger_id)
);
CREATE INDEX IF NOT EXISTS idx_pa_trigger_thread_at ON pa_trigger_thread (agent_id, thread_key, at);

CREATE TABLE IF NOT EXISTS pa_thread (
    agent_id UUID    NOT NULL,
    key      TEXT    NOT NULL,
    seq      BIGSERIAL,
    active   BOOLEAN NOT NULL,
    value    JSONB   NOT NULL,
    PRIMARY KEY (agent_id, key)
);
CREATE INDEX IF NOT EXISTS idx_pa_thread_active ON pa_thread (agent_id, seq) WHERE active;

-- The Eval a Thread has in progress (Thread.evaluating); a finished Eval leaves its Decision in pa_decision.
CREATE TABLE IF NOT EXISTS pa_eval (
    agent_id    UUID   NOT NULL,
    id          TEXT   NOT NULL,
    thread_key  TEXT   NOT NULL,
    strength    TEXT   NOT NULL,
    trigger_ids TEXT[] NOT NULL,
    started_at  TEXT   NOT NULL,
    deadline    TEXT   NOT NULL,
    PRIMARY KEY (agent_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pa_eval_thread ON pa_eval (agent_id, thread_key);

CREATE TABLE IF NOT EXISTS pa_decision (
    agent_id   UUID NOT NULL,
    id         TEXT NOT NULL,
    seq        BIGSERIAL,
    thread_key TEXT NOT NULL,
    kind       TEXT NOT NULL,
    ticket_id  TEXT NULL,
    value      JSONB NOT NULL,
    PRIMARY KEY (agent_id, id)
);
CREATE INDEX IF NOT EXISTS idx_pa_decision_thread ON pa_decision (agent_id, thread_key, seq);

CREATE TABLE IF NOT EXISTS pa_ticket (
    agent_id   UUID    NOT NULL,
    id         TEXT    NOT NULL,
    seq        BIGSERIAL,
    title      TEXT    NOT NULL,
    open       BOOLEAN NOT NULL,
    active     BOOLEAN NOT NULL,
    issue_id   UUID    NULL,
    created_at TEXT    NOT NULL,
    updated_at TEXT    NOT NULL,
    value      JSONB   NOT NULL,
    PRIMARY KEY (agent_id, id)
);
CREATE INDEX IF NOT EXISTS idx_pa_ticket_open ON pa_ticket (agent_id, seq) WHERE open;
CREATE INDEX IF NOT EXISTS idx_pa_ticket_active ON pa_ticket (agent_id, seq) WHERE active;

CREATE TABLE IF NOT EXISTS pa_subscription (
    agent_id   UUID    NOT NULL,
    thread_key TEXT    NOT NULL,
    ticket_id  TEXT    NOT NULL,
    context    BOOLEAN NOT NULL DEFAULT false,
    PRIMARY KEY (agent_id, thread_key, ticket_id)
);
CREATE INDEX IF NOT EXISTS idx_pa_subscription_ticket ON pa_subscription (agent_id, ticket_id);

CREATE TABLE IF NOT EXISTS pa_assignment (
    agent_id     UUID NOT NULL,
    id           TEXT NOT NULL,
    ticket_id    TEXT NOT NULL,
    state        TEXT NOT NULL,
    created_at   TEXT NOT NULL,
    started_at   TEXT NULL,
    ended_at     TEXT NULL,
    ended_reason TEXT NULL,
    task_id      UUID NULL,
    PRIMARY KEY (agent_id, id)
);
CREATE INDEX IF NOT EXISTS idx_pa_assignment_ticket ON pa_assignment (agent_id, ticket_id);

-- Every report an Assignment made, not only its latest.
CREATE TABLE IF NOT EXISTS pa_report (
    agent_id      UUID NOT NULL,
    assignment_id TEXT NOT NULL,
    at            TEXT NOT NULL,
    ticket_id     TEXT NOT NULL,
    state         TEXT NOT NULL,
    summary       TEXT NOT NULL,
    PRIMARY KEY (agent_id, assignment_id, at)
);

-- Messages queued for Channels: 'pending' goes out (auto mode), 'pending_confirm' waits for a person in the panel.
CREATE TABLE IF NOT EXISTS pa_outbox (
    agent_id   UUID        NOT NULL,
    id         TEXT        NOT NULL,
    seq        BIGSERIAL,
    ticket_id  TEXT        NULL,
    status     TEXT        NOT NULL CHECK (status IN ('pending', 'pending_confirm', 'sent', 'dropped')),
    value      JSONB       NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, id)
);
CREATE INDEX IF NOT EXISTS idx_pa_outbox_status ON pa_outbox (agent_id, status, seq);

-- A person's note on a Ticket in the panel; it enters the Engine as an explicit Trigger.
CREATE TABLE IF NOT EXISTS pa_annotation (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id   UUID        NOT NULL,
    ticket_id  TEXT        NOT NULL,
    author_id  UUID        NOT NULL,
    text       TEXT        NOT NULL,
    trigger_id TEXT        NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS idx_pa_annotation_ticket ON pa_annotation (agent_id, ticket_id, created_at);
