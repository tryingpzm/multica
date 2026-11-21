package handler

// personal-agent lets the personal agent check a default Router/Worker against its owner.

import (
	"context"

	"github.com/multica-ai/multica/server/pkg/db/generated"
)

// CanMemberInvokeAgent reports whether member userID may invoke agent in workspaceID.
func (h *Handler) CanMemberInvokeAgent(ctx context.Context, agent db.Agent, userID, workspaceID string) bool {
	return h.canInvokeAgent(ctx, agent, "member", userID, userID, workspaceID)
}
