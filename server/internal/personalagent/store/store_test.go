package store_test

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/personalagent/store/storetest"
)

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func open(t *testing.T) (*pgxpool.Pool, *store.Store) {
	pool := storetest.Open(t)
	return pool, store.New(context.Background(), pool, uuid())
}

func trig(id, thread string) core.Trigger {
	return core.Trigger{ID: id, Source: "chat", Threads: []core.ThreadRef{{Source: "chat", ID: thread}}, Actor: core.ActorRef{Source: "chat", ID: "alice"},
		Human: true, At: "2026-09-23T00:00:0" + id + ".000Z", Text: "m", Meta: map[string]any{}}
}

func TestAtomicRollsBackEverythingOnPanic(t *testing.T) {
	_, s := open(t)
	func() {
		defer func() { _ = recover() }()
		s.Atomic(func() {
			s.Triggers().Save(trig("1", "t1"))
			s.Threads().Save(core.Thread{Key: "chat:t1", Pending: []string{"1"}})
			panic("refused")
		})
	}()
	if s.Triggers().Get("1") != nil || s.Threads().Get("chat:t1") != nil {
		t.Fatal("rolled back work is visible")
	}
	s.Atomic(func() { s.Triggers().Save(trig("1", "t1")) })
	if s.Triggers().Get("1") == nil {
		t.Fatal("committed work missing")
	}
}

func TestAtomicSerializesOneAgentAcrossStores(t *testing.T) {
	pool, a := open(t)
	b := store.New(context.Background(), pool, a.AgentID())
	other := store.New(context.Background(), pool, uuid())
	entered := make(chan struct{})
	release := make(chan struct{})
	var order []string
	var mu sync.Mutex
	note := func(s string) { mu.Lock(); order = append(order, s); mu.Unlock() }
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		a.Atomic(func() { note("a-in"); close(entered); <-release; note("a-out") })
	}()
	<-entered
	// Another agent is not held up by the lock.
	done := make(chan struct{})
	go func() { other.Atomic(func() { note("other") }); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("another agent waited on the lock")
	}
	wg.Add(1)
	go func() { defer wg.Done(); b.Atomic(func() { note("b") }) }()
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()
	if fmt.Sprint(order) != "[a-in other a-out b]" {
		t.Fatalf("order: %v", order)
	}
}

func TestProjectionsFollowTheAggregates(t *testing.T) {
	pool, s := open(t)
	ctx := context.Background()
	s.Atomic(func() {
		s.Threads().Save(core.Thread{Key: "chat:t1", Subscriptions: []string{"k1", "k2"}, Context: []string{"k2"}, Pending: []string{},
			Evaluating: &core.Evaluation{ID: "e1", ThreadKey: "chat:t1", TriggerIDs: []string{"1"}, Strength: "explicit", StartedAt: "s", Deadline: "d"}})
		s.Tickets().Save(core.Ticket{ID: "k1", Title: "Fix", CreatedAt: "c", UpdatedAt: "u", Assignments: []*core.Assignment{{ID: "a1", State: "running", CreatedAt: "c",
			Report: &core.Report{State: "progress", Summary: "working", At: "r1"}}}})
		s.Tickets().Save(core.Ticket{ID: "k1", Title: "Fix", CreatedAt: "c", UpdatedAt: "u2", Ended: &core.Ended{At: "e", By: "executor", Outcome: "completed"},
			Assignments: []*core.Assignment{{ID: "a1", State: "ended", CreatedAt: "c", Report: &core.Report{State: "ready", Summary: "done", At: "r2"}}}})
	})
	var subs, ctxSubs, evals, reports int
	var state string
	var open bool
	q := func(sql string, dst ...any) {
		if err := pool.QueryRow(ctx, sql, s.AgentID()).Scan(dst...); err != nil {
			t.Fatal(err)
		}
	}
	q("SELECT count(*), count(*) FILTER (WHERE context) FROM pa_subscription WHERE agent_id = $1", &subs, &ctxSubs)
	q("SELECT count(*) FROM pa_eval WHERE agent_id = $1", &evals)
	q("SELECT count(*) FROM pa_report WHERE agent_id = $1", &reports)
	q("SELECT state FROM pa_assignment WHERE agent_id = $1", &state)
	q("SELECT open FROM pa_ticket WHERE agent_id = $1", &open)
	if subs != 2 || ctxSubs != 1 || evals != 1 || reports != 2 || state != "ended" || open {
		t.Fatalf("projections: subs %d ctx %d evals %d reports %d state %s open %v", subs, ctxSubs, evals, reports, state, open)
	}
	s.Atomic(func() { s.Threads().Save(core.Thread{Key: "chat:t1", Subscriptions: []string{}, Pending: []string{}}) })
	q("SELECT count(*) FROM pa_subscription WHERE agent_id = $1", &subs)
	q("SELECT count(*) FROM pa_eval WHERE agent_id = $1", &evals)
	if subs != 0 || evals != 0 || len(s.Tickets().Open()) != 0 || len(s.Threads().Active()) != 0 {
		t.Fatal("projections not rewritten")
	}
}

func TestSettingsAssembleFromAgentRows(t *testing.T) {
	pool, s := open(t)
	ctx := context.Background()
	agent := store.CreateAgent(s, uuid(), uuid())
	s = store.New(ctx, pool, agent.ID)
	for _, sql := range []string{
		"UPDATE personal_agent SET mode = 'auto' WHERE id = $1",
		"INSERT INTO pa_source_scope (agent_id, source, row_key, listen, admission) VALUES ($1, 'github', 'g1', 'all', '{\"unlistedUnaddressed\":\"inferred\"}')",
		"INSERT INTO pa_allowed (agent_id, source, actor_id, name) VALUES ($1, 'github', 'alice', 'Alice')",
		"INSERT INTO pa_allowed (agent_id, source, actor_id, kind) VALUES ($1, 'github', 'bot1', 'bot')",
	} {
		if _, err := pool.Exec(ctx, sql, agent.ID); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	part := got.Policy.Sources["github"]
	if got.Eval.Mode != "auto" || len(got.Policy.Rules) != 2 || len(part.Allowed) != 1 || part.Allowed[0].Name != "Alice" || len(part.Bots) != 1 ||
		part.Admission["g1"].UnlistedUnaddressed != "inferred" || fmt.Sprint(got.Sources["github"].Config["rows"]) != "[map[listen:all row:g1]]" {
		t.Fatalf("settings: %+v", got)
	}
	if again := store.CreateAgent(s, agent.WorkspaceID, agent.UserID); again.ID != agent.ID {
		t.Fatal("second agent for one user")
	}
}

// Translated from tests/outbox.spec.ts; the Source channel is faked at the boundary.

func TestOutboxHoldsEveryMessageWhilePausedAndSendsOnResume(t *testing.T) {
	_, s := open(t)
	o := store.NewOutbox(s, func() bool { return true })
	sent := []string{}
	send := func(m core.OutboundMessage) (string, error) { sent = append(sent, m.Text); return store.Sent, nil }
	s.Atomic(func() {
		o.Send(core.OutboundMessage{ID: "held", Channel: core.ChannelRef{Source: "github", Thread: "x"}, Text: "已合并。", Kind: "notice"})
	})
	if err := o.Deliver(true, send); err != nil {
		t.Fatal(err)
	}
	if o.Status("held") != "pending" || len(sent) != 0 {
		t.Fatalf("paused: %s %v", o.Status("held"), sent)
	}
	if err := o.Deliver(false, send); err != nil {
		t.Fatal(err)
	}
	if o.Status("held") != "sent" || fmt.Sprint(sent) != "[已合并。]" {
		t.Fatalf("resumed: %s %v", o.Status("held"), sent)
	}
}

// MUL-13446: a send that fails is handed back to its owner with the error, not retried silently.
func TestOutboxFailedSendWaitsForOwnerWithItsError(t *testing.T) {
	_, s := open(t)
	o := store.NewOutbox(s, func() bool { return true })
	fail := true
	send := func(m core.OutboundMessage) (string, error) {
		if fail {
			return "", errors.New("feishu: no sender")
		}
		return store.Sent, nil
	}
	o.Send(core.OutboundMessage{ID: "r", Channel: core.ChannelRef{Source: "feishu", Thread: "c:1"}, Text: "收到", Kind: "reply"})
	if err := o.Deliver(false, send); err == nil {
		t.Fatal("failure not reported")
	}
	if o.Receipt("r") != "pending_confirm" || o.Error("r") != "feishu: no sender" {
		t.Fatalf("after failure: %s %q", o.Receipt("r"), o.Error("r"))
	}
	fail = false
	if !o.Confirm("r") || o.Error("r") != "" {
		t.Fatalf("confirm kept error %q", o.Error("r"))
	}
	if err := o.Deliver(false, send); err != nil || o.Receipt("r") != "sent" {
		t.Fatalf("resend: %v %s", err, o.Receipt("r"))
	}
}

func TestOutboxKeepsMessageForUninstalledSourceWaitingUntilItRegisters(t *testing.T) {
	_, s := open(t)
	o := store.NewOutbox(s, func() bool { return true })
	installed := false
	sent := []string{}
	send := func(m core.OutboundMessage) (string, error) {
		if !installed {
			return store.Waiting, nil
		}
		sent = append(sent, m.ID)
		return store.Sent, nil
	}
	o.Send(core.OutboundMessage{ID: "waiting", Channel: core.ChannelRef{Source: "probe", Thread: "probe:1"}, Text: "Done", Kind: "reply"})
	_ = o.Deliver(false, send)
	if o.Status("waiting") != "pending" {
		t.Fatal("not waiting")
	}
	installed = true
	_ = o.Deliver(false, send)
	if o.Status("waiting") != "sent" || fmt.Sprint(sent) != "[waiting]" {
		t.Fatalf("after register: %s %v", o.Status("waiting"), sent)
	}
}

func TestOutboxConfirmationOutsideAutoAndIdempotentWithdrawableReceipts(t *testing.T) {
	_, s := open(t)
	auto := false
	o := store.NewOutbox(s, func() bool { return auto })
	msg := func(id, ticket, text string) core.OutboundMessage {
		return core.OutboundMessage{ID: id, TicketID: ticket, Channel: core.ChannelRef{Source: "github", Thread: "g1"}, Text: text, Kind: "reply"}
	}
	o.Send(msg("r1", "k1", "first"))
	o.Send(msg("r1", "k1", "replaced"))
	if o.Receipt("r1") != "pending_confirm" || o.Status("r1") != "pending" {
		t.Fatalf("receipt %s", o.Receipt("r1"))
	}
	sent := []string{}
	send := func(m core.OutboundMessage) (string, error) { sent = append(sent, m.Text); return store.Sent, nil }
	_ = o.Deliver(false, send)
	if len(sent) != 0 {
		t.Fatal("sent without confirmation")
	}
	if !o.Confirm("r1") || o.Confirm("r1") {
		t.Fatal("confirm")
	}
	_ = o.Deliver(false, send)
	if fmt.Sprint(sent) != "[first]" {
		t.Fatalf("sent %v", sent)
	}
	auto = true
	o.Send(msg("r2", "k1", "two"))
	o.Withdraw("r2")
	o.Withdraw("r1")
	if o.Receipt("r2") != "discarded" || o.Status("r2") != "discarded" || o.Status("r1") != "sent" {
		t.Fatal("withdraw")
	}
	// A message whose conversation is gone settles dropped; one that fails holds the ticket's later ones to that conversation.
	o.Send(msg("r3", "k2", "gone"))
	o.Send(msg("r4", "k3", "fails"))
	o.Send(msg("r5", "k3", "behind"))
	_ = o.Deliver(false, func(m core.OutboundMessage) (string, error) {
		switch m.ID {
		case "r3":
			return store.Gone, nil
		case "r4":
			return "", fmt.Errorf("network")
		}
		t.Fatalf("delivered %s behind a failed message", m.ID)
		return store.Sent, nil
	})
	if o.Status("r3") != "dropped" || o.Status("r4") != "pending" || o.Status("r5") != "pending" {
		t.Fatal("settle")
	}
}
