package runtime_test

// Seam tests of the personal agent's execution, against the real store and effect queue, with the Multica run plane
// and the Router session — the system boundaries — faked: a run's end without a report becomes a waiting report that
// wakes the Router, and one Thread's Evals run one at a time, batching what arrives meanwhile.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/executor"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/plans"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/router"
	"github.com/multica-ai/multica/server/internal/personalagent/runtime"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/panel"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/personalagent/store/storetest"
)

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type message struct{ id, text string }

type session struct {
	mu      sync.Mutex
	seen    map[string]bool
	got     []message
	holding bool
}

// hold keeps posted messages unread, as a Router run still busy with an earlier message does.
func (s *session) hold(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holding = on
}

func (s *session) Received(_ context.Context, _ store.Agent, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen[id] && !s.holding, nil
}

func (s *session) Deliver(_ context.Context, _ store.Agent, id, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.seen[id] {
		s.seen[id] = true
		s.got = append(s.got, message{id, text})
	}
	return nil
}

func (s *session) kinds(kind string) []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []map[string]any{}
	for _, m := range s.got {
		var v map[string]any
		if json.Unmarshal([]byte(m.text), &v) == nil && v["kind"] == kind {
			out = append(out, v)
		}
	}
	return out
}

type runs struct {
	mu        sync.Mutex
	issues    []string
	workers   []string
	targets   []core.Target
	issueIDs  []string
	posts     []string
	cancelled []string
	n         int
}

func (r *runs) CreateIssue(_ context.Context, _ store.Agent, target core.Target, title, description string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.issues = append(r.issues, title)
	r.workers = append(r.workers, target.ID)
	r.targets = append(r.targets, target)
	r.n++
	r.posts = append(r.posts, description)
	issue := uuid()
	r.issueIDs = append(r.issueIDs, issue)
	return issue, fmt.Sprintf("00000000-0000-4000-8000-%012d", r.n), nil
}

func (r *runs) Run(_ context.Context, _ store.Agent, _, text string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	r.posts = append(r.posts, text)
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.n), nil
}

func (r *runs) SetStatus(context.Context, store.Agent, string, string) error { return nil }

func (r *runs) Cancel(_ context.Context, task string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelled = append(r.cancelled, task)
	return nil
}

type fixture struct {
	rt      *runtime.Runtime
	session *session
	runs    *runs
	agent   store.Agent
}

func setup(t *testing.T) *fixture {
	t.Helper()
	return setupWith(t, defaultWorker)
}

// setupWith is setup with the deployment's default worker agent, "" for none.
func setupWith(t *testing.T, worker string) *fixture {
	t.Helper()
	pool := storetest.Open(t)
	ctx := context.Background()
	f := &fixture{session: &session{seen: map[string]bool{}}, runs: &runs{}}
	f.agent = store.CreateAgent(store.New(ctx, pool, ""), uuid(), uuid())
	if _, err := pool.Exec(ctx, "UPDATE personal_agent SET enabled = true, router_agent_id = $2 WHERE id = $1", f.agent.ID, uuid()); err != nil {
		t.Fatal(err)
	}
	f.rt = runtime.New(runtime.Options{Pool: pool, Runs: f.runs, Session: f.session,
		DefaultWorker: func(context.Context, store.Agent) string { return worker }})
	f.rt.Effects.Start()
	t.Cleanup(f.rt.Effects.Drain)
	return f
}

func (f *fixture) open(t *testing.T) *host.Agent {
	t.Helper()
	a, err := f.rt.Host.Open(context.Background(), f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// settle runs passes and effects until nothing is left to do; a pass the Host requested on receipt may hold the
// agent meanwhile, so it keeps passing for a while.
func (f *fixture) settle() {
	for i := 0; i < 10; i++ {
		f.rt.Pass(context.Background(), false)
		f.rt.Effects.Idle("")
		time.Sleep(10 * time.Millisecond)
	}
}

const defaultWorker = "00000000-0000-4000-8000-00000000d0d0"

// rules saves the Policy's rules.
func (f *fixture) rules(t *testing.T, rules ...policy.Rule) {
	t.Helper()
	a := f.open(t)
	a.Store.Atomic(func() { a.Store.SavePolicy(a.Store.Revision(), rules, map[string]map[string]any{}) })
}

// accept routes an annotation as accepted under rules and settles its launch.
func (f *fixture) accept(t *testing.T, id string, rules ...string) {
	t.Helper()
	f.annotate(t, id, "wrk_1:"+id, "修一下登录页 "+id)
	f.settle()
	results := router.Route(f.open(t), []router.RouteItem{{Kind: "accept", TriggerID: "panel:" + id, Title: "登录页 " + id, WorktreeName: "fix-login",
		Acknowledgement: "收到。", Rules: rules}})
	if len(results) != 1 || results[0]["decision"] != core.DecisionKind("accept") {
		t.Fatalf("route %v", results)
	}
	f.settle()
}

func TestAnAcceptUnderARuleWithAnAgentGoesToThatAgent(t *testing.T) {
	f := setup(t)
	frontend := uuid()
	f.rules(t, policy.Rule{ID: "copy", Name: "文案", Enabled: true, Instruction: "改文案"},
		policy.Rule{ID: "visual", Name: "样式", Enabled: true, Instruction: "改样式", Target: policy.RuleTarget{Type: "agent", ID: frontend}})
	// The first matched rule that names an agent decides.
	f.accept(t, "n1", "copy", "visual")
	if len(f.runs.workers) != 1 || f.runs.workers[0] != frontend {
		t.Fatalf("workers %v, want %s", f.runs.workers, frontend)
	}
}

// MUL-13553: a rule's executor the owner may not invoke falls back to their executor.
func TestAnAcceptUnderARuleWhoseAgentTheOwnerMayNotInvokeGoesToTheDefaultWorker(t *testing.T) {
	f := setup(t)
	forbidden := uuid()
	f.rt.Executor.SetCanInvoke(func(_ context.Context, _ store.Agent, target string) bool { return target != forbidden })
	f.rules(t, policy.Rule{ID: "visual", Name: "样式", Enabled: true, Instruction: "改样式", Target: policy.RuleTarget{Type: "agent", ID: forbidden}})
	f.accept(t, "n1", "visual")
	if len(f.runs.workers) != 1 || f.runs.workers[0] != defaultWorker {
		t.Fatalf("workers %v, want the default %s", f.runs.workers, defaultWorker)
	}
}

func TestAnAcceptUnderNoRuleWithAnAgentGoesToTheDefaultWorker(t *testing.T) {
	f := setup(t)
	f.rules(t, policy.Rule{ID: "copy", Name: "文案", Enabled: true, Instruction: "改文案"})
	f.accept(t, "n1", "copy")
	f.accept(t, "n2")
	if len(f.runs.workers) != 2 || f.runs.workers[0] != defaultWorker || f.runs.workers[1] != defaultWorker {
		t.Fatalf("workers %v, want the default %s twice", f.runs.workers, defaultWorker)
	}
}

var at = time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)

func (f *fixture) annotate(t *testing.T, id, thread, text string) {
	t.Helper()
	ok, err := host.Annotate(f.open(t), panel.Annotation{ID: id, Thread: core.ThreadRef{Source: "github", ID: thread}, Text: text, At: at})
	if err != nil || !ok {
		t.Fatalf("annotation %s not taken: %v %v", id, ok, err)
	}
}

func TestARunThatEndsWithoutAReportIsReportedWaitingAndWakesTheRouter(t *testing.T) {
	f := setup(t)
	f.annotate(t, "n1", "wrk_1:m1", "把设置页的保存按钮改成“保存”")
	f.settle()
	if cards := f.session.kinds("candidate"); len(cards) != 1 || cards[0]["trigger_id"] != "panel:n1" || cards[0]["strength"] != "explicit" {
		t.Fatalf("candidates %v", cards)
	}
	results := router.Route(f.open(t), []router.RouteItem{{Kind: "accept", TriggerID: "panel:n1", Title: "保存按钮文案", WorktreeName: "save-label",
		Acknowledgement: "收到：把保存按钮文案改成“保存”。"}})
	if len(results) != 1 || results[0]["decision"] != core.DecisionKind("accept") {
		t.Fatalf("route %v", results)
	}
	f.settle()
	if len(f.runs.issues) != 1 || len(f.runs.posts) != 1 || !strings.Contains(f.runs.posts[0], "ticket read") || !strings.Contains(f.runs.posts[0], "ticket report") {
		t.Fatalf("issues %v posts %v", f.runs.issues, f.runs.posts)
	}
	ticketID := results[0]["ticket"].(map[string]any)["ticket_id"].(string)
	ticket := f.open(t).Store.Tickets().Get(ticketID)
	if a := core.RunningAssignment(ticket); a == nil {
		t.Fatalf("no running assignment: %+v", ticket.Assignments[0])
	}
	if err := f.rt.Executor.RunEnded(context.Background(), "00000000-0000-4000-8000-000000000001", "", "completed", "看了代码，按钮在 settings.vue。"); err != nil {
		t.Fatal(err)
	}
	f.settle()
	ticket = f.open(t).Store.Tickets().Get(ticketID)
	report := core.LatestReport(ticket)
	if report == nil || report.State != "waiting" || report.Summary != "看了代码，按钮在 settings.vue。" {
		t.Fatalf("report %+v", report)
	}
	relays := f.session.kinds("worker_report")
	if len(relays) != 1 || relays[0]["state"] != "waiting" || relays[0]["summary"] != "看了代码，按钮在 settings.vue。" || relays[0]["trigger_id"] != "panel:n1" {
		t.Fatalf("relays %v", relays)
	}
	// The same run's end again changes nothing.
	if err := f.rt.Executor.RunEnded(context.Background(), "00000000-0000-4000-8000-000000000001", "", "completed", "again"); err != nil {
		t.Fatal(err)
	}
	f.settle()
	if len(f.session.kinds("worker_report")) != 1 {
		t.Fatalf("relays %v", f.session.kinds("worker_report"))
	}
}

func TestARunThatReportedIsNotReportedAgainAtItsEnd(t *testing.T) {
	f := setup(t)
	f.annotate(t, "n1", "wrk_1:m1", "修一下登录页")
	f.settle()
	results := router.Route(f.open(t), []router.RouteItem{{Kind: "accept", TriggerID: "panel:n1", Title: "登录页", WorktreeName: "fix-login", Acknowledgement: "收到。"}})
	f.settle()
	ticketID := results[0]["ticket"].(map[string]any)["ticket_id"].(string)
	question := "用哪个颜色？"
	out, err := f.rt.Executor.Report(f.open(t), ticketID, executorReport("blocked", "需要决定颜色", &question))
	if err != nil || out["notification"] != "orchestrator" {
		t.Fatalf("report %v %v", out, err)
	}
	if err := f.rt.Executor.RunEnded(context.Background(), "00000000-0000-4000-8000-000000000001", "", "completed", "done"); err != nil {
		t.Fatal(err)
	}
	f.settle()
	if got := core.LatestReport(f.open(t).Store.Tickets().Get(ticketID)); got.State != "blocked" {
		t.Fatalf("report %+v", got)
	}
	if len(f.session.kinds("worker_attention")) != 1 || len(f.session.kinds("worker_report")) != 0 {
		t.Fatalf("session %v", f.session.got)
	}
}

func TestOneThreadsEvalsRunOneAtATimeAndBatchWhatArrivesMeanwhile(t *testing.T) {
	f := setup(t)
	f.annotate(t, "n1", "wrk_1:m1", "第一条")
	f.settle()
	f.annotate(t, "n2", "wrk_1:m1", "第二条")
	f.annotate(t, "n3", "wrk_1:m1", "第三条")
	f.settle()
	cards := f.session.kinds("candidate")
	if len(cards) != 1 || cards[0]["trigger_id"] != "panel:n1" {
		t.Fatalf("while the first Eval runs: %v", cards)
	}
	if got := router.Route(f.open(t), []router.RouteItem{{Kind: "ignore", TriggerID: "panel:n1", Reason: "只是讨论"}}); got[0]["refused"] != nil {
		t.Fatalf("route %v", got)
	}
	f.settle()
	cards = f.session.kinds("candidate")
	if len(cards) != 2 || cards[1]["trigger_id"] != "panel:n3" {
		t.Fatalf("after the first Eval: %v", cards)
	}
	batch, _ := cards[1]["batch"].([]any)
	if len(batch) != 1 || batch[0].(map[string]any)["trigger_id"] != "panel:n2" {
		t.Fatalf("batch %v", cards[1]["batch"])
	}
}

func TestTheWatchdogFlagsARunThatDoesNotEndOnceAndTellsTheRouter(t *testing.T) {
	f := setup(t)
	f.annotate(t, "n1", "wrk_1:m1", "修一下登录页")
	f.settle()
	results := router.Route(f.open(t), []router.RouteItem{{Kind: "accept", TriggerID: "panel:n1", Title: "登录页", WorktreeName: "fix-login", Acknowledgement: "收到。"}})
	f.settle()
	ticketID := results[0]["ticket"].(map[string]any)["ticket_id"].(string)
	if found := f.rt.Executor.Watchdog(f.open(t)); len(found) != 0 {
		t.Fatalf("a fresh run is not stuck: %v", found)
	}
	a := f.open(t)
	a.Store.SetAssignmentRun(core.RunningAssignment(a.Store.Tickets().Get(ticketID)).ID, "00000000-0000-4000-8000-000000000001", "2000-01-01T00:00:00.000Z")
	for i := 0; i < 2; i++ {
		if found := f.rt.Executor.Watchdog(f.open(t)); len(found) != 1 || found[0].Kind != "run_overdue" || found[0].TicketID != ticketID {
			t.Fatalf("found %v", found)
		}
	}
	f.settle()
	if got := f.session.kinds("watchdog"); len(got) != 1 || got[0]["stuck"] != "run_overdue" {
		t.Fatalf("watchdog relays %v", got)
	}
	if stuck := f.open(t).Store.StuckTickets(); len(stuck) != 1 {
		t.Fatalf("stuck %v", stuck)
	}
}

// expire puts the Eval of the only active Thread past its deadline, as time passing would.
func (f *fixture) expire(t *testing.T) core.Evaluation {
	t.Helper()
	a := f.open(t)
	var e core.Evaluation
	a.Store.Atomic(func() {
		for _, thread := range a.Store.Threads().Active() {
			if thread.Evaluating != nil {
				thread.Evaluating.Deadline = "2000-01-01T00:00:00.000Z"
				e = *thread.Evaluating
				a.Store.Threads().Save(thread)
			}
		}
	})
	if e.ID == "" {
		t.Fatal("no Eval runs")
	}
	return e
}

func (f *fixture) evaluating(t *testing.T) *core.Evaluation {
	t.Helper()
	for _, thread := range f.open(t).Store.Threads().Active() {
		if thread.Evaluating != nil {
			e := *thread.Evaluating
			return &e
		}
	}
	return nil
}

func TestAnEvalsDeadlineDoesNotRunWhileItsCardWaitsBehindAnotherRouterRun(t *testing.T) {
	f := setup(t)
	f.session.hold(true)
	f.annotate(t, "n1", "wrk_1:m1", "首页改版发布正式了吗")
	f.settle()
	if cards := f.session.kinds("candidate"); len(cards) != 1 {
		t.Fatalf("the card is posted: %v", cards)
	}
	f.expire(t)
	f.settle()
	e := f.evaluating(t)
	if e == nil || e.Delivered != "" {
		t.Fatalf("a card no Router run has started on is still assessed, untimed: %+v", e)
	}
	f.session.hold(false)
	f.settle()
	e = f.evaluating(t)
	if e == nil || e.Delivered == "" || e.Deadline <= core.FormatTime(time.Now().UnixMilli()) {
		t.Fatalf("once a Router run starts on the card its deadline runs from then: %+v", e)
	}
}

func TestATimedOutTriggerRequeuesAndIsAssessedAgain(t *testing.T) {
	f := setup(t)
	f.annotate(t, "n1", "wrk_1:m1", "首页改版发布正式了吗")
	f.settle()
	for i := 0; i < 3; i++ {
		f.expire(t)
		f.settle()
	}
	a := f.open(t)
	if e := f.evaluating(t); e != nil {
		t.Fatalf("the Eval ended: %+v", e)
	}
	queued, err := a.Engine.Reevaluate("panel:n1")
	if err != nil || !queued {
		t.Fatalf("requeue %v %v", queued, err)
	}
	f.settle()
	if cards := f.session.kinds("candidate"); len(cards) < 2 || cards[len(cards)-1]["trigger_id"] != "panel:n1" {
		t.Fatalf("the requeued Trigger reaches the Router again: %v", cards)
	}
	got := router.Route(f.open(t), []router.RouteItem{{Kind: "ignore", TriggerID: "panel:n1", Reason: "状态询问"}})
	if got[0]["refused"] != nil || got[0]["decision"] != core.DecisionKind("ignore") {
		t.Fatalf("the new assessment decides: %v", got)
	}
}

// MUL-13553: a report's plan for an issue that needs the owner lands in their pending plans, once per issue status.
func TestAReportsPlanIsPendingForTheOwnerOnce(t *testing.T) {
	f := setup(t)
	f.accept(t, "n1")
	a := f.open(t)
	var ticketID string
	for _, tk := range a.Store.Tickets().List() {
		ticketID = tk.ID
	}
	plan := &executor.PlanArgs{IssueID: "MUL-551", IssueStatus: "in_review", Conclusion: "可以合并", Reason: "review 通过",
		Actions: []plans.Action{{Type: "merge_mr", Target: "repo!1"}}}
	for i := 0; i < 2; i++ {
		if _, err := f.rt.Executor.Report(f.open(t), ticketID, executor.ReportArgs{Kind: "blocked", Summary: "需要你决定是否合并", Plan: plan}); err != nil {
			t.Fatal(err)
		}
	}
	got := f.rt.Plans.Pending(context.Background(), f.agent.ID)
	if len(got) != 1 || got[0].Conclusion != "可以合并" || got[0].Actions[0].Type != "merge_mr" {
		t.Fatalf("pending %+v", got)
	}
}

// MUL-13553 path 1: a Worker that hands the work to another issue keeps its ticket following that issue; when the
// issue then needs the owner (blocked / in_review) the Worker is asked for a plan on it, once per status.
func TestATicketFollowsTheIssueItsWorkWasHandedToAndIsAskedForAPlanWhenItNeedsTheOwner(t *testing.T) {
	f := setup(t)
	f.accept(t, "n1")
	var ticketID string
	for _, tk := range f.open(t).Store.Tickets().List() {
		ticketID = tk.ID
	}
	if _, err := f.rt.Executor.Report(f.open(t), ticketID, executor.ReportArgs{Kind: "waiting", Summary: "已转给 MUL-900", Follow: "MUL-900"}); err != nil {
		t.Fatal(err)
	}
	before := len(f.runs.posts)
	ctx := context.Background()
	f.rt.Executor.IssueChanged(ctx, "in_progress", "uuid-900", "MUL-900")
	if len(f.runs.posts) != before {
		t.Fatalf("asked for a plan while the issue is in progress: %v", f.runs.posts[before:])
	}
	f.rt.Executor.IssueChanged(ctx, "in_review", "uuid-900", "MUL-900")
	f.rt.Executor.IssueChanged(ctx, "in_review", "uuid-900", "MUL-900")
	if got := f.runs.posts[before:]; len(got) != 1 || !strings.Contains(got[0], "MUL-900") || !strings.Contains(got[0], "--plan") {
		t.Fatalf("posts %v, want one plan request for MUL-900", got)
	}
}
