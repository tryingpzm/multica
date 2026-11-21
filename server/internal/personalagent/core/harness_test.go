package core_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/personalagent/store/storetest"
)

// Set PA_REPO=postgres to run every Engine spec against the PostgreSQL store instead of MemoryRepositories.

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// Translated from dsh-superagent-prototype tests/core-engine.spec.ts: the harness and its fakes. Only system boundaries
// (assessor, executor, outbox, clock) are faked; the Engine and MemoryRepositories are real.

var (
	member = core.ActorRef{Source: "chat", ID: "alice"}
	guest  = core.ActorRef{Source: "chat", ID: "guest"}
)

type tOpt func(*core.Trigger)

func threads(ids ...string) tOpt {
	return func(t *core.Trigger) {
		t.Threads = nil
		for _, id := range ids {
			src, rest, ok := strings.Cut(id, ":")
			if !ok {
				src, rest = "chat", id
			}
			t.Threads = append(t.Threads, core.ThreadRef{Source: src, ID: rest})
		}
	}
}
func actor(a core.ActorRef) tOpt { return func(t *core.Trigger) { t.Actor = a } }
func bot() tOpt                  { return func(t *core.Trigger) { t.Human = false } }
func channel(thread, message string) tOpt {
	return func(t *core.Trigger) {
		t.Channel = &core.ChannelRef{Source: "chat", Thread: thread, Address: map[string]string{"message": message}}
	}
}
func meta(m map[string]any) tOpt { return func(t *core.Trigger) { t.Meta = m } }

func trigger(id string, opts ...tOpt) core.Trigger {
	pad := id
	if len(pad) < 2 {
		pad = "0" + pad
	}
	t := core.Trigger{ID: id, Source: "chat", Threads: []core.ThreadRef{{Source: "chat", ID: "t1"}}, Actor: member, Human: true,
		At: "2026-09-23T00:00:" + pad + "Z", Text: "message " + id, Channel: &core.ChannelRef{Source: "chat", Thread: "t1", Address: map[string]string{"message": id}},
		Meta: map[string]any{}}
	for _, o := range opts {
		o(&t)
	}
	return t
}

type call struct {
	ticket     core.Ticket
	assignment core.Assignment
	reason     string
}

type testPolicy struct {
	h              *harness
	gate           func(t core.Trigger, thread core.Thread, tickets []core.Ticket) core.GateResult
	revalidate     func(core.Mandate) bool
	admitsInferred func([]string) bool
}

func (p *testPolicy) Version() string      { return p.h.version }
func (p *testPolicy) Paused() bool         { return p.h.paused }
func (p *testPolicy) EvalTimeoutMs() int64 { return 60_000 }
func (p *testPolicy) Revalidate(m core.Mandate) bool {
	if p.revalidate != nil {
		return p.revalidate(m)
	}
	return p.h.version != "revoked"
}
func (p *testPolicy) AdmitsInferred(rules []string) bool {
	if p.admitsInferred != nil {
		return p.admitsInferred(rules)
	}
	return len(rules) > 0
}
func (p *testPolicy) Gate(t core.Trigger, thread core.Thread, tickets []core.Ticket) core.GateResult {
	if p.gate != nil {
		return p.gate(t, thread, tickets)
	}
	switch {
	case !t.Human:
		return core.GateResult{Reason: "not human"}
	case t.Actor.ID == "alice":
		return core.GateResult{Admissible: true, Strength: core.Explicit, Basis: core.MandateBasis{Via: "listed"}}
	case len(thread.Subscriptions) > 0:
		return core.GateResult{Admissible: true, Strength: core.Inferred, Basis: core.MandateBasis{Via: "followup"}}
	}
	return core.GateResult{Reason: "not a member"}
}

type testScope struct{}

func (testScope) Scope(_ string, m map[string]any) core.ScopeResult {
	if m["blocked"] == true {
		return core.ScopeResult{Reason: "chat not allowed"}
	}
	return core.ScopeResult{Listened: true}
}

type harness struct {
	t          *testing.T
	repo       *core.MemoryRepositories
	engine     *core.Engine
	policy     *testPolicy
	outbox     []core.OutboundMessage
	withdrawn  []string
	requests   []core.Candidate
	released   []string
	launches   []call
	starts     []call
	delivers   []call
	stops      []call
	deliveries []string
	delivered  [][2]string
	events     []core.DomainEvent
	version    string
	paused     bool
	clock      int64
	store      core.Repositories
	real       core.Policy
}

type hopt func(*harness)

func withPolicy(p core.Policy) hopt { return func(h *harness) { h.real = p } }

func paused() hopt { return func(h *harness) { h.paused = true } }
func withGate(g func(core.Trigger, core.Thread, []core.Ticket) core.GateResult) hopt {
	return func(h *harness) { h.policy.gate = g }
}
func withAdmits(f func([]string) bool) hopt { return func(h *harness) { h.policy.admitsInferred = f } }

func newHarness(t *testing.T, opts ...hopt) *harness { return newHarnessWith(t, nil, nil, opts...) }

func newHarnessWith(t *testing.T, repo core.Repositories, prepare func(core.Ticket, core.Outcome) core.Ticket, opts ...hopt) *harness {
	t.Helper()
	h := &harness{t: t, version: "v1"}
	h.policy = &testPolicy{h: h}
	for _, o := range opts {
		o(h)
	}
	if repo == nil && os.Getenv("PA_REPO") == "postgres" {
		repo = store.New(context.Background(), storetest.Open(t), newUUID())
	}
	if repo == nil {
		h.repo = core.NewMemoryRepositories()
		repo = h.repo
	}
	h.engine = core.NewEngine(core.EngineOptions{Repo: repo, Scope: testScope{}, Policy: func() core.Policy {
		if h.real != nil {
			return h.real
		}
		return h.policy
	},
		Assessor: (*fakeAssessor)(h), Executor: (*fakeExecutor)(h), Outbox: (*fakeOutbox)(h), PrepareTicket: prepare,
		Now:  func() string { return core.FormatTime(1790121600000 + h.clock*1000) },
		Emit: func(e core.DomainEvent) { h.events = append(h.events, e) }})
	h.store = repo
	return h
}

type fakeAssessor harness

func (a *fakeAssessor) Request(c core.Candidate) { a.requests = append(a.requests, c) }
func (a *fakeAssessor) Release(id string)        { a.released = append(a.released, id) }

type fakeExecutor harness

func (e *fakeExecutor) Start(t core.Ticket, a core.Assignment) {
	e.launches = append(e.launches, call{ticket: t, assignment: a})
	e.starts = append(e.starts, call{ticket: t, assignment: a})
}
func (e *fakeExecutor) Deliver(t core.Ticket, a core.Assignment) {
	e.delivers = append(e.delivers, call{ticket: t, assignment: a})
	for _, id := range e.deliveries {
		if id == t.ID {
			return
		}
	}
	e.deliveries = append(e.deliveries, t.ID)
}
func (e *fakeExecutor) Stop(t core.Ticket, a core.Assignment, reason string) {
	e.stops = append(e.stops, call{ticket: t, assignment: a, reason: reason})
}

type fakeOutbox harness

func (o *fakeOutbox) Send(m core.OutboundMessage) {
	for _, item := range o.outbox {
		if item.ID == m.ID {
			return
		}
	}
	o.outbox = append(o.outbox, m)
}
func (o *fakeOutbox) Status(id string) string {
	for _, item := range o.outbox {
		if item.ID == id {
			return "pending"
		}
	}
	return ""
}
func (o *fakeOutbox) Withdraw(id string) { o.withdrawn = append(o.withdrawn, id) }

// settle simulates the executor starting queued assignments (unless launch is false) and delivering pending inputs.
func (h *harness) settle(launch ...bool) {
	h.t.Helper()
	doLaunch := len(launch) == 0 || launch[0]
	for {
		if doLaunch && len(h.launches) > 0 {
			next := h.launches[0]
			h.launches = h.launches[1:]
			h.ok(h.engine.AssignmentStarted(next.ticket.ID, next.assignment.ID))
			continue
		}
		if len(h.deliveries) == 0 {
			return
		}
		ticketID := h.deliveries[0]
		h.deliveries = h.deliveries[1:]
		ticket := h.ticket(ticketID)
		running := core.RunningAssignment(ticket)
		if ticket.Ended != nil || running == nil {
			continue
		}
		for _, inputID := range ticket.Undelivered {
			h.delivered = append(h.delivered, [2]string{ticketID, inputID})
			h.ok(h.engine.InputsDelivered(ticketID, running.ID, []string{inputID}))
		}
	}
}

func (h *harness) pump()              { h.t.Helper(); h.ok(h.engine.EvaluateDue()); h.settle() }
func (h *harness) tick(seconds int64) { h.clock += seconds }

// read simulates the worker's Session taking the delivered Trigger Inputs it has not yet read into a step.
func (h *harness) read(ticketID string) {
	h.t.Helper()
	ids := []string{}
	for _, input := range h.ticket(ticketID).Inputs {
		if input.Kind == "trigger" && input.ReadAt != nil && *input.ReadAt == "" {
			ids = append(ids, input.ID)
		}
	}
	h.ok(h.engine.InputsRead(ticketID, ids))
}

func (h *harness) decide(c core.Candidate, o core.Outcome) (core.Decision, error) {
	o.EvalID = c.EvalID
	return h.engine.Decide(c.Triggers[0].ID, o)
}
func (h *harness) mustDecide(c core.Candidate, o core.Outcome) core.Decision {
	h.t.Helper()
	d, err := h.decide(c, o)
	h.ok(err)
	return d
}
func (h *harness) last() core.Candidate {
	h.t.Helper()
	if len(h.requests) == 0 {
		h.t.Fatal("no assessor request")
	}
	return h.requests[len(h.requests)-1]
}

func (h *harness) receive(t core.Trigger) string {
	h.t.Helper()
	r, err := h.engine.Receive(t)
	h.ok(err)
	return r
}

func (h *harness) ok(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatalf("unexpected error: %v", err)
	}
}

func (h *harness) ticket(id string) *core.Ticket {
	h.t.Helper()
	t := h.store.Tickets().Get(id)
	if t == nil {
		h.t.Fatalf("no ticket %s", id)
	}
	return t
}
func (h *harness) thread(key string) *core.Thread {
	h.t.Helper()
	t := h.store.Threads().Get(key)
	if t == nil {
		h.t.Fatalf("no thread %s", key)
	}
	return t
}
func (h *harness) pending(key string) []string          { h.t.Helper(); return h.thread(key).Pending }
func (h *harness) decisions(key string) []core.Decision { return h.store.Decisions().ByThread(key) }
func (h *harness) disposition(id string) *core.TriggerDisposition {
	return h.store.Triggers().Disposition(id)
}
func (h *harness) owners(t core.Trigger) []string {
	return core.FollowupOwners(t, core.Records(h.store))
}

func (h *harness) report(ticketID, state, summary string) core.Ticket {
	h.t.Helper()
	t, err := h.engine.Report(ticketID, core.WorkerReport{State: state, Summary: summary})
	h.ok(err)
	return t
}

func accept(title string) core.Outcome {
	return core.Outcome{Kind: "accept", Title: title, Scope: "small", Said: "requested"}
}

// accepted receives a member's request on thread and accepts it with an acknowledgement.
func (h *harness) accepted(id, thread string) core.Decision {
	h.t.Helper()
	h.receive(trigger(id, threads(thread)))
	h.pump()
	o := accept("Fix")
	o.Acknowledgement = "ok"
	d := h.mustDecide(h.last(), o)
	h.settle()
	return d
}

// assertions

func eq(t *testing.T, got, want any, msg ...any) {
	t.Helper()
	if !reflect.DeepEqual(norm(got), norm(want)) {
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		t.Fatalf("%s\n got: %s\nwant: %s", fmt.Sprint(msg...), g, w)
	}
}

// norm compares values by their JSON form, so nil and empty slices or maps compare equal.
func norm(v any) any {
	data, err := json.Marshal(v)
	if err != nil {
		return v
	}
	var out any
	_ = json.Unmarshal(data, &out)
	return strip(out)
}

func strip(v any) any {
	switch x := v.(type) {
	case []any:
		if len(x) == 0 {
			return nil
		}
		for i := range x {
			x[i] = strip(x[i])
		}
	case map[string]any:
		if len(x) == 0 {
			return nil
		}
		for k := range x {
			x[k] = strip(x[k])
		}
	}
	return v
}

func userErr(t *testing.T, err error, code string, values ...string) {
	t.Helper()
	ue, ok := err.(*core.UserError)
	if !ok {
		t.Fatalf("want UserError %s, got %v", code, err)
	}
	if ue.Code != code {
		t.Fatalf("want UserError %s, got %s (%v)", code, ue.Code, err)
	}
	for i := 0; i+1 < len(values); i += 2 {
		if ue.Values[values[i]] != values[i+1] {
			t.Fatalf("UserError %s value %s: got %q want %q", code, values[i], ue.Values[values[i]], values[i+1])
		}
	}
}

func triggerIDs(c core.Candidate) []string {
	out := []string{}
	for _, t := range c.Triggers {
		out = append(out, t.ID)
	}
	return out
}

func kinds(ticket *core.Ticket) []string {
	out := []string{}
	for _, i := range ticket.Inputs {
		out = append(out, i.Kind)
	}
	return out
}

func inputsFor(ticket *core.Ticket, triggerID string) []*core.TicketInput {
	out := []*core.TicketInput{}
	for _, i := range ticket.Inputs {
		if i.Kind == "trigger" && i.TriggerID == triggerID {
			out = append(out, i)
		}
	}
	return out
}

func lastInput(ticket *core.Ticket) *core.TicketInput { return ticket.Inputs[len(ticket.Inputs)-1] }
func lastDecision(ds []core.Decision) core.Decision   { return ds[len(ds)-1] }
func str(s string) *string                            { return &s }
func states(ticket core.Ticket) []string {
	out := []string{}
	for _, a := range ticket.Assignments {
		out = append(out, a.State)
	}
	return out
}
