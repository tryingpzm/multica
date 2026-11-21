DELETE FROM pa_outbox WHERE status = 'discarded';
ALTER TABLE pa_outbox DROP CONSTRAINT IF EXISTS pa_outbox_status_check;
ALTER TABLE pa_outbox ADD CONSTRAINT pa_outbox_status_check CHECK (status IN ('pending', 'pending_confirm', 'sent', 'dropped'));
