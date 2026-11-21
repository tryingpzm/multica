-- personal-agent panel: an annotation is written on a Thread (thread_key) and, from a Ticket's page,
-- also names that Ticket; one written on a Thread with no Ticket has none.
ALTER TABLE pa_annotation ADD COLUMN IF NOT EXISTS thread_key TEXT NOT NULL DEFAULT '';
ALTER TABLE pa_annotation ALTER COLUMN ticket_id DROP NOT NULL;
