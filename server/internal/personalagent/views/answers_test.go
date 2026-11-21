package views_test

// Seam tests of the Execution list and the Ticket page as answers to the owner's questions: who asked,
// what for, who it went to, how it stands, and what the owner has to do.

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/host"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/views"
)

var issueID = uuid()

func names() views.Names {
	return views.Names{
		Issue: func(id string) *views.IssueLink {
			if id == issueID {
				return &views.IssueLink{ID: id, Identifier: "MUL-9", Title: "登录页回归", URL: "/acme/issues/MUL-9"}
			}
			return nil
		},
		Target: func(target core.Target) string {
			if target.ID == "agent-w" {
				return "前端小队员"
			}
			return ""
		},
	}
}

func seed(t *testing.T) *viewsAgent {
	a := open(t)
	a.Store.Atomic(func() {
		a.Store.Threads().Save(core.Thread{Key: "multica:" + issueID, Ref: core.ThreadRef{Source: "multica", ID: issueID}, CreatedAt: "2026-09-22T00:00:00Z",
			Subscriptions: []string{"k1"}, Pending: []string{}, Correlated: []string{}})
		a.Store.Decisions().Save(core.Decision{ID: "d1", ThreadKey: "multica:" + issueID, TriggerIDs: []string{"tr1"}, At: "2026-09-22T00:00:01Z",
			Kind: "accept", Reason: []core.Reason{{Code: "said", Values: map[string]string{"text": "Alice 点名要修登录页"}}}, Strength: "explicit", TicketID: "k1"})
		a.Store.Tickets().Save(core.Ticket{ID: "k1", Title: "修登录页", DecisionID: "d1", CreatedAt: "2026-09-22T00:00:01Z", UpdatedAt: "2026-09-22T00:00:05Z",
			Mandate:   core.Mandate{Strength: "explicit", Scope: "修复登录页回归", Actor: core.ActorRef{Source: "multica", ID: "u-alice"}},
			Execution: &core.Execution{Name: "fix-login", Assignment: "先复现再修", Target: core.Target{Type: "agent", ID: "agent-w"}},
			Assignments: []*core.Assignment{{ID: "a1", State: "running", CreatedAt: "2026-09-22T00:00:02Z", StartedAt: "2026-09-22T00:00:03Z",
				Report: &core.Report{State: "waiting", Summary: "已修复，等你验证\n细节见 PR", At: "2026-09-22T00:00:04Z"}}},
			Inputs: []*core.TicketInput{}, Undelivered: []string{}})
		a.Store.Tickets().Save(core.Ticket{ID: "k2", Title: "问个问题", CreatedAt: "2026-09-21T00:00:00Z", UpdatedAt: "2026-09-21T00:00:00Z",
			Attention: &core.Attention{Question: "用哪个方案？"}, Assignments: []*core.Assignment{}, Inputs: []*core.TicketInput{}, Undelivered: []string{}})
	})
	a.Store.SetTicketIssue("k1", issueID)
	return &viewsAgent{a}
}

func TestExecutionListPutsWhatWaitsOnTheOwnerFirstAndNamesWhoItWentTo(t *testing.T) {
	s := seed(t)
	items := view(s.Agent).WithNames(names()).Tickets("open", 0, 20).Items
	if len(items) != 2 || items[0].(map[string]any)["id"] != "k2" {
		t.Fatalf("order = %v, want the question k2 before the newer finished k1", items)
	}
	row := items[1].(map[string]any)
	if row["scope"] != "修复登录页回归" {
		t.Fatalf("scope = %v", row["scope"])
	}
	if e := row["executor"].(map[string]string); e["name"] != "前端小队员" || e["type"] != "agent" {
		t.Fatalf("executor = %v", e)
	}
	if issue := row["issue"].(*views.IssueLink); issue.Identifier != "MUL-9" {
		t.Fatalf("issue = %+v", issue)
	}
	if actor := row["actor"].(views.ActorView); actor.Source != "multica" {
		t.Fatalf("actor = %+v", actor)
	}
}

func TestTicketPageTellsTheStoryInOrderAndNamesItsThreadsByIssueTitle(t *testing.T) {
	s := seed(t)
	page := view(s.Agent).WithNames(names()).Ticket("k1", nil)
	if page["plan"] != "先复现再修" {
		t.Fatalf("plan = %v", page["plan"])
	}
	kinds := []string{}
	for _, item := range page["timeline"].([]any) {
		kinds = append(kinds, item.(map[string]any)["kind"].(string))
	}
	if got := fmtList(kinds); got != "decision,assign,report" {
		t.Fatalf("timeline = %s", got)
	}
	thread := page["threads"].([]any)[0].(map[string]any)
	if thread["title"] != "MUL-9 登录页回归" {
		t.Fatalf("thread title = %v, want the issue's title, not its key", thread["title"])
	}
}

type viewsAgent struct{ *host.Agent }

func fmtList(list []string) string { return strings.Join(list, ",") }
func TestWorkerTextNamesItsIdsAndCutsTheOnesNothingNames(t *testing.T) {
	s := seed(t)
	a := s.Agent
	a.Store.Atomic(func() {
		k := a.Store.Tickets().Get("k1")
		k.Assignments[0].Report.Summary = "已在 " + issueID + " 追加评论（comment id 01a1f255-fd38-781b-abda-caad467d0724）并 @ agent-w-x（11111111-2222-4333-8444-555555555555）"
		a.Store.Tickets().Save(*k)
	})
	row := view(a).WithNames(names()).Tickets("open", 0, 20).Items[1].(map[string]any)
	if got := row["summary"]; got != "已在 MUL-9 追加评论并 @ agent-w-x" {
		t.Fatalf("summary = %q", got)
	}
}
