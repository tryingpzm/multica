// Package store is the PostgreSQL implementation of the personal agent's core.Repositories (SA src/app/store.ts):
// aggregates keep SA's JSON structure, Atomic is one transaction holding the agent's advisory lock, and the key columns
// and projection tables (pa_eval, pa_subscription, pa_assignment, pa_report) are rewritten with each save.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store is one personal agent's Repositories. It is not safe for concurrent use: give each goroutine its own Store; the
// advisory lock serializes Stores of the same agent across goroutines and server replicas.
type Store struct {
	ctx     context.Context
	pool    *pgxpool.Pool
	agentID string
	tx      pgx.Tx
	after   []func()
}

// New returns the Store of agentID.
func New(ctx context.Context, pool *pgxpool.Pool, agentID string) *Store {
	return &Store{ctx: ctx, pool: pool, agentID: agentID}
}

// AgentID is the personal agent whose state the Store holds.
func (s *Store) AgentID() string { return s.agentID }

func (s *Store) q() querier {
	if s.tx != nil {
		return s.tx
	}
	return s.pool
}

func check(err error) {
	if err != nil {
		panic(fmt.Errorf("personal agent store: %w", err))
	}
}

// Atomic runs work in one transaction that holds the agent's advisory lock; a panic rolls it back and propagates.
// Nested calls join the outer transaction.
func (s *Store) Atomic(work func()) {
	if s.tx != nil {
		work()
		return
	}
	tx, err := s.pool.Begin(s.ctx)
	check(err)
	s.tx = tx
	committed := false
	defer func() {
		s.tx = nil
		after := s.after
		s.after = nil
		if !committed {
			_ = tx.Rollback(context.WithoutCancel(s.ctx))
			return
		}
		for _, fn := range after {
			fn()
		}
	}()
	_, err = tx.Exec(s.ctx, "SELECT pg_advisory_xact_lock(hashtextextended('personal_agent:' || $1, 0))", s.agentID)
	check(err)
	work()
	check(tx.Commit(s.ctx))
	committed = true
}

// Querier is what the Store reads and writes with: its transaction inside Atomic, else the pool.
type Querier = querier

// Q returns the Store's current Querier, so a part outside the package (the effect queue) writes in its transaction.
func (s *Store) Q() Querier { return s.q() }

// Context is the Store's context.
func (s *Store) Context() context.Context { return s.ctx }

// Pool is the pool the Store opens transactions on.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// AfterCommit runs fn once the current Atomic commits, and never when it rolls back; outside Atomic it runs fn now.
func (s *Store) AfterCommit(fn func()) {
	if s.tx == nil {
		fn()
		return
	}
	s.after = append(s.after, fn)
}

func (s *Store) Triggers() core.TriggerRepo   { return triggers{s} }
func (s *Store) Threads() core.ThreadRepo     { return threads{s} }
func (s *Store) Tickets() core.TicketRepo     { return tickets{s} }
func (s *Store) Decisions() core.DecisionRepo { return decisions{s} }

func encode(v any) []byte {
	data, err := json.Marshal(v)
	check(err)
	return data
}

func one[T any](s *Store, sql string, args ...any) *T {
	var data []byte
	err := s.q().QueryRow(s.ctx, sql, args...).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && data == nil) {
		return nil
	}
	check(err)
	var v T
	check(json.Unmarshal(data, &v))
	return &v
}

func many[T any](s *Store, sql string, args ...any) []T {
	rows, err := s.q().Query(s.ctx, sql, args...)
	check(err)
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		var data []byte
		check(rows.Scan(&data))
		var v T
		check(json.Unmarshal(data, &v))
		out = append(out, v)
	}
	check(rows.Err())
	return out
}

type triggers struct{ s *Store }

func (r triggers) Get(id string) *core.Trigger {
	return one[core.Trigger](r.s, "SELECT value FROM pa_trigger WHERE agent_id = $1 AND id = $2", r.s.agentID, id)
}

func (r triggers) Save(t core.Trigger) {
	s := r.s
	_, err := s.q().Exec(s.ctx, `INSERT INTO pa_trigger (agent_id, id, at, value) VALUES ($1, $2, $3, $4)
		ON CONFLICT (agent_id, id) DO UPDATE SET at = EXCLUDED.at, value = EXCLUDED.value`, s.agentID, t.ID, t.At, encode(t))
	check(err)
	for _, ref := range t.Threads {
		_, err := s.q().Exec(s.ctx, `INSERT INTO pa_trigger_thread (agent_id, thread_key, trigger_id, at, human) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT DO NOTHING`, s.agentID, core.ThreadKey(ref), t.ID, t.At, t.Human)
		check(err)
	}
}

func (r triggers) Disposition(id string) *core.TriggerDisposition {
	return one[core.TriggerDisposition](r.s, "SELECT disposition FROM pa_trigger WHERE agent_id = $1 AND id = $2", r.s.agentID, id)
}

func (r triggers) SaveDisposition(id string, d core.TriggerDisposition) {
	tag, err := r.s.q().Exec(r.s.ctx, "UPDATE pa_trigger SET disposition = $3 WHERE agent_id = $1 AND id = $2", r.s.agentID, id, encode(d))
	check(err)
	if tag.RowsAffected() == 0 {
		check(fmt.Errorf("disposition of unknown trigger %s", id))
	}
}

type threads struct{ s *Store }

func (r threads) Get(key string) *core.Thread {
	return one[core.Thread](r.s, "SELECT value FROM pa_thread WHERE agent_id = $1 AND key = $2", r.s.agentID, key)
}

func (r threads) Save(t core.Thread) {
	s := r.s
	_, err := s.q().Exec(s.ctx, `INSERT INTO pa_thread (agent_id, key, active, value) VALUES ($1, $2, $3, $4)
		ON CONFLICT (agent_id, key) DO UPDATE SET active = EXCLUDED.active, value = EXCLUDED.value`, s.agentID, t.Key, core.ActiveThread(t), encode(t))
	check(err)
	// Projections: the Thread's Subscriptions and the Eval it has in progress.
	_, err = s.q().Exec(s.ctx, "DELETE FROM pa_subscription WHERE agent_id = $1 AND thread_key = $2", s.agentID, t.Key)
	check(err)
	for _, id := range t.Subscriptions {
		context := false
		for _, c := range t.Context {
			context = context || c == id
		}
		_, err = s.q().Exec(s.ctx, "INSERT INTO pa_subscription (agent_id, thread_key, ticket_id, context) VALUES ($1, $2, $3, $4)", s.agentID, t.Key, id, context)
		check(err)
	}
	_, err = s.q().Exec(s.ctx, "DELETE FROM pa_eval WHERE agent_id = $1 AND thread_key = $2", s.agentID, t.Key)
	check(err)
	if e := t.Evaluating; e != nil {
		_, err = s.q().Exec(s.ctx, `INSERT INTO pa_eval (agent_id, id, thread_key, strength, trigger_ids, started_at, deadline) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (agent_id, id) DO UPDATE SET thread_key = EXCLUDED.thread_key, trigger_ids = EXCLUDED.trigger_ids, deadline = EXCLUDED.deadline`,
			s.agentID, e.ID, t.Key, e.Strength, e.TriggerIDs, e.StartedAt, e.Deadline)
		check(err)
	}
}

func (r threads) List() []core.Thread {
	return many[core.Thread](r.s, "SELECT value FROM pa_thread WHERE agent_id = $1 ORDER BY seq", r.s.agentID)
}

func (r threads) Active() []core.Thread {
	return many[core.Thread](r.s, "SELECT value FROM pa_thread WHERE agent_id = $1 AND active ORDER BY seq", r.s.agentID)
}

type tickets struct{ s *Store }

func (r tickets) Get(id string) *core.Ticket {
	return one[core.Ticket](r.s, "SELECT value FROM pa_ticket WHERE agent_id = $1 AND id = $2", r.s.agentID, id)
}

func (r tickets) Save(t core.Ticket) {
	s := r.s
	_, err := s.q().Exec(s.ctx, `INSERT INTO pa_ticket (agent_id, id, title, open, active, created_at, updated_at, value) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (agent_id, id) DO UPDATE SET title = EXCLUDED.title, open = EXCLUDED.open, active = EXCLUDED.active,
		updated_at = EXCLUDED.updated_at, value = EXCLUDED.value`,
		s.agentID, t.ID, t.Title, core.LiveTicket(t), core.ActiveTicket(t), t.CreatedAt, t.UpdatedAt, encode(t))
	check(err)
	for _, a := range t.Assignments {
		_, err = s.q().Exec(s.ctx, `INSERT INTO pa_assignment (agent_id, id, ticket_id, state, created_at, started_at, ended_at, ended_reason)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), NULLIF($8, ''))
			ON CONFLICT (agent_id, id) DO UPDATE SET state = EXCLUDED.state, started_at = EXCLUDED.started_at, ended_at = EXCLUDED.ended_at,
			ended_reason = EXCLUDED.ended_reason`, s.agentID, a.ID, t.ID, a.State, a.CreatedAt, a.StartedAt, a.EndedAt, a.EndedReason)
		check(err)
		if a.Report != nil {
			_, err = s.q().Exec(s.ctx, `INSERT INTO pa_report (agent_id, assignment_id, at, ticket_id, state, summary) VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT DO NOTHING`, s.agentID, a.ID, a.Report.At, t.ID, a.Report.State, a.Report.Summary)
			check(err)
		}
	}
}

// List is newest first, as SA's Store orders tickets.
func (r tickets) List() []core.Ticket {
	return many[core.Ticket](r.s, "SELECT value FROM pa_ticket WHERE agent_id = $1 ORDER BY seq DESC", r.s.agentID)
}

func (r tickets) Open() []core.Ticket {
	return many[core.Ticket](r.s, "SELECT value FROM pa_ticket WHERE agent_id = $1 AND open ORDER BY seq DESC", r.s.agentID)
}

func (r tickets) Active() []core.Ticket {
	return many[core.Ticket](r.s, "SELECT value FROM pa_ticket WHERE agent_id = $1 AND active ORDER BY seq DESC", r.s.agentID)
}

type decisions struct{ s *Store }

func (r decisions) Get(id string) *core.Decision {
	return one[core.Decision](r.s, "SELECT value FROM pa_decision WHERE agent_id = $1 AND id = $2", r.s.agentID, id)
}

func (r decisions) Save(d core.Decision) {
	_, err := r.s.q().Exec(r.s.ctx, `INSERT INTO pa_decision (agent_id, id, thread_key, kind, ticket_id, value) VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6)
		ON CONFLICT (agent_id, id) DO UPDATE SET kind = EXCLUDED.kind, ticket_id = EXCLUDED.ticket_id, value = EXCLUDED.value`,
		r.s.agentID, d.ID, d.ThreadKey, d.Kind, d.TicketID, encode(d))
	check(err)
}

func (r decisions) ByThread(key string) []core.Decision {
	return many[core.Decision](r.s, "SELECT value FROM pa_decision WHERE agent_id = $1 AND thread_key = $2 ORDER BY seq", r.s.agentID, key)
}
