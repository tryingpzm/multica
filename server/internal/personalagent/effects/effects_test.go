package effects_test

// Translated from dsh-superagent-prototype tests/effects.spec.ts. Go errors carry no "Error: " prefix, so the failure
// texts in health details are the error messages alone.

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/personalagent/effects"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/personalagent/store/storetest"
)

type fixture struct {
	pool    *pgxpool.Pool
	store   *store.Store
	agent   string
	clock   atomic.Int64
	effects *effects.Effects
}

func setup(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{pool: storetest.Open(t)}
	f.clock.Store(time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC).UnixMilli())
	s := store.New(context.Background(), f.pool, "")
	a := store.CreateAgent(s, "11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222")
	f.agent = a.ID
	f.store = store.New(context.Background(), f.pool, a.ID)
	f.effects = f.next()
	f.effects.Start()
	return f
}

func (f *fixture) next() *effects.Effects { return effects.New(f.pool, f.clock.Load) }
func (f *fixture) advance(ms int64)       { f.clock.Add(ms) }

type relay struct {
	Text string `json:"text"`
}

type calls struct {
	mu    sync.Mutex
	keys  []string
	texts []string
}

func (c *calls) add(key string, payload json.RawMessage) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	var r relay
	_ = json.Unmarshal(payload, &r)
	c.keys = append(c.keys, key)
	c.texts = append(c.texts, r.Text)
	return len(c.keys)
}

func (c *calls) Keys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.keys...)
}

func (c *calls) Texts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.texts...)
}

func (c *calls) Len() int { return len(c.Keys()) }

func equal(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func recorder(c *calls) effects.Handler {
	return func(_ context.Context, _, key string, payload json.RawMessage) error { c.add(key, payload); return nil }
}

func TestRunsAnEffectQueuedInATransactionOnceItCommitsAndNoneFromARollback(t *testing.T) {
	f := setup(t)
	c := &calls{}
	f.effects.Handle("relay", recorder(c), effects.Options{})
	f.store.Atomic(func() { f.effects.Enqueue(f.store, "relay", "kept", relay{"a"}) })
	func() {
		defer func() {
			if r := recover(); r == nil || !strings.Contains(r.(error).Error(), "rolled back") {
				t.Fatalf("want the rollback, got %v", r)
			}
		}()
		f.store.Atomic(func() { f.effects.Enqueue(f.store, "relay", "dropped", relay{"b"}); panic(errors.New("rolled back")) })
	}()
	f.effects.Idle("")
	equal(t, c.Keys(), []string{"kept"})
	equal(t, c.Texts(), []string{"a"})
	if f.effects.Pending(f.agent, "relay", "") {
		t.Fatal("nothing should be pending")
	}
}

func TestKeepsOneEffectPerKeyWithTheLatestPayloadAndRunsItAgainWhenANewOneArrivesWhileItRuns(t *testing.T) {
	f := setup(t)
	hold := make(chan struct{})
	c := &calls{}
	f.effects.Handle("relay", func(_ context.Context, _, key string, payload json.RawMessage) error {
		c.add(key, payload)
		var r relay
		_ = json.Unmarshal(payload, &r)
		if r.Text == "first" {
			<-hold
		}
		return nil
	}, effects.Options{})
	f.effects.Enqueue(f.store, "relay", "k", relay{"first"})
	waitFor(t, func() bool { return c.Len() == 1 })
	f.effects.Enqueue(f.store, "relay", "k", relay{"second"})
	f.effects.Enqueue(f.store, "relay", "k", relay{"third"})
	close(hold)
	f.effects.Idle("")
	equal(t, c.Texts(), []string{"first", "third"})
}

func TestRunsAKindOldestFirstAsManyAtOnceAsItsLimitAllowsAndHoldsItWhileTheLimitIs0(t *testing.T) {
	f := setup(t)
	var limit atomic.Int64
	limit.Store(2)
	var mu sync.Mutex
	holds := map[string]chan struct{}{}
	c := &calls{}
	f.effects.Handle("launch", func(_ context.Context, _, key string, payload json.RawMessage) error {
		hold := make(chan struct{})
		mu.Lock()
		holds[key] = hold
		mu.Unlock()
		c.add(key, payload)
		<-hold
		return nil
	}, effects.Options{Limit: func() int { return int(limit.Load()) }})
	release := func(key string) { mu.Lock(); close(holds[key]); mu.Unlock() }
	for _, key := range []string{"a", "b", "c"} {
		f.effects.Enqueue(f.store, "launch", key, map[string]string{"ticketId": key, "assignmentId": key})
	}
	// Handlers run on their own goroutines, so the order they record in is not the order they started in.
	started := func() string { keys := c.Keys(); sort.Strings(keys); return strings.Join(keys, ",") }
	waitFor(t, func() bool { return started() == "a,b" })
	limit.Store(0)
	release("a")
	waitFor(t, func() bool { return !f.effects.Pending(f.agent, "launch", "a") })
	f.effects.Kick()
	time.Sleep(50 * time.Millisecond)
	if c.Len() != 2 {
		t.Fatalf("held kind ran: %v", c.Keys())
	}
	limit.Store(2)
	release("b")
	waitFor(t, func() bool { return started() == "a,b,c" })
	release("c")
	f.effects.Idle("")
}

func TestKeepsAnEffectsPlaceInItsQueueWhenItIsQueuedAgainWithANewPayload(t *testing.T) {
	f := setup(t)
	var room atomic.Int64
	c := &calls{}
	f.effects.Handle("launch", recorder(c), effects.Options{Limit: func() int { return int(room.Load()) }})
	f.store.Atomic(func() {
		f.effects.Enqueue(f.store, "launch", "first", map[string]string{"ticketId": "t1", "assignmentId": "a1"})
		f.effects.Enqueue(f.store, "launch", "second", map[string]string{"ticketId": "t2", "assignmentId": "a2"})
		f.effects.Enqueue(f.store, "launch", "first", map[string]string{"ticketId": "t1", "assignmentId": "a1"})
	})
	f.effects.Idle("")
	room.Store(1)
	f.effects.Idle("")
	equal(t, c.Keys(), []string{"first", "second"})
}

func TestRetriesAFailedEffectAfterADelayThatDoublesUpToItsMaximumAndReportsErrorAfterThreeFailures(t *testing.T) {
	f := setup(t)
	var fail atomic.Bool
	fail.Store(true)
	c := &calls{}
	f.effects.Handle("notice", func(_ context.Context, _, key string, payload json.RawMessage) error {
		c.add(key, payload)
		if fail.Load() {
			return errors.New("session busy")
		}
		return nil
	}, effects.Options{Retry: [2]int64{1000, 3000}})
	f.effects.Enqueue(f.store, "notice", "prompts", map[string]any{"value": map[string]any{"prompt_config_saved": 2}, "summary": "Prompt configuration saved"})
	f.effects.Idle("")
	if h := f.effects.Health("notice"); h.State != "ready" || h.Detail != "1 waiting since 2026-09-09T00:00:00.000Z" {
		t.Fatalf("health %+v", h)
	}
	for _, wait := range []int64{1000, 2000, 3000, 3000} {
		f.advance(wait - 1)
		f.effects.Idle("")
		before := c.Len()
		f.advance(1)
		f.effects.Idle("")
		if c.Len() != before+1 {
			t.Fatalf("after %dms: %d calls, want %d", wait, c.Len(), before+1)
		}
		if wait == 2000 {
			h := f.effects.Health("notice")
			if h.State != "error" || !strings.Contains(h.Detail, "1 waiting since 2026-09-09T00:00:00.000Z; the last failure: session busy") {
				t.Fatalf("health %+v", h)
			}
		}
	}
	fail.Store(false)
	f.advance(3000)
	f.effects.Idle("")
	if h := f.effects.Health("notice"); h.State != "ready" || h.Detail != "Nothing waiting" {
		t.Fatalf("health %+v", h)
	}
	if f.effects.Pending(f.agent, "notice", "") {
		t.Fatal("still pending")
	}
}

func TestStartsNothingOnceDrainedKeepsWhatWaitsAndWhatShutdownCutShortAndRunsItAfterTheNextStart(t *testing.T) {
	f := setup(t)
	hold := make(chan struct{})
	var started atomic.Bool
	f.effects.Handle("relay", func(context.Context, string, string, json.RawMessage) error {
		started.Store(true)
		<-hold
		return errors.New("aborted")
	}, effects.Options{})
	f.effects.Enqueue(f.store, "relay", "cut", relay{"a"})
	waitFor(t, started.Load)
	drained := make(chan struct{})
	go func() { f.effects.Drain(); close(drained) }()
	time.Sleep(20 * time.Millisecond)
	f.effects.Enqueue(f.store, "relay", "later", relay{"b"})
	close(hold)
	<-drained
	next := f.next()
	next.Start()
	c := &calls{}
	next.Handle("relay", recorder(c), effects.Options{})
	next.Idle("")
	equal(t, c.Keys(), []string{"cut", "later"})
}

func TestLetsAHandlerDeferAnEffectWithoutAFailureUntilALaterDispatchAfterItsFirstDelay(t *testing.T) {
	f := setup(t)
	var later atomic.Bool
	later.Store(true)
	c := &calls{}
	f.effects.Handle("launch", func(_ context.Context, _, key string, payload json.RawMessage) error {
		c.add(key, payload)
		if later.Load() {
			return effects.NewLater("paused")
		}
		return nil
	}, effects.Options{Retry: [2]int64{5000, 60_000}})
	f.effects.Enqueue(f.store, "launch", "a", map[string]string{"ticketId": "t", "assignmentId": "a"})
	f.effects.Idle("")
	if h := f.effects.Health("launch"); h.State != "ready" || h.Detail != "1 waiting since 2026-09-09T00:00:00.000Z" {
		t.Fatalf("health %+v", h)
	}
	later.Store(false)
	f.advance(4999)
	f.effects.Idle("")
	if c.Len() != 1 {
		t.Fatalf("ran %d times", c.Len())
	}
	f.advance(1)
	f.effects.Idle("")
	if c.Len() != 2 {
		t.Fatalf("ran %d times", c.Len())
	}
	if f.effects.Pending(f.agent, "launch", "") {
		t.Fatal("still pending")
	}
}

func TestRunsAnOrderedKindOneAtATimeInTheOrderQueuedAndHoldsTheLaterOnesWhileAnEarlierOneFails(t *testing.T) {
	f := setup(t)
	var failing atomic.Bool
	failing.Store(true)
	c := &calls{}
	f.effects.Handle("relay", func(_ context.Context, _, key string, payload json.RawMessage) error {
		c.add(key, payload)
		if key == "first" && failing.Load() {
			return errors.New("offline")
		}
		return nil
	}, effects.Options{Ordered: true, Limit: func() int { return 4 }, Retry: [2]int64{1000, 1000}})
	f.effects.Enqueue(f.store, "relay", "first", relay{"a"})
	f.effects.Enqueue(f.store, "relay", "second", relay{"b"})
	f.effects.Idle("")
	f.advance(1000)
	f.effects.Idle("")
	equal(t, c.Keys(), []string{"first", "first"})
	failing.Store(false)
	f.advance(1000)
	f.effects.Idle("")
	equal(t, c.Keys(), []string{"first", "first", "first", "second"})
}

func TestKeepsAQuietKindsFailuresOutOfItsHealthAndAsksNoLimitWhileNothingIsDue(t *testing.T) {
	f := setup(t)
	var asked atomic.Int64
	f.effects.Handle("delivery", func(context.Context, string, string, json.RawMessage) error { return errors.New("Session gone") },
		effects.Options{Quiet: true, Limit: func() int { asked.Add(1); return 4 }, Retry: [2]int64{1000, 1000}})
	f.effects.Kick()
	f.effects.Idle("")
	if asked.Load() != 0 {
		t.Fatal("limit asked while nothing was due")
	}
	f.effects.Enqueue(f.store, "delivery", "t", map[string]string{"ticketId": "t"})
	for i := 0; i < 4; i++ {
		f.effects.Idle("")
		f.advance(1000)
	}
	if h := f.effects.Health("delivery"); h.State != "ready" || h.Detail != "1 waiting since 2026-09-09T00:00:00.000Z" {
		t.Fatalf("health %+v", h)
	}
}

func TestKeepsAnEffectCancelledAndQueuedAgainWhileItsFirstRunGoesOnWhichTheFirstRunDoesNotEnd(t *testing.T) {
	f := setup(t)
	hold := make(chan struct{})
	c := &calls{}
	f.effects.Handle("relay", func(_ context.Context, _, key string, payload json.RawMessage) error {
		if c.add(key, payload) == 1 {
			<-hold
		}
		return nil
	}, effects.Options{})
	f.effects.Enqueue(f.store, "relay", "k", relay{"first"})
	waitFor(t, func() bool { return c.Len() == 1 })
	f.effects.Cancel(f.store, "relay", "k")
	f.effects.Enqueue(f.store, "relay", "k", relay{"second"})
	close(hold)
	f.effects.Idle("")
	equal(t, c.Texts(), []string{"first", "second"})
	if f.effects.Pending(f.agent, "relay", "") {
		t.Fatal("still pending")
	}
}

func TestQueuesAnEffectAfterARestartBehindTheOnesThatWaitedForAKindThatKeepsItsOrder(t *testing.T) {
	f := setup(t)
	hold := make(chan struct{})
	var started atomic.Bool
	f.effects.Handle("relay", func(context.Context, string, string, json.RawMessage) error {
		started.Store(true)
		<-hold
		return errors.New("aborted")
	}, effects.Options{Ordered: true})
	f.effects.Enqueue(f.store, "relay", "first", relay{"a"})
	f.effects.Enqueue(f.store, "relay", "second", relay{"b"})
	waitFor(t, started.Load)
	drained := make(chan struct{})
	go func() { f.effects.Drain(); close(drained) }()
	time.Sleep(20 * time.Millisecond)
	close(hold)
	<-drained
	next := f.next()
	next.Enqueue(f.store, "relay", "third", relay{"c"})
	next.Start()
	c := &calls{}
	next.Handle("relay", recorder(c), effects.Options{Ordered: true})
	next.Idle("")
	equal(t, c.Keys(), []string{"first", "second", "third"})
}

func TestReportsAFailureOfTheQueueItselfUnderItsKindUntilADispatchOfThatKindSucceedsAgain(t *testing.T) {
	f := setup(t)
	var broken atomic.Bool
	broken.Store(true)
	f.effects.Handle("relay", func(context.Context, string, string, json.RawMessage) error { return nil },
		effects.Options{Limit: func() int {
			if broken.Load() {
				panic(errors.New("database is locked"))
			}
			return 1
		}})
	f.effects.Enqueue(f.store, "relay", "k", relay{"a"})
	f.effects.Idle("")
	if h := f.effects.Health("relay"); h.State != "error" || h.Detail != "The effect queue failed: database is locked" {
		t.Fatalf("health %+v", h)
	}
	broken.Store(false)
	f.effects.Kick()
	f.effects.Idle("")
	if h := f.effects.Health("relay"); h.State != "ready" || h.Detail != "Nothing waiting" {
		t.Fatalf("health %+v", h)
	}
}

func TestRefusesASecondHandlerForAKind(t *testing.T) {
	f := setup(t)
	f.effects.Handle("relay", func(context.Context, string, string, json.RawMessage) error { return nil }, effects.Options{})
	defer func() {
		if r := recover(); r != "The relay effects already have a handler" {
			t.Fatalf("got %v", r)
		}
	}()
	f.effects.Handle("relay", func(context.Context, string, string, json.RawMessage) error { return nil }, effects.Options{})
}
