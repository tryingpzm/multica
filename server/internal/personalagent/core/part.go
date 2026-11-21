package core

import "time"

// EngineOptions configures the Engine (SA EngineOptions).
type EngineOptions struct {
	Repo   Repositories
	Policy func() Policy
	// Scope decides whether a Source listens to a trigger; one it does not listen to is stored with a filtered disposition.
	Scope    SourceScope
	Assessor Assessor
	Executor Executor
	Outbox   Outbox
	// PrepareTicket may adjust a ticket an accept creates; it may panic to refuse the accept.
	PrepareTicket func(ticket Ticket, outcome Outcome) Ticket
	Now           func() string
	Emit          func(event DomainEvent)
}

// base is SA's EnginePart: shared access to repositories, policy, clock and event emission.
type base struct{ options *EngineOptions }

func (p *base) repo() Repositories { return p.options.Repo }
func (p *base) now() string {
	if p.options.Now != nil {
		return p.options.Now()
	}
	return FormatTime(time.Now().UnixMilli())
}
func (p *base) policy() Policy { return p.options.Policy() }

// owners returns the tickets whose worker decides a person's follow-up.
func (p *base) owners(trigger Trigger) []string { return FollowupOwners(trigger, Records(p.repo())) }

// leftToWorkers refuses a main-Session routing of a follow-up a ticket's worker still decides.
func (p *base) leftToWorkers(trigger Trigger) {
	if owners := p.owners(trigger); len(owners) > 0 {
		refuse("workerDecides", "tickets", join(owners, ", "))
	}
}

// handBack records that a ticket's worker hands back a Trigger that reached it, and queues it for Eval when the ticket
// decided it and no other ticket still does.
func (p *base) handBack(ticketID string, trigger Trigger, reason string) bool {
	decided := contains(p.owners(trigger), ticketID)
	disposition := p.disposition(trigger.ID)
	disposition.Handbacks = append(disposition.Handbacks, Handback{TicketID: ticketID, Reason: reason, At: p.now()})
	p.repo().Triggers().SaveDisposition(trigger.ID, disposition)
	return decided && len(p.owners(trigger)) == 0 && p.queueForEval(trigger)
}

// takeUp records that a ticket's worker took a person's message up as within the ticket.
func (p *base) takeUp(ticketID, triggerID string) {
	disposition := p.repo().Triggers().Disposition(triggerID)
	if disposition != nil && !contains(disposition.Acked, ticketID) {
		disposition.Acked = append(disposition.Acked, ticketID)
		p.repo().Triggers().SaveDisposition(triggerID, *disposition)
	}
}

// queueForEval queues a Trigger on its primary Thread for the next Eval, unless it waits there already or an Eval holds it.
func (p *base) queueForEval(trigger Trigger) bool {
	primary := p.thread(trigger.Threads[0])
	if contains(primary.Pending, trigger.ID) || (primary.Evaluating != nil && contains(primary.Evaluating.TriggerIDs, trigger.ID)) {
		return false
	}
	primary.Pending = append(primary.Pending, trigger.ID)
	p.repo().Threads().Save(primary)
	return true
}

func (p *base) emit(kind string, data map[string]any) {
	if p.options.Emit != nil {
		p.options.Emit(DomainEvent{Type: kind, At: p.now(), Data: data})
	}
}

func (p *base) require(ticketID string) *Ticket {
	ticket := p.repo().Tickets().Get(ticketID)
	if ticket == nil {
		refuse("unknownTicket")
	}
	return ticket
}

// disposition returns the stored disposition or an empty one at now.
func (p *base) disposition(id string) TriggerDisposition {
	if d := p.repo().Triggers().Disposition(id); d != nil {
		return *d
	}
	return TriggerDisposition{Fanout: []string{}, At: p.now()}
}

// thread returns the stored Thread or an unsaved empty Thread aggregate.
func (p *base) thread(ref ThreadRef) Thread {
	k := ThreadKey(ref)
	if t := p.repo().Threads().Get(k); t != nil {
		return *t
	}
	return Thread{Key: k, Ref: ThreadRef{Source: ref.Source, ID: ref.ID}, CreatedAt: p.now(), Subscriptions: []string{}, Correlated: []string{},
		Pending: []string{}, Meta: map[string]any{}}
}

func (p *base) ticketsOnThread(thread Thread) []Ticket {
	out := []Ticket{}
	for _, id := range thread.Subscriptions {
		if ticket := p.repo().Tickets().Get(id); ticket != nil {
			out = append(out, *ticket)
		}
	}
	return out
}

func (p *base) source(ticket *Ticket) *Trigger {
	return p.repo().Triggers().Get(ticket.Origin.TriggerID)
}

func (p *base) reaches(ticket *Ticket, trigger Trigger, context bool) bool {
	return Reached(ticket, trigger, func(k string) *Thread { return p.repo().Threads().Get(k) }, context)
}

func join(list []string, sep string) string {
	out := ""
	for i, item := range list {
		if i > 0 {
			out += sep
		}
		out += item
	}
	return out
}
