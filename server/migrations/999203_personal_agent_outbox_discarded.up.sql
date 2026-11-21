-- personal-agent a withdrawn reply keeps its row as discarded instead of being deleted.
ALTER TABLE pa_outbox DROP CONSTRAINT IF EXISTS pa_outbox_status_check;
ALTER TABLE pa_outbox ADD CONSTRAINT pa_outbox_status_check CHECK (status IN ('pending', 'pending_confirm', 'sent', 'dropped', 'discarded'));
