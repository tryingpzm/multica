package delivery_test

// Translated from dsh-superagent-prototype tests/delivery.spec.ts. SA's three legacy-identity cases (retired plugin
// and user-RPC sources) have no stored counterpart in Multica and are not translated.

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/delivery"
)

type session struct {
	key                 string
	nextTurn, nextStep  []delivery.Message
	steer, followup, in []delivery.Message
}

func (s *session) Key() string { return s.key }
func (s *session) Pending() []delivery.Message {
	return append(append([]delivery.Message{}, s.nextTurn...), s.nextStep...)
}
func (s *session) Steer(m delivery.Message) {
	s.steer = append(s.steer, m)
	s.nextStep = append(s.nextStep, m)
}
func (s *session) Followup(m delivery.Message) {
	s.followup = append(s.followup, m)
	s.nextTurn = append(s.nextTurn, m)
}
func (s *session) Inject(m delivery.Message) { s.in = append(s.in, m) }

type store struct {
	mu       sync.Mutex
	history  []delivery.Message
	flushes  int
	flushErr []error
	readErr  []error
	reads    int
	onRead   func()
}

func (p *store) Flush(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushes++
	if len(p.flushErr) > 0 {
		err := p.flushErr[0]
		p.flushErr = p.flushErr[1:]
		return err
	}
	return nil
}

func (p *store) Read(_ context.Context, _ string, offset, length int) ([]delivery.Message, error) {
	p.mu.Lock()
	p.reads++
	if len(p.readErr) > 0 {
		err := p.readErr[0]
		p.readErr = p.readErr[1:]
		p.mu.Unlock()
		return nil, err
	}
	hook := p.onRead
	p.onRead = nil
	p.mu.Unlock()
	if hook != nil {
		hook()
	}
	if offset >= len(p.history) {
		return nil, nil
	}
	end := offset + length
	if end > len(p.history) {
		end = len(p.history)
	}
	return p.history[offset:end], nil
}

type sessions struct{ live map[string]delivery.Session }

func (s *sessions) Current(key string) delivery.Session { return s.live[key] }

type fixture struct {
	s     *session
	p     *store
	all   *sessions
	d     *delivery.Delivery
	ctx   context.Context
	abort context.CancelFunc
}

func setup(history ...delivery.Message) *fixture {
	s := &session{key: "delivery-test"}
	p := &store{history: history}
	all := &sessions{live: map[string]delivery.Session{s.key: s}}
	ctx, cancel := context.WithCancel(context.Background())
	return &fixture{s: s, p: p, all: all, d: delivery.New(p, all), ctx: ctx, abort: cancel}
}

func (f *fixture) send(id, mode string) error {
	return f.d.Send(f.ctx, f.s, id, `{"event_id":"event"}`, mode)
}

// consume is the session taking a message out of its inbox.
func (f *fixture) consume(m delivery.Message) {
	f.s.nextStep, f.s.nextTurn = nil, nil
	f.d.Observe(f.s, m)
}

func equal(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func TestDeliversAsARelayDeduplicatingPendingAndConsumedMessages(t *testing.T) {
	for _, mode := range []string{delivery.Queue, delivery.Steer} {
		f := setup()
		target := func() []delivery.Message {
			if mode == delivery.Queue {
				return f.s.followup
			}
			return f.s.steer
		}
		_ = f.send("request", mode)
		equal(t, len(target()), 1)
		message := target()[0]
		equal(t, message.Source, delivery.Source{Kind: "personal-agent", Form: "relay"})
		_ = f.send("request", mode)
		f.consume(message)
		_ = f.send("request", mode)
		equal(t, len(target()), 1)
		equal(t, f.p.reads, 1)
		restored := setup(message)
		_ = restored.send("request", mode)
		equal(t, [2]int{len(restored.s.steer), len(restored.s.followup)}, [2]int{0, 0})
	}
}

func TestRetriesAFailedDurabilityBarrierWithoutAnotherDelivery(t *testing.T) {
	f := setup()
	f.p.flushErr = []error{nil, errors.New("disk busy")}
	if err := f.send("request", delivery.Steer); err == nil || err.Error() != "disk busy" {
		t.Fatalf("got %v", err)
	}
	f.consume(f.s.steer[0])
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = f.send("request", delivery.Steer) }()
	}
	wg.Wait()
	equal(t, len(f.s.steer), 1)
	equal(t, f.p.flushes, 4)
}

func TestRetriesHistoryFailuresAndReadsPastTheFirstPage(t *testing.T) {
	history := make([]delivery.Message, 0, 513)
	for i := 0; i < 512; i++ {
		history = append(history, delivery.Message{Source: delivery.Source{Kind: "user"}})
	}
	history = append(history, delivery.Message{ID: delivery.MessageID("request"), Source: delivery.Source{Kind: "personal-agent", Form: "relay"}})
	f := setup(history...)
	f.p.readErr = []error{errors.New("read failed")}
	if err := f.send("request", delivery.Steer); err == nil || err.Error() != "read failed" {
		t.Fatalf("got %v", err)
	}
	if err := f.send("request", delivery.Steer); err != nil {
		t.Fatal(err)
	}
	equal(t, len(f.s.steer), 0)
}

func TestKeepsConsumptionThatHappensDuringTheInitialHistoryRead(t *testing.T) {
	f := setup()
	old := delivery.Message{ID: delivery.MessageID("request"), Source: delivery.Source{Kind: "personal-agent", Form: "relay"}}
	f.p.onRead = func() { f.consume(old) }
	_ = f.send("request", delivery.Steer)
	equal(t, len(f.s.steer), 0)
}

func TestDoesNotDispatchWhenCancelledOrDisposedDuringHistoryLoading(t *testing.T) {
	cancelled := setup()
	cancelled.p.onRead = cancelled.abort
	if err := cancelled.send("request", delivery.Steer); err == nil {
		t.Fatal("cancelled send succeeded")
	}
	equal(t, len(cancelled.s.steer), 0)
	disposed := setup()
	disposed.p.onRead = func() { delete(disposed.all.live, disposed.s.key) }
	if err := disposed.send("request", delivery.Steer); !errors.Is(err, delivery.ErrDisposed) {
		t.Fatalf("got %v", err)
	}
	equal(t, len(disposed.s.steer), 0)
}

func TestInjectsANoticeOnceWithItsSummaryWithoutWakingTheSession(t *testing.T) {
	f := setup()
	for i := 0; i < 2; i++ {
		_ = f.d.Inject(f.ctx, f.s, "notice:settings", `{"settings_saved":"policy"}`, "Settings saved")
	}
	equal(t, len(f.s.in), 1)
	equal(t, f.s.in[0].Source, delivery.Source{Kind: "personal-agent", Form: "notice", Summary: "Settings saved"})
	equal(t, [2]int{len(f.s.steer), len(f.s.followup)}, [2]int{0, 0})
}

func TestTellsWhetherTheSessionHoldsAMessage(t *testing.T) {
	f := setup()
	held, _ := f.d.Holds(f.ctx, f.s, "request")
	equal(t, held, false)
	_ = f.send("request", delivery.Steer)
	held, _ = f.d.Holds(f.ctx, f.s, "request")
	equal(t, held, true)
	f.consume(f.s.steer[0])
	held, _ = f.d.Holds(f.ctx, f.s, "request")
	equal(t, []any{len(f.s.nextStep), held}, []any{0, true})
	restored := setup(f.s.steer[0])
	a, _ := restored.d.Holds(restored.ctx, restored.s, "request")
	b, _ := restored.d.Holds(restored.ctx, restored.s, "other")
	equal(t, []bool{a, b}, []bool{true, false})
}
