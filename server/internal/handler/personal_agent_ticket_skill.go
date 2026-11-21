package handler

// personal-agent: a run on a personal agent ticket's issue, whichever agent or squad member runs it,
// is given the personal-agent-ticket skill, so the ticket's issue carries a short card instead of the instructions.

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/personalagent/ticketskill"
	"github.com/multica-ai/multica/server/internal/service"
)

// personalAgentTicketSkill is the ticket skill when issueID is a personal agent ticket's issue.
func (h *Handler) personalAgentTicketSkill(ctx context.Context, issueID pgtype.UUID) (service.AgentSkillData, bool) {
	if !issueID.Valid || h.DB == nil {
		return service.AgentSkillData{}, false
	}
	var found bool
	if err := h.DB.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pa_ticket WHERE issue_id = $1)", issueID).Scan(&found); err != nil || !found {
		return service.AgentSkillData{}, false
	}
	return TicketSkill(), true
}

// TicketSkill is the personal-agent-ticket skill as a built-in.
func TicketSkill() service.AgentSkillData {
	return service.AgentSkillData{Name: ticketskill.Name, Description: ticketskill.Description(), Content: ticketskill.Content()}
}
