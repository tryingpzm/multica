package core

// GateResult is a gate admission result: inadmissible with a reason code, or admissible with strength and basis.
type GateResult struct {
	Admissible bool
	Reason     string
	Strength   Strength
	Basis      MandateBasis
}

// ScopeResult says whether a Source receives a Trigger; Observed when it also receives messages not addressed to the
// agent, and Row the receiving scope the admission table names.
type ScopeResult struct {
	Listened bool
	Observed bool
	Row      *string
	Reason   string
}

// SourceScope is the Source configuration as the domain sees it.
type SourceScope interface {
	Scope(source string, meta map[string]any) ScopeResult
}

// Policy is the versioned rules; Gate decides who may originate work.
type Policy interface {
	Version() string
	Paused() bool
	Gate(trigger Trigger, thread Thread, tickets []Ticket) GateResult
	Revalidate(mandate Mandate) bool
	AdmitsInferred(rules []string) bool
	EvalTimeoutMs() int64
}

// Candidate is what the assessor receives: bounded facts, never authority.
type Candidate struct {
	EvalID        string
	Thread        Thread
	Triggers      []Trigger
	Strength      Strength
	PolicyVersion string
	Tickets       []Ticket
}

// Outcome is SA's AssessmentOutcome & { evalId }, flattened by Kind: ignore, unavailable, accept, stop. The reason is
// ReasonCode when set, else the words in Said.
type Outcome struct {
	Kind            string
	Said            string
	ReasonCode      *Reason
	Audit           map[string]any
	Title           string
	Scope           string
	Acknowledgement string
	Rules           []string
	Name            string
	Note            string
	TicketID        string
	EvalID          string
}

func (o Outcome) reasons() []Reason {
	if o.ReasonCode != nil {
		return []Reason{*o.ReasonCode}
	}
	return []Reason{Said(o.Said)}
}

// Assessor does the semantic judgement; the outcome returns through Engine.Decide.
type Assessor interface {
	Request(candidate Candidate)
	Release(evalID string)
}

// Executor executes assignments; each method only queues work, which reports back through the Engine.
type Executor interface {
	Start(ticket Ticket, assignment Assignment)
	Deliver(ticket Ticket, assignment Assignment)
	Stop(ticket Ticket, assignment Assignment, reason string)
}

// OutboundMessage is a message for a Channel.
type OutboundMessage struct {
	ID       string     `json:"id"`
	Channel  ChannelRef `json:"channel"`
	Text     string     `json:"text"`
	Kind     string     `json:"kind"`
	TicketID string     `json:"ticketId,omitempty"`
}

// Outbox holds the messages queued for Channels. Status returns "" for a message never queued.
type Outbox interface {
	Send(message OutboundMessage)
	Status(id string) string
	Withdraw(id string)
}

// DomainEvent is what the Engine reports after a change.
type DomainEvent struct {
	Type string
	At   string
	Data map[string]any
}

// TriggerRepo stores Triggers and their dispositions.
type TriggerRepo interface {
	Get(id string) *Trigger
	Save(trigger Trigger)
	Disposition(id string) *TriggerDisposition
	SaveDisposition(id string, value TriggerDisposition)
}

// ThreadRepo stores Threads.
type ThreadRepo interface {
	Get(key string) *Thread
	Save(thread Thread)
	List() []Thread
	// Active lists Threads with Triggers queued for Eval or an Eval in progress.
	Active() []Thread
}

// TicketRepo stores Tickets; List is newest first.
type TicketRepo interface {
	Get(id string) *Ticket
	Save(ticket Ticket)
	List() []Ticket
	Open() []Ticket
	// Active lists tickets with undelivered Inputs or a pending or running latest Assignment.
	Active() []Ticket
}

// DecisionRepo stores Decisions.
type DecisionRepo interface {
	Get(id string) *Decision
	Save(decision Decision)
	ByThread(key string) []Decision
}

// Repositories is durable state. Every method is synchronous so Atomic can wrap a unit of work; Atomic rolls the work
// back and re-panics when it panics.
type Repositories interface {
	Atomic(work func())
	Triggers() TriggerRepo
	Threads() ThreadRepo
	Tickets() TicketRepo
	Decisions() DecisionRepo
}

// Records adapts Repositories to FollowupRecords.
func Records(repo Repositories) FollowupRecords { return records{repo} }

type records struct{ repo Repositories }

func (r records) TicketByID(id string) *Ticket   { return r.repo.Tickets().Get(id) }
func (r records) ThreadByKey(key string) *Thread { return r.repo.Threads().Get(key) }
func (r records) DispositionOf(id string) *TriggerDisposition {
	return r.repo.Triggers().Disposition(id)
}

func activeTicket(ticket Ticket) bool {
	if len(ticket.Undelivered) > 0 {
		return true
	}
	latest := LatestAssignment(&ticket)
	return latest != nil && (latest.State == "pending" || latest.State == "running")
}

func activeThread(thread Thread) bool { return len(thread.Pending) > 0 || thread.Evaluating != nil }

// ActiveTicket and ActiveThread are the Active() predicates shared by every Repositories implementation.
var (
	ActiveTicket = activeTicket
	ActiveThread = activeThread
)
