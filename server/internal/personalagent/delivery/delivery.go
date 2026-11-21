// Package delivery is a Go translation of dsh-superagent-prototype src/app/delivery.ts: idempotent delivery of
// messages into a person's long-lived Router session, at most once per request ID across live and restored sessions.
package delivery

import (
	"context"
	"errors"
	"sync"
)

// Producer is the source kind of every message this package delivers, so none is mistaken for one a person wrote.
const Producer = "personal-agent"

// Source says who produced a session message; Form is relay or notice.
type Source struct {
	Kind    string `json:"kind"`
	Form    string `json:"form,omitempty"`
	Summary string `json:"summary,omitempty"`
}

// Message is a user message of a session.
type Message struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Source Source `json:"source"`
}

// MessageID is the ID of the message delivered under requestID, the same on every retry.
func MessageID(requestID string) string { return Producer + ":" + requestID }

func deliveryID(m Message) string {
	if m.Source.Kind == Producer {
		return m.ID
	}
	return ""
}

// Session is a live Router session.
type Session interface {
	Key() string
	// Pending is the messages waiting in its inbox for the next step or turn.
	Pending() []Message
	// Steer queues a message for the next step, Followup for the next turn; Inject adds one to history without waking it.
	Steer(Message)
	Followup(Message)
	Inject(Message)
}

// Persistence is the sessions' durable history.
type Persistence interface {
	Flush(ctx context.Context) error
	// Read returns up to length user messages of a session's stored history from offset; an empty page ends it.
	Read(ctx context.Context, session string, offset, length int) ([]Message, error)
}

// Sessions tells the live session of an ID; a disposed one is no longer current.
type Sessions interface {
	Current(key string) Session
}

// Modes.
const (
	Queue = "queue"
	Steer = "steer"
)

// PageSize is how many stored messages one history read returns.
const PageSize = 512

type seen struct {
	mu      sync.Mutex
	ids     map[string]bool
	loaded  bool
	loading chan struct{}
	err     error
}

// Delivery delivers messages at most once per request ID.
type Delivery struct {
	persistence Persistence
	sessions    Sessions
	mu          sync.Mutex
	seen        map[string]*seen
}

// New returns a Delivery on persistence and sessions.
func New(p Persistence, s Sessions) *Delivery {
	return &Delivery{persistence: p, sessions: s, seen: map[string]*seen{}}
}

func (d *Delivery) state(key string) *seen {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.seen[key]
	if s == nil {
		s = &seen{ids: map[string]bool{}}
		d.seen[key] = s
	}
	return s
}

// Observe records a user message a session consumed (SA's session/event listener).
func (d *Delivery) Observe(session Session, m Message) {
	if id := deliveryID(m); id != "" {
		s := d.state(session.Key())
		s.mu.Lock()
		s.ids[id] = true
		s.mu.Unlock()
	}
}

// history loads the session's stored history once; a failed load is retried by the next call.
func (d *Delivery) history(ctx context.Context, session Session) (*seen, error) {
	s := d.state(session.Key())
	for {
		s.mu.Lock()
		if s.loaded {
			s.mu.Unlock()
			return s, nil
		}
		if s.loading != nil {
			wait := s.loading
			s.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			s.mu.Lock()
			err := s.err
			s.mu.Unlock()
			if err != nil {
				return nil, err
			}
			continue
		}
		s.loading = make(chan struct{})
		s.err = nil
		s.mu.Unlock()
		err := d.load(ctx, session.Key(), s)
		s.mu.Lock()
		s.loaded, s.err = err == nil, err
		close(s.loading)
		s.loading = nil
		s.mu.Unlock()
		if err != nil {
			return nil, err
		}
		return s, nil
	}
}

func (d *Delivery) load(ctx context.Context, key string, s *seen) error {
	if err := d.persistence.Flush(ctx); err != nil {
		return err
	}
	for offset := 0; ; {
		page, err := d.persistence.Read(ctx, key, offset, PageSize)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		s.mu.Lock()
		for _, m := range page {
			if id := deliveryID(m); id != "" {
				s.ids[id] = true
			}
		}
		s.mu.Unlock()
		offset += len(page)
	}
}

func pending(session Session, id string) bool {
	for _, m := range session.Pending() {
		if deliveryID(m) == id {
			return true
		}
	}
	return false
}

// Holds reports whether the session holds a request's message: waiting in its inbox, taken, or in stored history.
func (d *Delivery) Holds(ctx context.Context, session Session, requestID string) (bool, error) {
	s, err := d.history(ctx, session)
	if err != nil {
		return false, err
	}
	id := MessageID(requestID)
	s.mu.Lock()
	held := s.ids[id]
	s.mu.Unlock()
	return held || pending(session, id), nil
}

// ErrDisposed is returned when the target session was disposed while history loaded.
var ErrDisposed = errors.New("Delivery target was disposed")

// Send delivers text once to the session's inbox in mode (Queue or Steer), then flushes persistence before the
// producer is acknowledged.
func (d *Delivery) Send(ctx context.Context, session Session, requestID, text, mode string) error {
	return d.send(ctx, session, requestID, text, mode, "")
}

// Inject adds a notice with its summary to the session's history once, without waking it.
func (d *Delivery) Inject(ctx context.Context, session Session, requestID, text, summary string) error {
	return d.send(ctx, session, requestID, text, "", summary)
}

func (d *Delivery) send(ctx context.Context, session Session, requestID, text, mode, summary string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s, err := d.history(ctx, session)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.sessions.Current(session.Key()) != session {
		return ErrDisposed
	}
	id := MessageID(requestID)
	s.mu.Lock()
	held := s.ids[id]
	if !held && !pending(session, id) {
		m := Message{ID: id, Text: text, Source: Source{Kind: Producer, Form: "relay"}}
		switch {
		case mode == "":
			m.Source = Source{Kind: Producer, Form: "notice", Summary: summary}
			s.ids[id] = true
			s.mu.Unlock()
			session.Inject(m)
		case mode == Steer:
			s.mu.Unlock()
			session.Steer(m)
		default:
			s.mu.Unlock()
			session.Followup(m)
		}
	} else {
		s.mu.Unlock()
	}
	return d.persistence.Flush(ctx)
}
