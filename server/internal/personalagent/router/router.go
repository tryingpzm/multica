// Package router is each personal agent's Router (SA's main Session): one long-lived session the platform hosts, which
// candidates and worker reports reach through the durable effect queue, and whose decisions come back through the
// multica ticket commands. It implements the Engine's Assessor port (SA src/app/assessment.ts): an Eval's candidate
// card is delivered once, the Eval's deadline starts once the session received it, and bounded thread context is served
// while the Eval runs.
package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/multica-ai/multica/server/internal/personalagent/cards"
	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/effects"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
)

// Session is the Router's long-lived session: the system boundary messages for it cross.
type Session interface {
	// Deliver posts text to the agent's Router session at most once per requestID and wakes it.
	Deliver(ctx context.Context, agent store.Agent, requestID, text string) error
	// Received reports whether a Router run has started on what requestID posted; until then it waits in the queue.
	Received(ctx context.Context, agent store.Agent, requestID string) (bool, error)
}

// Opener opens one personal agent for a unit of work.
type Opener func(ctx context.Context, agentID string) (*host.Agent, error)

// Options configure the Router.
type Options struct {
	Effects *effects.Effects
	Session Session
	Open    Opener
	Paused  func(agentID string) bool
	// ThreadContext reads a Trigger's thread; nil reads it through the agent's Registry.
	ThreadContext func(a *host.Agent, t core.Trigger) *sources.TriggerContext
	// MaxPages bounds the pages of an Eval's initial window.
	MaxPages int
}

// Limits of the initial window, as SA's.
const (
	TriggerChars = 4000
	NearChars    = 1200
	WindowChars  = 400
	StubChars    = 50
)

const (
	finished  = "This assessment has finished: it was decided, timed out or restarted"
	explicit  = "This is an explicit request, which is routed instead of assessed: use multica ticket route with its trigger_id and kind accept, attach, stop or ignore"
	noContext = "This Source gives no Thread context; assess the Trigger's own text and the batch."
	noEval    = "No assessment is in progress for this trigger; it was already decided, timed out, or never reached assessment"
)

type evalReads struct {
	mu       sync.Mutex
	messages map[string]string
	cursors  map[string]bool
	pages    map[string]string
	context  *sources.AdmissionContextResult
	err      string
	loaded   bool
}

// Router serves every personal agent's Router session.
type Router struct {
	o     Options
	mu    sync.Mutex
	evals map[string]*evalReads
}

// New returns a Router and registers its assessment and relay effects.
func New(o Options) *Router {
	if o.Paused == nil {
		o.Paused = func(string) bool { return false }
	}
	if o.ThreadContext == nil {
		o.ThreadContext = func(a *host.Agent, t core.Trigger) *sources.TriggerContext { return a.Registry.ThreadContext(t) }
	}
	if o.MaxPages == 0 {
		o.MaxPages = 10
	}
	r := &Router{o: o, evals: map[string]*evalReads{}}
	o.Effects.Handle("assessment", r.assessment, effects.Options{Limit: func() int { return 4 }})
	o.Effects.Handle("relay", r.relay, effects.Options{Ordered: true, Limit: func() int { return 4 }})
	return r
}

// For returns the core.Assessor port of one agent's Engine, which queues its work on s.
func (r *Router) For(_ store.Agent, s *store.Store) core.Assessor { return port{r, s} }

type port struct {
	r *Router
	s *store.Store
}

func (p port) Request(c core.Candidate) {
	p.r.o.Effects.Enqueue(p.s, "assessment", c.EvalID, map[string]string{"triggerId": c.Triggers[0].ID})
}

func (p port) Release(evalID string) { p.r.Release(evalID) }

// Release drops an Eval's reads once it finished.
func (r *Router) Release(evalID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.evals, evalID)
}

// Kept is how many Evals hold reads.
func (r *Router) Kept() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.evals)
}

// Relay queues a message for the agent's Router session in s's transaction (executor.Relayer).
func (r *Router) Relay(s *store.Store, relay cards.Relay) {
	r.o.Effects.Enqueue(s, "relay", relay.ID, map[string]string{"text": relay.Text})
}

func (r *Router) relay(ctx context.Context, agentID, key string, payload json.RawMessage) error {
	if r.o.Paused(agentID) {
		return effects.NewLater("Processing is paused")
	}
	var p struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	a, err := r.o.Open(ctx, agentID)
	if err != nil {
		return err
	}
	return r.o.Session.Deliver(ctx, a.Agent, key, p.Text)
}

// assessment delivers an Eval's candidate card; the Eval's deadline starts once a Router run starts on it.
func (r *Router) assessment(ctx context.Context, agentID, evalID string, payload json.RawMessage) error {
	if r.o.Paused(agentID) {
		return effects.NewLater("Processing is paused")
	}
	var p struct {
		TriggerID string `json:"triggerId"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return err
	}
	a, err := r.o.Open(ctx, agentID)
	if err != nil {
		return err
	}
	c := a.Engine.Candidate(p.TriggerID)
	if c == nil || c.EvalID != evalID {
		return nil
	}
	card := CandidateCard(a, *c)
	card["kind"] = "candidate"
	data, _ := json.Marshal(card)
	if err := r.o.Session.Deliver(ctx, a.Agent, "candidate:"+evalID, string(data)); err != nil {
		return err
	}
	return r.received(ctx, a, evalID)
}

// Received starts the deadline of each of an agent's Evals whose card a Router run has started on (SA: the deadline
// counts from the card's delivery, not from the time it waited behind another run).
func (r *Router) Received(ctx context.Context, a *host.Agent) error {
	for _, thread := range a.Store.Threads().Active() {
		if e := thread.Evaluating; e != nil && e.Delivered == "" {
			if err := r.received(ctx, a, e.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Router) received(ctx context.Context, a *host.Agent, evalID string) error {
	ok, err := r.o.Session.Received(ctx, a.Agent, "candidate:"+evalID)
	if err != nil || !ok {
		return err
	}
	return a.Engine.CandidateDelivered(evalID)
}

var references = regexp.MustCompile(`#\d+|https?://[^\s)>\]]+`)

// identifiers are the Issue keys a message names, such as ; a ticket whose title or scope names one is referenced.
var identifiers = regexp.MustCompile(`\b[A-Z][A-Z0-9]*-\d+\b`)

// CandidateCard is the Router's card of a candidate: the primary Trigger as a worker reads it, its batch, hand-backs,
// related and referenced tickets, and what to do next.
func CandidateCard(a *host.Agent, c core.Candidate) map[string]any {
	primary := c.Triggers[len(c.Triggers)-1]
	var parent *core.Trigger
	if id := a.Registry.RepliesTo(primary); id != "" {
		parent = a.Store.Triggers().Get(id)
	}
	card := cards.TriggerMessage(primary, cards.Link{SourceURL: primary.URL}, "", parent)
	card["strength"] = c.Strength
	card["mode"] = a.Settings.Eval.Mode
	var batch, handedBack []map[string]any
	for _, t := range c.Triggers {
		if d := a.Store.Triggers().Disposition(t.ID); d != nil {
			for _, h := range d.Handbacks {
				handedBack = append(handedBack, map[string]any{"trigger_id": t.ID, "ticket_id": h.TicketID, "reason": h.Reason})
			}
		}
		if t.ID != primary.ID {
			batch = append(batch, cards.TriggerCard(t, nil))
		}
	}
	if len(batch) > 0 {
		card["batch"] = batch
	}
	if len(handedBack) > 0 {
		card["handed_back"] = handedBack
	}
	for k, v := range cards.RelatedTickets(c.Tickets) {
		card[k] = v
	}
	if d := a.Store.Triggers().Disposition(primary.ID); d != nil && len(d.Fanout) > 0 {
		card["delivered_to"] = d.Fanout
	}
	known := map[string]bool{}
	for _, t := range c.Tickets {
		known[t.ID] = true
	}
	var referenced []map[string]any
	for _, ref := range references.FindAllString(primary.Text, -1) {
		for _, key := range a.Registry.Find(ref).Threads {
			thread := a.Store.Threads().Get(key)
			if thread == nil {
				continue
			}
			for _, id := range thread.Subscriptions {
				if known[id] || len(referenced) >= 4 {
					continue
				}
				known[id] = true
				if t := a.Store.Tickets().Get(id); t != nil {
					referenced = append(referenced, cards.TicketCard(*t))
				}
			}
		}
	}
	if ids := identifiers.FindAllString(primary.Text, -1); len(ids) > 0 {
		for _, tk := range a.Store.Tickets().List() {
			if known[tk.ID] || len(referenced) >= 4 {
				continue
			}
			named := identifiers.FindAllString(tk.Title+" "+tk.Mandate.Scope, -1)
			for _, id := range ids {
				if slices.Contains(named, id) {
					known[tk.ID] = true
					referenced = append(referenced, cards.TicketCard(tk))
					break
				}
			}
		}
	}
	if len(referenced) > 0 {
		card["referenced_tickets"] = referenced
	}
	if c.Strength == "explicit" {
		card["next_action"] = "Route it with multica ticket route as dispatch says: accept, attach, stop or ignore."
	} else {
		card["next_action"] = "Ignore it with multica ticket route if the card shows it touches no enabled rule; else assess it with multica ticket eval, multica ticket eval-read, then multica ticket decide."
	}
	return card
}

// evalFor is the Eval in progress that holds a Trigger; an explicit one is routed instead.
func evalFor(a *host.Agent, triggerID string) (*core.Candidate, core.Trigger, error) {
	c := a.Engine.Candidate(triggerID)
	if c == nil {
		return nil, core.Trigger{}, errors.New(noEval)
	}
	if c.Strength == "explicit" {
		return nil, core.Trigger{}, errors.New(explicit)
	}
	for _, t := range c.Triggers {
		if t.ID == triggerID {
			return c, t, nil
		}
	}
	return nil, core.Trigger{}, errors.New(noEval)
}

func live(a *host.Agent, evalID string, t core.Trigger) bool {
	c := a.Engine.Candidate(t.ID)
	return c != nil && c.EvalID == evalID
}

// reads returns an Eval's reads, seeded with the Trigger's own text; a finished Eval keeps none.
func (r *Router) reads(a *host.Agent, evalID string, t core.Trigger) (*evalReads, error) {
	r.mu.Lock()
	reads := r.evals[evalID]
	r.mu.Unlock()
	if reads != nil {
		return reads, nil
	}
	if !live(a, evalID, t) {
		return nil, errors.New(finished)
	}
	id := ""
	if tc := r.o.ThreadContext(a, t); tc != nil {
		id = tc.MessageID
	}
	reads = &evalReads{messages: map[string]string{id: t.Text}, cursors: map[string]bool{}, pages: map[string]string{}}
	r.mu.Lock()
	if existing := r.evals[evalID]; existing != nil {
		reads = existing
	} else {
		r.evals[evalID] = reads
	}
	r.mu.Unlock()
	return reads, nil
}

// seed loads an Eval's initial window once.
func (r *Router) seed(ctx context.Context, a *host.Agent, evalID string, t core.Trigger) (*evalReads, error) {
	reads, err := r.reads(a, evalID, t)
	if err != nil {
		return nil, err
	}
	reads.mu.Lock()
	defer reads.mu.Unlock()
	if !reads.loaded {
		reads.loaded = true
		tc := r.o.ThreadContext(a, t)
		if tc == nil {
			reads.err = noContext
		} else if result, err := sources.AdmissionContext(ctx, tc, r.o.MaxPages); err != nil {
			reads.err = "Thread context unavailable: " + cut(err.Error(), 300) + ". Use multica ticket eval-read before deciding based on missing context."
		} else {
			reads.context = &result
			if result.Next != "" {
				reads.cursors[result.Next] = true
			}
			for _, m := range result.Messages {
				if !m.Deleted {
					reads.messages[m.ID] = m.Text
				}
			}
		}
	}
	if !live(a, evalID, t) {
		r.Release(evalID)
		return nil, errors.New(finished)
	}
	return reads, nil
}

func cut(text string, limit int) string {
	if r := []rune(text); len(r) > limit {
		return string(r[:limit])
	}
	return text
}

func clip(text string, limit int) map[string]any {
	if len([]rune(text)) <= limit {
		return map[string]any{"text": text}
	}
	return map[string]any{"text": cut(text, limit) + "…", "chars": len([]rune(text))}
}

var whitespace = regexp.MustCompile(`\s+`)

// Context is multica ticket eval: the Trigger's text once, its batch, the Thread's tickets, the Policy version and the
// initial window (the first five messages and five on either side of the Trigger, the two nearest kept longer, the
// rest as stubs).
func (r *Router) Context(ctx context.Context, a *host.Agent, triggerID string) (map[string]any, error) {
	c, t, err := evalFor(a, triggerID)
	if err != nil {
		return nil, err
	}
	reads, err := r.seed(ctx, a, c.EvalID, t)
	if err != nil {
		return nil, err
	}
	tc := r.o.ThreadContext(a, t)
	messageID := ""
	if tc != nil {
		messageID = tc.MessageID
	}
	var thread map[string]any
	if reads.context != nil {
		shown := map[string]bool{}
		for _, e := range reads.context.Window.Entries {
			shown[e.Message.ID] = true
		}
		at := -1
		for i, m := range reads.context.Messages {
			if m.ID == messageID {
				at = i
			}
		}
		messages := []map[string]any{}
		for i, m := range reads.context.Messages {
			item := map[string]any{"id": m.ID, "from": m.From}
			if m.Bot {
				item["bot"] = true
			}
			switch {
			case m.Deleted:
				item["deleted"] = true
			case !shown[m.ID]:
				item["preview"] = cut(whitespace.ReplaceAllString(m.Text, " "), StubChars)
			case i == at:
				item["at"] = m.At
				item["trigger"] = true
			default:
				item["at"] = m.At
				limit := WindowChars
				if at >= 0 && (i-at <= 2 && at-i <= 2) {
					limit = NearChars
				}
				for k, v := range clip(m.Text, limit) {
					item[k] = v
				}
			}
			messages = append(messages, item)
		}
		thread = map[string]any{"complete": reads.context.Complete, "loaded_messages": len(reads.context.Messages),
			"source_found": reads.context.Window.SourceFound, "messages": messages}
		if reads.context.Next != "" {
			thread["next_cursor"] = reads.context.Next
		}
	} else {
		thread = map[string]any{"error": reads.err, "complete": false}
	}
	source := map[string]any{"kind": t.Source, "actor": t.Actor.ID, "thread": core.ThreadKey(t.Threads[0])}
	if t.URL != "" {
		source["url"] = t.URL
	}
	result := map[string]any{"trigger_id": t.ID, "strength": c.Strength, "mode": a.Settings.Eval.Mode, "source": source,
		"policy_version": c.PolicyVersion, "thread": thread}
	for k, v := range clip(t.Text, TriggerChars) {
		result[k] = v
	}
	var batch []map[string]any
	for _, other := range c.Triggers {
		if other.ID == t.ID {
			continue
		}
		item := map[string]any{"trigger_id": other.ID, "actor": other.Actor.ID, "at": other.At}
		text := clip(other.Text, WindowChars)
		for k, v := range text {
			item[k] = v
		}
		if _, cut := text["chars"]; cut {
			item["read"] = "trigger/" + other.ID
		}
		batch = append(batch, item)
	}
	if len(batch) > 0 {
		result["batch"] = batch
	}
	for k, v := range cards.RelatedTickets(c.Tickets) {
		result[k] = v
	}
	return result, nil
}

// Read is what multica ticket eval-read asks for, always with the candidate's trigger_id.
type Read struct {
	TriggerID string `json:"trigger_id"`
	Resource  string `json:"resource"`
	MessageID string `json:"message_id,omitempty"`
	Cursor    string `json:"cursor,omitempty"`
	Offset    int    `json:"offset"`
}

// ReadContext is multica ticket eval-read: a message whole, the root, the parent or a page of the thread, in pages.
func (r *Router) ReadContext(ctx context.Context, a *host.Agent, read Read) (map[string]any, error) {
	c, t, err := evalFor(a, read.TriggerID)
	if err != nil {
		return nil, err
	}
	switch read.Resource {
	case "message", "root", "parent", "thread":
	default:
		return nil, fmt.Errorf("A personal agent reads message, root, parent or thread; %s is not one", read.Resource)
	}
	if read.MessageID != "" && read.Resource != "message" {
		return nil, errors.New("message_id is only for message; use returned image_id for images.")
	}
	if read.Cursor != "" && read.Resource != "thread" {
		return nil, errors.New("cursor is only for thread. First page: {resource:\"thread\"}.")
	}
	reads, err := r.seed(ctx, a, c.EvalID, t)
	if err != nil {
		return nil, err
	}
	cacheKey := strings.Join([]string{read.Resource, read.Cursor, read.MessageID}, "\x00")
	reads.mu.Lock()
	defer reads.mu.Unlock()
	if read.Offset > 0 {
		body, ok := reads.pages[cacheKey]
		if !ok {
			return nil, errors.New("Read this resource at offset 0 first")
		}
		return cards.TextPage(read.Resource, body, read.Offset, 4000), nil
	}
	tc := r.o.ThreadContext(a, t)
	var body string
	if read.Resource == "message" {
		id := read.MessageID
		if id == "" && tc != nil {
			id = tc.MessageID
		}
		text, ok := reads.messages[id]
		if !ok {
			return nil, errors.New("Unknown message reference. Use resource=root/parent or read the thread first; copy a returned message read call.")
		}
		body = text
	} else {
		if tc == nil {
			return nil, errors.New(noContext)
		}
		if read.Cursor != "" && !reads.cursors[read.Cursor] {
			return nil, errors.New("Unknown thread cursor. First call {resource:\"thread\"}; then copy the returned next_read exactly.")
		}
		part, err := tc.Read(ctx, read.Resource, read.Cursor)
		if err != nil {
			return nil, err
		}
		for _, m := range part.Messages {
			if !m.Deleted {
				reads.messages[m.ID] = m.Text
			}
		}
		var next any
		if part.Next != "" {
			reads.cursors[part.Next] = true
			next = map[string]any{"trigger_id": t.ID, "resource": "thread", "cursor": part.Next}
		}
		m := map[string]any{"images": []any{}, "next_read": next}
		for k, v := range part.Body {
			m[k] = v
		}
		if part.Body == nil {
			m["messages"] = part.Messages
		}
		data, _ := json.Marshal(m)
		body = string(data)
	}
	reads.pages[cacheKey] = body
	return cards.TextPage(read.Resource, body, 0, 4000), nil
}

// Decide is multica ticket decide: the assessment, under the agent's settings, as an outcome for the Engine to verify.
func (r *Router) Decide(a *host.Agent, triggerID string, assessment policy.Assessment) (map[string]any, error) {
	c, _, err := evalFor(a, triggerID)
	if err != nil {
		return nil, err
	}
	outcome, err := policy.AssessmentOutcome(assessment, a.Settings, c.Strength)
	if err != nil {
		return nil, err
	}
	outcome.EvalID = c.EvalID
	decision, err := a.Engine.Decide(triggerID, outcome)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"state": decision.Kind, "reason": core.ReasonText(decision.Reason...)}
	if decision.TicketID != "" {
		result["ticket_id"] = decision.TicketID
	}
	return result, nil
}
