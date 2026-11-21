package core_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// Translated from tests/passes.spec.ts.

type passHarness struct {
	passes   *core.Passes
	health   *core.Health
	evaluate atomic.Int32
	kick     atomic.Int32
	receive  atomic.Int32
	fail     atomic.Bool
	closing  atomic.Bool
	gate     chan struct{}
}

func newPasses() *passHarness {
	h := &passHarness{health: core.NewHealth("starting", "")}
	h.passes = core.NewPasses(core.PassesHost{
		Receive: func(core.Trigger) (string, error) { h.receive.Add(1); return "stored", nil },
		EvaluateDue: func() error {
			h.evaluate.Add(1)
			if h.gate != nil {
				<-h.gate
			}
			if h.fail.CompareAndSwap(true, false) {
				return errors.New("database is locked")
			}
			return nil
		},
		Kick:       func() { h.kick.Add(1) },
		Health:     h.health,
		Configured: func() bool { return true },
		Closing:    h.closing.Load,
	})
	return h
}

func TestPassesFailedPassIsRuntimeErrorClearedByNextSuccess(t *testing.T) {
	h := newPasses()
	h.fail.Store(true)
	h.passes.Request()
	h.passes.Idle()
	if s, d := h.health.Get(); s != "error" || d != "A pass failed: Error: database is locked" {
		t.Fatalf("health: %s %s", s, d)
	}
	h.passes.Request()
	h.passes.Idle()
	if s, d := h.health.Get(); s != "ready" || d != "Orchestrator is ready" {
		t.Fatalf("health: %s %s", s, d)
	}
}

func TestPassesAskedDuringPassRunAsOnePassAfterIt(t *testing.T) {
	h := newPasses()
	h.gate = make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _ = h.passes.Pump() }()
		if i == 0 {
			for h.evaluate.Load() == 0 {
				time.Sleep(time.Millisecond)
			}
		}
	}
	time.Sleep(50 * time.Millisecond)
	close(h.gate)
	wg.Wait()
	if h.evaluate.Load() != 2 || h.kick.Load() != 2 {
		t.Fatalf("passes: evaluate %d kick %d", h.evaluate.Load(), h.kick.Load())
	}
	_ = h.passes.Pump()
	if h.evaluate.Load() != 3 {
		t.Fatalf("evaluate %d", h.evaluate.Load())
	}
}

func TestPassesRefuseTriggerWhileStoppingAndPassForStoredOne(t *testing.T) {
	h := newPasses()
	h.closing.Store(true)
	if _, err := h.passes.Receive(core.Trigger{ID: "t1"}); err == nil || err.Error() != "Orchestrator is stopping" {
		t.Fatalf("closing: %v", err)
	}
	if h.receive.Load() != 0 {
		t.Fatal("engine called while stopping")
	}
	h.closing.Store(false)
	if r, err := h.passes.Receive(core.Trigger{ID: "t1"}); err != nil || r != "stored" {
		t.Fatalf("receive: %s %v", r, err)
	}
	h.passes.Idle()
	if h.evaluate.Load() != 1 {
		t.Fatalf("evaluate %d", h.evaluate.Load())
	}
}

func TestPassesStartNoTimerOnceStopped(t *testing.T) {
	h := newPasses()
	h.passes.Interval = 5 * time.Millisecond
	h.passes.Stop()
	h.passes.Start()
	time.Sleep(50 * time.Millisecond)
	if h.evaluate.Load() != 0 {
		t.Fatal("timer ran after stop")
	}
}
