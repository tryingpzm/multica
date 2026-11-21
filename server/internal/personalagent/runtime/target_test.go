package runtime_test

// Seam tests of MUL-13499: a rule's executor may be a squad, whose members' runs count as the ticket's; a ticket with
// no executor and no deployment default waits for its owner; a 修正补充 ticket hands its Worker the procedure.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/executor"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/router"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/views"
)

func ticketOf(t *testing.T, f *fixture) *core.Ticket {
	t.Helper()
	open := f.open(t).Store.Tickets().Open()
	if len(open) != 1 {
		t.Fatalf("open tickets %d", len(open))
	}
	return &open[0]
}

func TestARuleTargetingASquadDispatchesToItAndAMembersRunEndIsReported(t *testing.T) {
	f := setup(t)
	squad := uuid()
	f.rules(t, policy.Rule{ID: "bug", Name: "测试反馈缺陷", Enabled: true, Instruction: "建缺陷单", Target: policy.RuleTarget{Type: "squad", ID: squad}})
	f.accept(t, "n1", "bug")
	if len(f.runs.targets) != 1 || f.runs.targets[0] != (core.Target{Type: "squad", ID: squad}) {
		t.Fatalf("targets %v", f.runs.targets)
	}
	// A squad member's run on the ticket's issue — not the leader's run the launch recorded — ends.
	member := uuid()
	if err := f.rt.Executor.RunEnded(context.Background(), member, f.runs.issueIDs[0], executor.Completed, "缺陷单已建：1001"); err != nil {
		t.Fatal(err)
	}
	f.settle()
	report := core.LatestReport(ticketOf(t, f))
	if report == nil || report.State != "waiting" || report.Summary != "缺陷单已建：1001" {
		t.Fatalf("report %+v", report)
	}
	if relays := f.session.kinds("worker_report"); len(relays) != 1 {
		t.Fatalf("relays %v", relays)
	}
}

func TestAnotherAgentsRunOnAnAgentTicketsIssueIsNotTheTicketsRun(t *testing.T) {
	f := setup(t)
	f.rules(t, policy.Rule{ID: "copy", Name: "文案", Enabled: true, Instruction: "改文案", Target: policy.RuleTarget{Type: "agent", ID: uuid()}})
	f.accept(t, "n1", "copy")
	if err := f.rt.Executor.RunEnded(context.Background(), uuid(), f.runs.issueIDs[0], executor.Completed, "别人的"); err != nil {
		t.Fatal(err)
	}
	f.settle()
	if report := core.LatestReport(ticketOf(t, f)); report != nil {
		t.Fatalf("report %+v", report)
	}
}

func TestATicketWithNoExecutorAndNoDefaultWaitsForTheOwner(t *testing.T) {
	f := setupWith(t, "")
	f.rules(t, policy.Rule{ID: "fix", Name: "修正补充", Enabled: true, Instruction: "补充到原单"})
	f.accept(t, "n1", "fix")
	if len(f.runs.issues) != 0 {
		t.Fatalf("created %v", f.runs.issues)
	}
	ticket := ticketOf(t, f)
	if stage := core.StageOf(ticket); stage.Stage != "person" {
		t.Fatalf("stage %+v", stage)
	}
	if report := core.LatestReport(ticket); report.Summary != "这条规则还没有指定执行方，请选择智能体或小队" {
		t.Fatalf("report %+v", report)
	}
	problems := views.New(f.open(t), sources.ChannelLinks{}, time.Now()).WithDefaultWorker(false).Overview()["problems"].([]any)
	found := false
	for _, p := range problems {
		m := p.(map[string]any)
		if m["kind"] == "rule-no-target" && m["rule"].(map[string]string)["id"] == "fix" {
			found = true
		}
	}
	if !found {
		t.Fatalf("problems %v", problems)
	}
	problems = views.New(f.open(t), sources.ChannelLinks{}, time.Now()).WithDefaultWorker(true).Overview()["problems"].([]any)
	for _, p := range problems {
		if p.(map[string]any)["kind"] == "rule-no-target" {
			t.Fatalf("with a default worker: %v", problems)
		}
	}
}
func TestACorrectionTicketHandsItsWorkerTheProcedureOfAppendingToTheInProgressIssue(t *testing.T) {
	f := setup(t)
	procedure := "1. 找关联的进行中的单 2. 找到：在那张 issue 上追加评论并 @ 那张单的执行 agent，不另开单 3. 找不到：在需求系统建技术需求"
	f.rules(t, policy.Rule{ID: "fix", Name: "修正补充", Enabled: true, Instruction: procedure, Target: policy.RuleTarget{Type: "agent", ID: uuid()}})
	f.annotate(t, "n1", "wrk_1:n1", "MUL-549 的保存按钮文案改成「保存设置」")
	f.settle()
	router.Route(f.open(t), []router.RouteItem{{Kind: "accept", TriggerID: "panel:n1", Title: "保存按钮文案", WorktreeName: "save-label",
		Acknowledgement: "收到。", Rules: []string{"fix"}}})
	f.settle()
	if len(f.runs.posts) != 1 {
		t.Fatalf("posts %v", f.runs.posts)
	}
	// MUL-13594: the procedure is in multica ticket read; the issue card only names the rule.
	read, err := f.rt.Executor.Read(f.open(t), f.open(t).Store.Tickets().List()[0].ID)
	if err != nil || !strings.Contains(read["assignment"].(string), "- 修正补充 (fix): "+procedure) || !strings.Contains(f.runs.posts[0], "修正补充") {
		t.Fatalf("read %v %v posts %v", read, err, f.runs.posts)
	}
}
