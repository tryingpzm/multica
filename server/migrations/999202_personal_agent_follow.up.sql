-- personal-agent path 1: an issue a ticket's Worker handed its work to; the ticket follows it and is
-- asked for a plan once per status that needs the owner.
CREATE TABLE IF NOT EXISTS pa_follow (
    agent_id TEXT NOT NULL,
    ticket_id TEXT NOT NULL,
    issue_ref TEXT NOT NULL,
    asked_status TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (agent_id, ticket_id, issue_ref)
);
