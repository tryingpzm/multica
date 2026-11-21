// Package effects is a Go translation of dsh-superagent-prototype src/app/effects.ts: a durable queue, in pa_effect,
// of the side effects an Engine command decides on. An effect is queued in the command's transaction and runs after it
// commits; each kind has one handler, a concurrency limit, retries with exponential backoff and a health entry. Rows are
// per personal agent; an ordered kind runs one at a time per agent, oldest first.
package effects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/personalagent/store"
)

// Later says an effect cannot run now, as when processing is paused or every worker slot is taken: the row is due again
// after the kind's initial retry delay, without counting a failure.
type Later struct{ Reason string }

func (l *Later) Error() string { return l.Reason }

// NewLater returns a Later error.
func NewLater(reason string) error { return &Later{Reason: reason} }

// FailuresBeforeError is how many consecutive failures put a kind's health in error.
const FailuresBeforeError = 3

// Health is a kind's health entry.
type Health struct {
	State  string `json:"state"`
	Detail string `json:"detail"`
	At     string `json:"at"`
}

// Handler runs one effect of a personal agent.
type Handler func(ctx context.Context, agentID, key string, payload json.RawMessage) error

// Options tune a kind. Limit is how many may run at once now (0 holds the kind; default 1); Retry the initial and
// maximum backoff in milliseconds (default 2s, 60s); Ordered runs one at a time per agent in queue order, a failure
// holding back the later rows; Quiet keeps failures out of the kind's health.
type Options struct {
	Limit   func() int
	Retry   [2]int64
	Ordered bool
	Quiet   bool
}

type handler struct {
	run Handler
	Options
}

type task struct {
	kind, agentID, key string
	done               chan struct{}
}

type row struct {
	agentID, key      string
	value             []byte
	seq               int64
	generation, tries int
	due               int64
}

// Effects is the queue. Effects run only between Start and Drain.
type Effects struct {
	pool *pgxpool.Pool
	now  func() int64

	mu       sync.Mutex
	handlers map[string]*handler
	order    []string
	health   map[string]Health
	tasks    map[*task]bool
	broken   map[string]bool
	started  bool
	stopped  bool
	dispatch sync.Mutex
}

// New returns a queue on pool; now is the clock in Unix milliseconds (nil: the wall clock).
func New(pool *pgxpool.Pool, now func() int64) *Effects {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &Effects{pool: pool, now: now, handlers: map[string]*handler{}, health: map[string]Health{}, tasks: map[*task]bool{}, broken: map[string]bool{}}
}

func iso(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

// Enqueue upserts an effect in s's transaction, coalescing with a waiting row of the same kind and key while keeping its
// place in the queue; it runs once the transaction commits.
func (e *Effects) Enqueue(s *store.Store, kind, key string, payload any) {
	value, err := json.Marshal(payload)
	if err != nil {
		panic(err)
	}
	now := e.now()
	if _, err := s.Q().Exec(s.Context(), `INSERT INTO pa_effect (agent_id, kind, key, value, due, since) VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (agent_id, kind, key) DO UPDATE SET value = EXCLUDED.value, generation = pa_effect.generation + 1, attempts = 0, error = '', due = EXCLUDED.due`,
		s.AgentID(), kind, key, value, now, iso(now)); err != nil {
		panic(fmt.Errorf("personal agent effects: %w", err))
	}
	s.AfterCommit(e.Kick)
}

// Handle registers the handler of a kind.
func (e *Effects) Handle(kind string, run Handler, options Options) {
	e.mu.Lock()
	if _, ok := e.handlers[kind]; ok {
		e.mu.Unlock()
		panic(fmt.Sprintf("The %s effects already have a handler", kind))
	}
	if options.Limit == nil {
		options.Limit = func() int { return 1 }
	}
	if options.Retry == [2]int64{} {
		options.Retry = [2]int64{2000, 60_000}
	}
	e.handlers[kind] = &handler{run: run, Options: options}
	e.order = append(e.order, kind)
	e.mu.Unlock()
	e.report(kind)
	e.Kick()
}

// Pending reports whether an effect of kind waits or runs, of the agent and key when key is set.
func (e *Effects) Pending(agentID, kind, key string) bool {
	var found bool
	err := e.pool.QueryRow(context.Background(), "SELECT EXISTS (SELECT 1 FROM pa_effect WHERE agent_id = $1 AND kind = $2 AND ($3 = '' OR key = $3))", agentID, kind, key).Scan(&found)
	if err != nil {
		panic(err)
	}
	return found
}

// Cancel drops a queued effect in s's transaction; an attempt already running finishes normally.
func (e *Effects) Cancel(s *store.Store, kind, key string) {
	if _, err := s.Q().Exec(s.Context(), "DELETE FROM pa_effect WHERE agent_id = $1 AND kind = $2 AND key = $3", s.AgentID(), kind, key); err != nil {
		panic(fmt.Errorf("personal agent effects: %w", err))
	}
	s.AfterCommit(func() { e.report(kind) })
}

// Health returns a kind's health entry.
func (e *Effects) Health(kind string) Health {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.health[kind]
}

// Start lets effects run.
func (e *Effects) Start() {
	e.mu.Lock()
	e.started = true
	e.mu.Unlock()
	e.Kick()
}

// Kick schedules a dispatch.
func (e *Effects) Kick() { go e.run() }

// Run kicks the queue every interval until ctx ends, so rows that become due later run.
func (e *Effects) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.run()
		}
	}
}

// Idle returns once no effect of kind (of any kind when "") runs and none can start.
func (e *Effects) Idle(kind string) {
	for {
		e.run()
		e.mu.Lock()
		var running []chan struct{}
		for t := range e.tasks {
			if kind == "" || t.kind == kind {
				running = append(running, t.done)
			}
		}
		e.mu.Unlock()
		if len(running) == 0 {
			return
		}
		for _, done := range running {
			<-done
		}
	}
}

// Drain stops starting effects and waits for the running ones; waiting rows stay for the next Start.
func (e *Effects) Drain() {
	e.mu.Lock()
	e.stopped = true
	var running []chan struct{}
	for t := range e.tasks {
		running = append(running, t.done)
	}
	e.mu.Unlock()
	for _, done := range running {
		<-done
	}
}

func (e *Effects) run() {
	e.dispatch.Lock()
	defer e.dispatch.Unlock()
	e.mu.Lock()
	if !e.started || e.stopped {
		e.mu.Unlock()
		return
	}
	kinds := append([]string(nil), e.order...)
	e.mu.Unlock()
	for _, kind := range kinds {
		e.dispatchKind(kind)
	}
}

func (e *Effects) dispatchKind(kind string) {
	defer func() {
		if r := recover(); r != nil {
			e.failed(kind, fmt.Errorf("%v", r))
		}
	}()
	e.mu.Lock()
	h := e.handlers[kind]
	running := map[string]bool{}
	agents := map[string]bool{}
	for t := range e.tasks {
		if t.kind == kind {
			running[t.agentID+"\x00"+t.key] = true
			agents[t.agentID] = true
		}
	}
	e.mu.Unlock()
	now := e.now()
	var rows []row
	var err error
	if h.Ordered {
		// Each agent's ordered kind blocks behind its oldest row, even one not yet due.
		rows, err = e.rows(`SELECT DISTINCT ON (agent_id) agent_id::text, key, value, seq, generation, attempts, due FROM pa_effect WHERE kind = $1
			ORDER BY agent_id, seq`, kind)
	} else {
		rows, err = e.rows(`SELECT agent_id::text, key, value, seq, generation, attempts, due FROM pa_effect WHERE kind = $1 AND due <= $2
			ORDER BY seq LIMIT $3`, kind, now, 16+len(running))
	}
	if err != nil {
		e.failed(kind, err)
		return
	}
	ready := rows[:0]
	for _, r := range rows {
		if r.due <= now && !running[r.agentID+"\x00"+r.key] && !(h.Ordered && agents[r.agentID]) {
			ready = append(ready, r)
		}
	}
	room := 0
	if len(ready) > 0 {
		room = h.Limit() - len(running)
	}
	for _, r := range ready {
		if room <= 0 {
			break
		}
		room--
		e.start(kind, h, r)
	}
	e.mu.Lock()
	wasBroken := e.broken[kind]
	delete(e.broken, kind)
	e.mu.Unlock()
	if wasBroken {
		e.report(kind)
	}
}

func (e *Effects) rows(sql string, args ...any) ([]row, error) {
	rs, err := e.pool.Query(context.Background(), sql, args...)
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.agentID, &r.key, &r.value, &r.seq, &r.generation, &r.tries, &r.due); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rs.Err()
}

func (e *Effects) start(kind string, h *handler, r row) {
	t := &task{kind: kind, agentID: r.agentID, key: r.key, done: make(chan struct{})}
	e.mu.Lock()
	e.tasks[t] = true
	e.mu.Unlock()
	go func() {
		defer func() {
			if p := recover(); p != nil {
				e.failed(kind, fmt.Errorf("%v", p))
			}
			e.mu.Lock()
			delete(e.tasks, t)
			e.mu.Unlock()
			close(t.done)
			e.Kick()
		}()
		e.attempt(kind, h, r)
	}()
}

func (e *Effects) attempt(kind string, h *handler, r row) {
	failure := func() (err error) {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("%v", p)
			}
		}()
		return h.run(context.Background(), r.agentID, r.key, r.value)
	}()
	// Match seq and generation, so a row replaced or queued again while this ran is not touched.
	own := "agent_id = $1 AND kind = $2 AND key = $3 AND seq = $4 AND generation = $5"
	args := []any{r.agentID, kind, r.key, r.seq, r.generation}
	ctx := context.Background()
	var later *Later
	e.mu.Lock()
	stopped := e.stopped
	e.mu.Unlock()
	switch {
	case failure == nil:
		_, err := e.pool.Exec(ctx, "DELETE FROM pa_effect WHERE "+own, args...)
		if err != nil {
			e.failed(kind, err)
		}
	case stopped:
		// A row shutdown cut short runs again after the next Start.
		return
	case errors.As(failure, &later):
		_, err := e.pool.Exec(ctx, "UPDATE pa_effect SET due = $6 WHERE "+own, append(args, e.now()+h.Retry[0])...)
		if err != nil {
			e.failed(kind, err)
		}
	default:
		exp := r.tries
		if exp > 16 {
			exp = 16
		}
		delay := h.Retry[0] << exp
		if delay > h.Retry[1] || delay <= 0 {
			delay = h.Retry[1]
		}
		text := failure.Error()
		if len(text) > 1000 {
			text = text[:1000]
		}
		_, err := e.pool.Exec(ctx, "UPDATE pa_effect SET attempts = attempts + 1, error = $6, due = $7 WHERE "+own, append(args, text, e.now()+delay)...)
		if err != nil {
			e.failed(kind, err)
		}
	}
	e.report(kind)
}

func (e *Effects) failed(kind string, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.broken[kind] = true
	e.health[kind] = Health{State: "error", Detail: "The effect queue failed: " + err.Error(), At: iso(e.now())}
}

func (e *Effects) report(kind string) {
	var waiting int
	var since, lastError *string
	var attempts *int
	err := e.pool.QueryRow(context.Background(), `SELECT COUNT(*), MIN(since), MAX(attempts),
		(SELECT error FROM pa_effect WHERE kind = $1 AND attempts > 0 ORDER BY seq DESC LIMIT 1) FROM pa_effect WHERE kind = $1`, kind).
		Scan(&waiting, &since, &attempts, &lastError)
	if err != nil {
		e.failed(kind, err)
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	h := e.handlers[kind]
	detail := "Nothing waiting"
	if waiting > 0 {
		detail = strconv.Itoa(waiting) + " waiting since " + deref(since)
	}
	if attempts != nil && *attempts >= FailuresBeforeError && (h == nil || !h.Quiet) {
		e.health[kind] = Health{State: "error", Detail: detail + "; the last failure: " + deref(lastError), At: iso(e.now())}
		return
	}
	e.health[kind] = Health{State: "ready", Detail: detail, At: iso(e.now())}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
