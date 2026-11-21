package main

// personal-agent: MUL-13574 (B-32): a Policy changed back to a version the Router was sent before reaches the
// Router again; only an unchanged Policy is not resent.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/testutil"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestPersonalAgentRouterGetsEachPolicyItIsChangedBackTo(t *testing.T) {
	fx := testutil.New(testPool, testWorkspaceID, testUserID)
	ctx := context.Background()
	stamp := time.Now().UnixNano()
	runtime := fx.Runtime(t, fmt.Sprintf("policy-resend-runtime-%d", stamp))
	router := fx.Agent(t, fmt.Sprintf("policy-resend-router-%d", stamp), runtime)
	session := fx.ChatSession(t, router)
	fx.Cleanup(t, `DELETE FROM agent_task_queue WHERE chat_session_id = $1`, session)
	fx.Cleanup(t, `DELETE FROM chat_submission_receipt WHERE chat_session_id = $1`, session)
	fx.Cleanup(t, `DELETE FROM chat_message WHERE chat_session_id = $1`, session)

	queries := db.New(testPool)
	bus := events.New()
	taskSvc := service.NewTaskService(queries, testPool, nil, bus)
	h := &handler.Handler{Queries: queries, TxStarter: testPool, Bus: bus, TaskService: taskSvc}
	on := policy.MustParseSettings(`{"policy":{"rules":[{"id":"status","name":"状态询问","enabled":true,"instruction":"问进度"}]}}`)
	off := policy.MustParseSettings(`{"policy":{"rules":[{"id":"status","name":"状态询问","enabled":false,"instruction":"问进度"}]}}`)
	current := on
	s := &personalAgentSession{h: h, pool: testPool, settingsOf: func(context.Context, string) (policy.Settings, error) { return current, nil }}
	agent := store.Agent{ID: fmt.Sprintf("policy-resend-%d", stamp), WorkspaceID: testWorkspaceID, UserID: testUserID,
		RouterAgentID: router, RouterSessionID: session}

	latestPolicy := func() string {
		var last string
		fx.QueryRow(t, `SELECT content FROM chat_message WHERE chat_session_id = $1 AND content LIKE '%## Policy%'
			ORDER BY created_at DESC LIMIT 1`, session).Scan(&last)
		return last
	}
	for i, settings := range []policy.Settings{on, off, on, off, on, on} {
		current = settings
		if err := s.Deliver(ctx, agent, fmt.Sprintf("card-%d", i), "a card"); err != nil {
			t.Fatalf("Deliver %d: %v", i, err)
		}
		enabled := strings.Contains(latestPolicy(), "状态询问 (status)")
		if want := settings.Policy.Rules[0].Enabled; enabled != want {
			t.Fatalf("after change %d the Router's latest Policy has the rule enabled=%v, want %v:\n%s", i, enabled, want, latestPolicy())
		}
	}
	if got := fx.Count(t, `SELECT count(*) FROM chat_message WHERE chat_session_id = $1 AND content LIKE '%## Policy%'`, session); got != 5 {
		t.Fatalf("%d Policies sent, want 5 (an unchanged Policy is not resent)", got)
	}
}
