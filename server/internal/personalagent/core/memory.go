package core

import "encoding/json"

func clone[T any](value T) T {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}

func clonePtr[T any](value *T) *T {
	if value == nil {
		return nil
	}
	v := clone(*value)
	return &v
}

// ordered is an insertion-ordered map, like a JS Map.
type ordered[T any] struct {
	keys   []string
	values map[string]T
}

func newOrdered[T any]() *ordered[T] { return &ordered[T]{values: map[string]T{}} }

func (o *ordered[T]) get(id string) (T, bool) { v, ok := o.values[id]; return v, ok }
func (o *ordered[T]) set(id string, value T) {
	if _, ok := o.values[id]; !ok {
		o.keys = append(o.keys, id)
	}
	o.values[id] = value
}
func (o *ordered[T]) list() []T {
	out := make([]T, 0, len(o.keys))
	for _, k := range o.keys {
		out = append(out, o.values[k])
	}
	return out
}
func (o *ordered[T]) copy() *ordered[T] {
	c := newOrdered[T]()
	c.keys = append(c.keys, o.keys...)
	for k, v := range o.values {
		c.values[k] = v
	}
	return c
}

// MemoryRepositories is the in-memory Repositories port, backed by cloned entries so caller mutations do not affect
// stored state; Atomic rolls back to a snapshot.
type MemoryRepositories struct {
	triggerRows  *ordered[Trigger]
	dispositions *ordered[TriggerDisposition]
	threadRows   *ordered[Thread]
	ticketRows   *ordered[Ticket]
	decisionRows *ordered[Decision]
}

// NewMemoryRepositories returns empty in-memory repositories.
func NewMemoryRepositories() *MemoryRepositories {
	return &MemoryRepositories{newOrdered[Trigger](), newOrdered[TriggerDisposition](), newOrdered[Thread](), newOrdered[Ticket](), newOrdered[Decision]()}
}

func (m *MemoryRepositories) Atomic(work func()) {
	snapshot := *m
	snapshot.triggerRows, snapshot.dispositions, snapshot.threadRows = m.triggerRows.copy(), m.dispositions.copy(), m.threadRows.copy()
	snapshot.ticketRows, snapshot.decisionRows = m.ticketRows.copy(), m.decisionRows.copy()
	defer func() {
		if r := recover(); r != nil {
			*m = snapshot
			panic(r)
		}
	}()
	work()
}

func (m *MemoryRepositories) Triggers() TriggerRepo   { return memTriggers{m} }
func (m *MemoryRepositories) Threads() ThreadRepo     { return memThreads{m} }
func (m *MemoryRepositories) Tickets() TicketRepo     { return memTickets{m} }
func (m *MemoryRepositories) Decisions() DecisionRepo { return memDecisions{m} }

type memTriggers struct{ m *MemoryRepositories }

func (r memTriggers) Get(id string) *Trigger {
	v, ok := r.m.triggerRows.get(id)
	if !ok {
		return nil
	}
	return clonePtr(&v)
}
func (r memTriggers) Save(t Trigger) { r.m.triggerRows.set(t.ID, clone(t)) }
func (r memTriggers) Disposition(id string) *TriggerDisposition {
	v, ok := r.m.dispositions.get(id)
	if !ok {
		return nil
	}
	return clonePtr(&v)
}
func (r memTriggers) SaveDisposition(id string, v TriggerDisposition) {
	r.m.dispositions.set(id, clone(v))
}

type memThreads struct{ m *MemoryRepositories }

func (r memThreads) Get(key string) *Thread {
	v, ok := r.m.threadRows.get(key)
	if !ok {
		return nil
	}
	return clonePtr(&v)
}
func (r memThreads) Save(t Thread)  { r.m.threadRows.set(t.Key, clone(t)) }
func (r memThreads) List() []Thread { return clone(r.m.threadRows.list()) }
func (r memThreads) Active() []Thread {
	out := []Thread{}
	for _, t := range r.List() {
		if ActiveThread(t) {
			out = append(out, t)
		}
	}
	return out
}

type memTickets struct{ m *MemoryRepositories }

func (r memTickets) Get(id string) *Ticket {
	v, ok := r.m.ticketRows.get(id)
	if !ok {
		return nil
	}
	return clonePtr(&v)
}
func (r memTickets) Save(t Ticket) { r.m.ticketRows.set(t.ID, clone(t)) }

// List is newest first, matching the Store's order.
func (r memTickets) List() []Ticket {
	all := clone(r.m.ticketRows.list())
	out := make([]Ticket, 0, len(all))
	for i := len(all) - 1; i >= 0; i-- {
		out = append(out, all[i])
	}
	return out
}
func (r memTickets) Open() []Ticket {
	out := []Ticket{}
	for _, t := range r.List() {
		if LiveTicket(t) {
			out = append(out, t)
		}
	}
	return out
}
func (r memTickets) Active() []Ticket {
	out := []Ticket{}
	for _, t := range r.List() {
		if ActiveTicket(t) {
			out = append(out, t)
		}
	}
	return out
}

type memDecisions struct{ m *MemoryRepositories }

func (r memDecisions) Get(id string) *Decision {
	v, ok := r.m.decisionRows.get(id)
	if !ok {
		return nil
	}
	return clonePtr(&v)
}
func (r memDecisions) Save(d Decision) { r.m.decisionRows.set(d.ID, clone(d)) }
func (r memDecisions) ByThread(key string) []Decision {
	out := []Decision{}
	for _, d := range r.m.decisionRows.list() {
		if d.ThreadKey == key {
			out = append(out, clone(d))
		}
	}
	return out
}
