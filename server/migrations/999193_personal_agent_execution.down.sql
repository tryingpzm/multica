DROP TABLE IF EXISTS pa_watchdog;
DROP TABLE IF EXISTS pa_effect;
DROP INDEX IF EXISTS idx_personal_agent_router_session;
DROP INDEX IF EXISTS idx_pa_assignment_task;
ALTER TABLE pa_assignment DROP COLUMN IF EXISTS task_started_at;
ALTER TABLE personal_agent DROP COLUMN IF EXISTS router_session_id;
ALTER TABLE personal_agent DROP COLUMN IF EXISTS worker_agent_id;
ALTER TABLE personal_agent DROP COLUMN IF EXISTS router_agent_id;
