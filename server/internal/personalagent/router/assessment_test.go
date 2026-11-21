package router_test

// Translated from dsh-superagent-prototype tests/assessment.spec.ts. The Engine runs on the real store; the Policy
// gate and each Source's thread reader are faked, as SA's fixture fakes them. Repository file reads are not a personal
// agent's, so the cases read messages instead.

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/effects"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/prompts"
	"github.com/multica-ai/multica/server/internal/personalagent/router"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/personalagent/store/storetest"
)

type gate struct{ strength core.Strength }

func (g gate) Version() string { return "v1" }
func (g gate) Paused() bool    { return false }
func (g gate) Gate(core.Trigger, core.Thread, []core.Ticket) core.GateResult {
	return core.GateResult{Admissible: true, Strength: g.strength, Basis: core.MandateBasis{Via: "followup"}}
}
func (g gate) Revalidate(core.Mandate) bool       { return true }
func (g gate) AdmitsInferred(rules []string) bool { return len(rules) > 0 }
func (g gate) EvalTimeoutMs() int64               { return 600_000 }

type everything struct{}

func (everything) Scope(string, map[string]any) core.ScopeResult {
	return core.ScopeResult{Listened: true, Observed: true}
}

type idle struct{}

func (idle) Start(core.Ticket, core.Assignment)        {}
func (idle) Deliver(core.Ticket, core.Assignment)      {}
func (idle) Stop(core.Ticket, core.Assignment, string) {}

type fixture struct {
	agent  *host.Agent
	router *router.Router
	store  *store.Store
}

// setup is SA's fixture: the github Source's thread reader returns an Issue and its comments, and a Source named
// probe gives no thread context; reader replaces it when set.
func setup(t *testing.T, strength core.Strength, bodies []string, reader func(core.Trigger) *sources.TriggerContext, hold chan struct{}) *fixture {
	t.Helper()
	if bodies == nil {
		for i := 0; i < 5; i++ {
			bodies = append(bodies, fmt.Sprintf("comment %d", i))
		}
	}
	pool := storetest.Open(t)
	row := store.CreateAgent(store.New(context.Background(), pool, ""), "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	s := store.New(context.Background(), pool, row.ID)
	if reader == nil {
		reader = func(trigger core.Trigger) *sources.TriggerContext {
			if trigger.Source != "github" {
				return nil
			}
			number := trigger.Threads[0].ID
			issue := "https://github.com/acme/widgets/issues/" + number
			id := issue
			if trigger.URL != "" {
				id = trigger.URL
			}
			return &sources.TriggerContext{MessageID: id, Read: func(ctx context.Context, part, cursor string) (sources.ContextPart, error) {
				if hold != nil {
					<-hold
				}
				messages := []sources.ContextMessage{{ID: issue, From: "bob", At: "2026-09-09T00:00:00Z", Text: "body"}}
				for i, body := range bodies {
					messages = append(messages, sources.ContextMessage{ID: fmt.Sprintf("%s#c%d", issue, i), From: "bob", At: "2026-09-09T00:00:00Z", Text: body})
				}
				return sources.ContextPart{Messages: messages, Body: map[string]any{"messages": messages}}, nil
			}}
		}
	}
	eff := effects.New(pool, nil)
	r := router.New(router.Options{Effects: eff, ThreadContext: func(_ *host.Agent, t core.Trigger) *sources.TriggerContext { return reader(t) }})
	settings := policy.MustParseSettings(`{}`)
	engine := core.NewEngine(core.EngineOptions{Repo: s, Policy: func() core.Policy { return gate{strength} }, Scope: everything{},
		Assessor: r.For(row, s), Executor: idle{}, Outbox: store.NewOutbox(s, func() bool { return false })})
	a := &host.Agent{Agent: row, Store: s, Settings: settings, Registry: sources.NewRegistry(func() policy.Settings { return settings }, nil), Engine: engine}
	return &fixture{agent: a, router: r, store: s}
}

func issue(n int) core.Trigger {
	return core.Trigger{ID: fmt.Sprintf("issue-%d", n), Source: "github", Threads: []core.ThreadRef{{Source: "github", ID: fmt.Sprint(n)}},
		Actor: core.ActorRef{Source: "github", ID: "bob"}, Human: true, At: time.Date(2026, 9, 9, 0, 0, n, 0, time.UTC).Format("2006-01-02T15:04:05.000Z"),
		Text: fmt.Sprintf("issue %d", n), URL: fmt.Sprintf("https://github.com/acme/widgets/issues/%d", n), Meta: map[string]any{"number": n}}
}

func (f *fixture) receive(t *testing.T, triggers ...core.Trigger) {
	t.Helper()
	for _, tr := range triggers {
		if _, err := f.agent.Engine.Receive(tr); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.agent.Engine.EvaluateDue(); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) ignore(t *testing.T, id string) {
	t.Helper()
	if _, err := f.agent.Engine.Decide(id, core.Outcome{Kind: "ignore", Said: "not a request"}); err != nil {
		t.Fatal(err)
	}
}

func ctx() context.Context { return context.Background() }

func TestKeepsNothingForAnEvalOnceItIsDecidedAsForOneThatTimedOut(t *testing.T) {
	f := setup(t, "inferred", nil, nil, nil)
	for n := 1; n <= 3; n++ {
		f.receive(t, issue(n))
		if _, err := f.router.Context(ctx(), f.agent, fmt.Sprintf("issue-%d", n)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.router.ReadContext(ctx(), f.agent, router.Read{TriggerID: fmt.Sprintf("issue-%d", n), Resource: "thread"}); err != nil {
			t.Fatal(err)
		}
		if f.router.Kept() == 0 {
			t.Fatal("an Eval in progress keeps its reads")
		}
		f.ignore(t, fmt.Sprintf("issue-%d", n))
		if f.router.Kept() != 0 {
			t.Fatalf("kept %d after the decision", f.router.Kept())
		}
	}
	f.receive(t, issue(9))
	if _, err := f.router.Context(ctx(), f.agent, "issue-9"); err != nil {
		t.Fatal(err)
	}
	thread := f.store.Threads().Get("github:9")
	thread.Expired = 2
	thread.Evaluating.Delivered = "2000-01-01T00:00:00.000Z"
	thread.Evaluating.Deadline = "2000-01-01T00:00:00.000Z"
	f.store.Atomic(func() { f.store.Threads().Save(*thread) })
	if err := f.agent.Engine.EvaluateDue(); err != nil {
		t.Fatal(err)
	}
	if f.router.Kept() != 0 {
		t.Fatalf("kept %d after the timeout", f.router.Kept())
	}
}

func TestKeepsNothingFromAContextReadThatFinishesAfterItsEvalWasDecided(t *testing.T) {
	hold := make(chan struct{})
	f := setup(t, "inferred", nil, nil, hold)
	f.receive(t, issue(1))
	done := make(chan error, 1)
	go func() { _, err := f.router.Context(ctx(), f.agent, "issue-1"); done <- err }()
	time.Sleep(50 * time.Millisecond)
	f.ignore(t, "issue-1")
	close(hold)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "This assessment has finished") {
		t.Fatalf("got %v", err)
	}
	if f.router.Kept() != 0 {
		t.Fatalf("kept %d", f.router.Kept())
	}
}

func TestReadsTheContextOfATriggerThroughItsSourceAndNoneForASourceThatGivesNone(t *testing.T) {
	f := setup(t, "inferred", nil, nil, nil)
	f.receive(t, issue(1))
	c, err := f.router.Context(ctx(), f.agent, "issue-1")
	if err != nil {
		t.Fatal(err)
	}
	thread := c["thread"].(map[string]any)
	if thread["complete"] != true || thread["loaded_messages"] != 6 || thread["source_found"] != true {
		t.Fatalf("thread %v", thread)
	}
	page, err := f.router.ReadContext(ctx(), f.agent, router.Read{TriggerID: "issue-1", Resource: "thread"})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(page["text"].(string)), &body); err != nil {
		t.Fatal(err)
	}
	if body["next_read"] != nil || !strings.Contains(page["text"].(string), `"text":"comment 0"`) {
		t.Fatalf("page %v", body)
	}
	other := issue(2)
	other.ID, other.Source = "other-2", "probe"
	other.Threads = []core.ThreadRef{{Source: "probe", ID: "2"}}
	other.Actor = core.ActorRef{Source: "probe", ID: "bob"}
	f.receive(t, other)
	c, err = f.router.Context(ctx(), f.agent, "other-2")
	if err != nil || !strings.Contains(fmt.Sprint(c["thread"].(map[string]any)["error"]), "gives no Thread context") {
		t.Fatalf("context %v %v", c, err)
	}
	if _, err := f.router.ReadContext(ctx(), f.agent, router.Read{TriggerID: "other-2", Resource: "thread"}); err == nil || !strings.Contains(err.Error(), "gives no Thread context") {
		t.Fatalf("got %v", err)
	}
	message, err := f.router.ReadContext(ctx(), f.agent, router.Read{TriggerID: "other-2", Resource: "message"})
	if err != nil || message["text"] != "issue 2" {
		t.Fatalf("message %v %v", message, err)
	}
}

func TestRoutesAnExplicitCandidateInsteadOfAssessingItEachEvalCommandNamesTheRoutingCommand(t *testing.T) {
	f := setup(t, "explicit", nil, nil, nil)
	f.receive(t, issue(1))
	routed := "multica ticket route with its trigger_id and kind accept, attach, stop or ignore"
	if _, err := f.router.Context(ctx(), f.agent, "issue-1"); err == nil || !strings.Contains(err.Error(), routed) {
		t.Fatalf("eval: %v", err)
	}
	if _, err := f.router.ReadContext(ctx(), f.agent, router.Read{TriggerID: "issue-1", Resource: "message"}); err == nil || !strings.Contains(err.Error(), routed) {
		t.Fatalf("eval-read: %v", err)
	}
	direction := map[string]any{"implementation_requested": false, "target_basis": "undetermined", "requires_design_decision": false, "evidence": "chatter"}
	if _, err := f.router.Decide(f.agent, "issue-1", policy.Assessment{Decision: "ignore", RuleAssessments: []policy.RuleAssessment{}, Direction: direction,
		Title: "t", Scope: "s", Reason: "r"}); err == nil || !strings.Contains(err.Error(), routed) {
		t.Fatalf("decide: %v", err)
	}
}

func TestAsksTheAssessmentOnlyForDecisionsItsCommandAccepts(t *testing.T) {
	if regexp.MustCompile(`reopen|decision=stop`).MatchString(prompts.Admission) {
		t.Fatal("admission asks for reopen or stop")
	}
}

func TestLoadsAWindowWithoutTheAssessmentInstructions(t *testing.T) {
	var bodies []string
	for i := 0; i < 30; i++ {
		repeat := 3
		if i%4 == 0 {
			repeat = 40
		}
		bodies = append(bodies, fmt.Sprintf("comment %d: %s", i, strings.Repeat("the label reads oddly on the settings page. ", repeat)))
	}
	f := setup(t, "inferred", bodies, nil, nil)
	trigger := issue(1)
	trigger.ID, trigger.Text, trigger.URL = "c15", bodies[15], "https://github.com/acme/widgets/issues/1#c15"
	f.receive(t, trigger)
	result, err := f.router.Context(ctx(), f.agent, "c15")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result["instructions"]; ok || result["text"] != bodies[15] {
		t.Fatalf("result %v", result)
	}
	messages := map[string]map[string]any{}
	for _, m := range result["thread"].(map[string]any)["messages"].([]map[string]any) {
		messages[m["id"].(string)] = m
	}
	at := func(i int) map[string]any {
		return messages[fmt.Sprintf("https://github.com/acme/widgets/issues/1#c%d", i)]
	}
	if m := at(15); len(m) != 4 || m["trigger"] != true || m["from"] != "bob" {
		t.Fatalf("trigger %v", m)
	}
	cutAt := func(s string, n int) string { return string([]rune(s)[:n]) + "…" }
	if m := at(16); m["chars"] != len(bodies[16]) || m["text"] != cutAt(bodies[16], 1200) {
		t.Fatalf("near %v", m)
	}
	if m := at(12); m["chars"] != len(bodies[12]) || m["text"] != cutAt(bodies[12], 400) {
		t.Fatalf("window %v", m)
	}
	if m := at(14); m["text"] != bodies[14] || m["chars"] != nil {
		t.Fatalf("short %v", m)
	}
	if m := at(8); len(m) != 3 || m["preview"] != bodies[8][:50] {
		t.Fatalf("stub %v", m)
	}
	whole, err := f.router.ReadContext(ctx(), f.agent, router.Read{TriggerID: "c15", Resource: "message", MessageID: at(8)["id"].(string)})
	if err != nil || !strings.Contains(whole["text"].(string), bodies[8][len(bodies[8])-40:]) {
		t.Fatalf("whole %v %v", whole, err)
	}
	data, _ := json.Marshal(result)
	if len(data) >= 9000 {
		t.Fatalf("window of %d bytes", len(data))
	}
}

func TestLetsTheRouterReadOnFromTheCursorItsWindowEndsWithAndPointsACutBatchMessageAtItsText(t *testing.T) {
	page := func(cursor string) sources.ContextPart {
		n := 0
		fmt.Sscan(cursor, &n)
		m := sources.ContextMessage{ID: fmt.Sprintf("m%d", n), From: "bob", At: time.Date(2026, 9, 9, 0, 0, n, 0, time.UTC).Format(time.RFC3339), Text: fmt.Sprintf("message %d", n)}
		return sources.ContextPart{Messages: []sources.ContextMessage{m}, Next: fmt.Sprint(n + 1), Body: map[string]any{"messages": []sources.ContextMessage{m}}}
	}
	reader := func(core.Trigger) *sources.TriggerContext {
		return &sources.TriggerContext{MessageID: "m0", Read: func(_ context.Context, _, cursor string) (sources.ContextPart, error) { return page(cursor), nil }}
	}
	long := issue(2)
	long.ID, long.Threads, long.Text = "long", issue(1).Threads, strings.Repeat("a long follow-up. ", 40)
	f := setup(t, "inferred", nil, reader, nil)
	f.receive(t, issue(1), long)
	result, err := f.router.Context(ctx(), f.agent, "long")
	if err != nil {
		t.Fatal(err)
	}
	if next := result["thread"].(map[string]any)["next_cursor"]; next != "10" {
		t.Fatalf("next %v", next)
	}
	more, err := f.router.ReadContext(ctx(), f.agent, router.Read{TriggerID: "long", Resource: "thread", Cursor: "10"})
	if err != nil || !strings.Contains(more["text"].(string), "message 10") {
		t.Fatalf("more %v %v", more, err)
	}
	if batch := result["batch"].([]map[string]any); len(batch) != 1 || batch[0]["trigger_id"] != "issue-1" {
		t.Fatalf("batch %v", batch)
	}
	f2 := setup(t, "inferred", nil, reader, nil)
	third := issue(3)
	third.Threads = issue(1).Threads
	f2.receive(t, long, third)
	other, err := f2.router.Context(ctx(), f2.agent, "issue-3")
	if err != nil {
		t.Fatal(err)
	}
	batch := other["batch"].([]map[string]any)
	if len(batch) != 1 || batch[0]["trigger_id"] != "long" || batch[0]["chars"] != len(long.Text) || batch[0]["read"] != "trigger/long" {
		t.Fatalf("batch %v", batch)
	}
}

// MUL-13576 (C-05, C-09, B-28): a message in another thread naming the Issue a ticket's title names shows that ticket
// as referenced, so the Router can attach it; an Issue named by no ticket shows none.
func TestShowsTheTicketWhoseTitleNamesTheIssueAMessageInAnotherThreadNames(t *testing.T) {
	f := setup(t, "explicit", nil, nil, nil)
	first := issue(1)
	first.Text = "MUL-551 的按钮文案改成『立即预约』"
	f.receive(t, first)
	d, err := f.agent.Engine.Decide("issue-1", core.Outcome{Kind: "accept", Title: "修改 MUL-551 按钮文案为「立即预约」", Scope: "small", Said: "requested"})
	if err != nil {
		t.Fatal(err)
	}
	for n, c := range []struct {
		text string
		want int
	}{{"MUL-551 现在做到哪了", 1}, {"MUL-5510 现在做到哪了", 0}} {
		text, want := c.text, c.want
		second := issue(n + 2)
		second.Text = text
		f.receive(t, second)
		card := router.CandidateCard(f.agent, *f.agent.Engine.Candidate(second.ID))
		got, _ := card["referenced_tickets"].([]map[string]any)
		if len(got) != want || (want == 1 && got[0]["ticket_id"] != d.TicketID) {
			t.Fatalf("%s: referenced %v, want %d of %s", text, got, want, d.TicketID)
		}
	}
}
