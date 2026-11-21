package main

// personal-agent: the deployment's default agents. An owner's unset Router is PERSONAL_AGENT_ROUTER_AGENT_ID, and a
// ticket no rule names an agent for goes to PERSONAL_AGENT_WORKER_AGENT_ID; either may be left unset and chosen in
// the panel instead.

import (
	"context"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
)

func personalAgentRouter(h *handler.Handler) func(ctx context.Context, workspaceID, userID string) string {
	return func(ctx context.Context, workspaceID, _ string) string {
		return configuredAgent(ctx, h, workspaceID, "PERSONAL_AGENT_ROUTER_AGENT_ID")
	}
}

func personalAgentDefaultWorker(h *handler.Handler) func(ctx context.Context, agent store.Agent) string {
	return func(ctx context.Context, a store.Agent) string {
		return configuredAgent(ctx, h, a.WorkspaceID, "PERSONAL_AGENT_WORKER_AGENT_ID")
	}
}

// configuredAgent is the agent the env names, when it is an agent of the workspace; "" otherwise.
func configuredAgent(ctx context.Context, h *handler.Handler, workspaceID, env string) string {
	id := strings.TrimSpace(os.Getenv(env))
	if _, err := uuid.Parse(id); err != nil || !workspaceAgent(ctx, h, workspaceID, id) {
		return ""
	}
	return id
}

// personalAgentCanInvoke reports whether the owner may invoke a rule's agent in their workspace.
func personalAgentCanInvoke(h *handler.Handler) func(ctx context.Context, a store.Agent, target string) bool {
	return func(ctx context.Context, a store.Agent, target string) bool {
		if _, err := uuid.Parse(target); err != nil {
			return false
		}
		ag, err := h.Queries.GetAgent(ctx, parseUUID(target))
		return err == nil && uuid.UUID(ag.WorkspaceID.Bytes).String() == a.WorkspaceID && h.CanMemberInvokeAgent(ctx, ag, a.UserID, a.WorkspaceID)
	}
}

func personalAgentName(h *handler.Handler) func(ctx context.Context, id string) string {
	return func(ctx context.Context, id string) string {
		if _, err := uuid.Parse(id); err != nil {
			return ""
		}
		ag, err := h.Queries.GetAgent(ctx, parseUUID(id))
		if err != nil {
			return ""
		}
		return ag.Name
	}
}
