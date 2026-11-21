package core

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Health is a component's health entry: "starting", "ready" or "error", with a detail.
type Health struct {
	mu     sync.Mutex
	state  string
	detail string
}

// NewHealth returns a Health in state with detail.
func NewHealth(state, detail string) *Health { return &Health{state: state, detail: detail} }

// Get returns the state and detail.
func (h *Health) Get() (string, string) { h.mu.Lock(); defer h.mu.Unlock(); return h.state, h.detail }

func (h *Health) set(state, detail string) {
	h.mu.Lock()
	h.state, h.detail = state, detail
	h.mu.Unlock()
}

// PassesHost is what Passes drives (SA PassesHost).
type PassesHost struct {
	Receive     func(Trigger) (string, error)
	EvaluateDue func() error
	// Kick starts the due effects, such as outbox deliveries.
	Kick       func()
	Health     *Health
	Configured func() bool
	Closing    func() bool
}

// ErrStopping refuses a Trigger while the agent stops.
var ErrStopping = errors.New("Orchestrator is stopping")

// Passes schedules and serializes orchestration passes that kick due effects and advance Engine evaluations
// (SA src/app/passes.ts). Callers that ask for a pass while one runs share a single pass after it.
type Passes struct {
	host     PassesHost
	Interval time.Duration

	mu      sync.Mutex
	cond    *sync.Cond
	running bool
	queued  bool
	started int
	done    int
	errs    map[int]error
	stopped bool
	ticker  *time.Ticker
	quit    chan struct{}
	flight  sync.WaitGroup
}

// NewPasses returns Passes over host, ticking every 2 seconds once started.
func NewPasses(host PassesHost) *Passes {
	p := &Passes{host: host, Interval: 2 * time.Second, errs: map[int]error{}, quit: make(chan struct{})}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// Start requests a pass on every tick, unless Passes stopped already.
func (p *Passes) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped || p.ticker != nil {
		return
	}
	p.ticker = time.NewTicker(p.Interval)
	go func(ticks <-chan time.Time) {
		for {
			select {
			case <-ticks:
				p.Request()
			case <-p.quit:
				return
			}
		}
	}(p.ticker.C)
}

// Stop stops the timer and waits for the passes in flight.
func (p *Passes) Stop() {
	p.mu.Lock()
	if !p.stopped {
		p.stopped = true
		close(p.quit)
		if p.ticker != nil {
			p.ticker.Stop()
		}
	}
	p.mu.Unlock()
	p.Idle()
}

// Receive refuses a Trigger while the agent stops, before the Engine stores anything, and requests a pass for one it stored.
func (p *Passes) Receive(t Trigger) (string, error) {
	if p.host.Closing() {
		return "", ErrStopping
	}
	result, err := p.host.Receive(t)
	if err == nil && result == "stored" {
		p.Request()
	}
	return result, err
}

// Request runs a pass in the background and reports its outcome on Health: an error, or ready once a pass after an
// error succeeds.
func (p *Passes) Request() {
	p.flight.Add(1)
	go func() {
		defer p.flight.Done()
		err := p.Pump()
		h := p.host.Health
		if h == nil {
			return
		}
		if err != nil {
			h.set("error", fmt.Sprintf("A pass failed: Error: %v", err))
			return
		}
		if state, _ := h.Get(); state == "error" && !p.isStopped() {
			h.set("ready", "Orchestrator is ready")
		}
	}()
}

func (p *Passes) isStopped() bool { p.mu.Lock(); defer p.mu.Unlock(); return p.stopped }

// Idle waits until no requested pass runs.
func (p *Passes) Idle() {
	p.flight.Wait()
	p.mu.Lock()
	for p.running {
		p.cond.Wait()
	}
	p.mu.Unlock()
}

// Pump runs one pass and returns its error; callers that arrive while a pass runs share one pass after it. Nothing runs
// while the agent waits for setup or stops.
func (p *Passes) Pump() error {
	if !p.host.Configured() || p.host.Closing() {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	target := p.started + 1
	if p.running {
		p.queued = true
	} else {
		p.running = true
		go p.run()
	}
	for p.done < target {
		p.cond.Wait()
	}
	return p.errs[target]
}

func (p *Passes) run() {
	p.mu.Lock()
	for {
		p.started++
		n := p.started
		p.queued = false
		p.mu.Unlock()
		err := p.once()
		p.mu.Lock()
		p.done, p.errs[n] = n, err
		delete(p.errs, n-8)
		p.cond.Broadcast()
		if !p.queued {
			break
		}
	}
	p.running = false
	p.cond.Broadcast()
	p.mu.Unlock()
}

func (p *Passes) once() (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	if p.host.Kick != nil {
		p.host.Kick()
	}
	if p.host.Closing() {
		return nil
	}
	// While processing is paused, the Engine only records the pause on active Evals so they do not expire.
	return p.host.EvaluateDue()
}
