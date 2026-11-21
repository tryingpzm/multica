package main

// personal-agent plan actions carried out as the plan's owner: each goes through the same HTTP handler
// a member's own request does, with their identity, so every permission gate (issue access) applies to it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/go-chi/chi/v5"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/personalagent/plans"
)

type personalAgentActor struct {
	h         *handler.Handler
	workspace func(ctx context.Context, agentID string) string
}

func (x personalAgentActor) Do(ctx context.Context, p plans.Plan, a plans.Action) (string, error) {
	issue := p.IssueID
	switch a.Type {
	case "comment":
		if a.Target != "" {
			issue = a.Target
		}
		return x.call(ctx, p, x.h.CreateComment, map[string]string{"id": issue}, map[string]any{"content": a.Body})
	case "clarify_answer":
		return x.call(ctx, p, x.h.CreateComment, map[string]string{"id": issue}, map[string]any{"content": a.Body, "parent_id": a.Target})
	case "status":
		if a.Target != "" {
			issue = a.Target
		}
		return x.call(ctx, p, x.h.UpdateIssue, map[string]string{"id": issue}, map[string]any{"status": a.Body})
	}
	return "", fmt.Errorf("action %s is not supported here", a.Type)
}

func (x personalAgentActor) call(ctx context.Context, p plans.Plan, fn http.HandlerFunc, params map[string]string, body any) (string, error) {
	data, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(data)).WithContext(ctx)
	rc := chi.NewRouteContext()
	for k, v := range params {
		rc.URLParams.Add(k, v)
	}
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rc))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", p.OwnerID)
	req.Header.Set("X-Workspace-ID", x.workspace(ctx, p.AgentID))
	w := httptest.NewRecorder()
	fn(w, req)
	if w.Code >= 300 {
		return "", errors.New(w.Body.String())
	}
	return fmt.Sprint(w.Code), nil
}
