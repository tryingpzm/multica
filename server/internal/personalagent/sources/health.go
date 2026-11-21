package sources

import (
	"fmt"
	"sync"
)

// Health is one health entry's state.
type Health struct {
	State  string `json:"state"` // starting | ready | paused | error
	Detail string `json:"detail"`
}

// HealthBoard holds every health entry; each entry has one owner, which claims it (SA infra/health.ts).
type HealthBoard struct {
	mu      sync.Mutex
	entries map[string]*HealthEntry
	all     map[string]Health
}

// HealthEntry is a claimed entry; its owner reports through it until it is released.
type HealthEntry struct {
	board    *HealthBoard
	name     string
	released bool
}

// NewHealthBoard returns an empty board.
func NewHealthBoard() *HealthBoard { return &HealthBoard{entries: map[string]*HealthEntry{}} }

// Claim takes an entry with its initial state; it fails for an entry another owner holds.
func (b *HealthBoard) Claim(name string, initial Health) (*HealthEntry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.entries[name]; ok {
		return nil, fmt.Errorf("health entry %s is already claimed", name)
	}
	e := &HealthEntry{board: b, name: name}
	b.entries[name] = e
	b.set(name, initial)
	return e, nil
}

func (b *HealthBoard) set(name string, h Health) {
	if b.all == nil {
		b.all = map[string]Health{}
	}
	b.all[name] = h
}

// Report sets the entry's state; a released entry's report does nothing.
func (e *HealthEntry) Report(h Health) {
	e.board.mu.Lock()
	defer e.board.mu.Unlock()
	if e.released || e.board.entries[e.name] != e {
		return
	}
	e.board.set(e.name, h)
}

// Release gives the entry up.
func (e *HealthEntry) Release() {
	e.board.mu.Lock()
	defer e.board.mu.Unlock()
	if e.released {
		return
	}
	e.released = true
	if e.board.entries[e.name] == e {
		delete(e.board.entries, e.name)
		delete(e.board.all, e.name)
	}
}

// All returns every entry's state.
func (b *HealthBoard) All() map[string]Health {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]Health{}
	for k, v := range b.all {
		out[k] = v
	}
	return out
}
