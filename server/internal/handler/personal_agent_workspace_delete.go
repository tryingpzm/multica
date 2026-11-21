package handler

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// personalAgentTables are the tables keyed by a personal agent's id, deleted before the agents themselves.
var personalAgentTables = []string{"pa_allowed", "pa_annotation", "pa_assignment", "pa_decision", "pa_effect", "pa_eval", "pa_follow",
	"pa_outbox", "pa_plan", "pa_report", "pa_source_scope", "pa_subscription", "pa_thread", "pa_ticket", "pa_trigger", "pa_trigger_thread",
	"pa_watchdog"}

// deleteWorkspacePersonalAgents deletes the workspace's personal agents and their rows. The pa_* tables have no
// foreign keys (some key the agent as text), so the delete compares as text.
func deleteWorkspacePersonalAgents(ctx context.Context, tx pgx.Tx, workspaceID pgtype.UUID) error {
	for _, table := range personalAgentTables {
		if _, err := tx.Exec(ctx, "DELETE FROM "+table+" WHERE agent_id::text IN (SELECT id::text FROM personal_agent WHERE workspace_id = $1)", workspaceID); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, "DELETE FROM personal_agent WHERE workspace_id = $1", workspaceID)
	return err
}
