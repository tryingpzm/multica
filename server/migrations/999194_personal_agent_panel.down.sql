DELETE FROM pa_annotation WHERE ticket_id IS NULL;
ALTER TABLE pa_annotation ALTER COLUMN ticket_id SET NOT NULL;
ALTER TABLE pa_annotation DROP COLUMN IF EXISTS thread_key;
