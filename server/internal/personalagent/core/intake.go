package core

// Intake ingests incoming triggers from Sources (SA intake.ts).
type Intake struct {
	base
	tickets *Tickets
}

// Receive stores a trigger once, fans it out to subscribed tickets, and queues it for Eval unless a ticket's worker
// decides it. It returns "stored", "duplicate" or "filtered".
func (p *Intake) Receive(received Trigger) string {
	trigger := received
	trigger.At = ISOTime(received.At)
	if len(trigger.Threads) == 0 {
		panic("A trigger must belong to at least one thread")
	}
	repo := p.repo()
	if repo.Triggers().Get(trigger.ID) != nil {
		return "duplicate"
	}
	at := p.now()
	filter := p.options.Scope.Scope(trigger.Source, trigger.Meta)
	if !filter.Listened {
		repo.Triggers().Save(trigger)
		repo.Triggers().SaveDisposition(trigger.ID, TriggerDisposition{Filtered: &Reason{Code: filter.Reason}, Fanout: []string{}, At: at})
		return "filtered"
	}
	repo.Triggers().Save(trigger)
	keys := make([]string, 0, len(trigger.Threads))
	for _, ref := range trigger.Threads {
		keys = append(keys, ThreadKey(ref))
	}
	type reach struct {
		ticket  *Ticket
		context bool
	}
	reached := map[string]*reach{}
	order := []string{}
	for _, ref := range trigger.Threads {
		thread := p.thread(ref)
		if thread.LastAt == "" || trigger.At > thread.LastAt {
			thread.LastAt = trigger.At
		}
		others := []string{}
		for _, k := range keys {
			if k != thread.Key {
				others = append(others, k)
			}
		}
		thread.Correlated = uniq(append(append([]string{}, thread.Correlated...), others...))
		for _, ticketID := range thread.Subscriptions {
			ticket := repo.Tickets().Get(ticketID)
			// Only a person's message on a non-context thread resumes an ended ticket, and a human-stopped one receives nothing.
			followed := contains(thread.Context, ticketID) || trigger.Notice
			resumes := trigger.Human && ticket != nil && (ticket.Ended == nil || ticket.Ended.By != "human") && !followed
			if ticket == nil || (!LiveTicket(*ticket) && !resumes) {
				continue
			}
			previous := true
			if r, ok := reached[ticketID]; ok {
				previous = r.context
			} else {
				order = append(order, ticketID)
			}
			reached[ticketID] = &reach{ticket: ticket, context: followed && previous}
		}
		repo.Threads().Save(thread)
	}
	for _, ticketID := range order {
		r := reached[ticketID]
		input := TicketInput{ID: Key("input", ticketID, "trigger", trigger.ID), Kind: "trigger", TriggerID: trigger.ID, At: at,
			Human: trigger.Human, Context: r.context}
		p.tickets.addInput(r.ticket, input)
	}
	fanout := append([]string{}, order...)
	context := []string{}
	for _, id := range fanout {
		if reached[id].context {
			context = append(context, id)
		}
	}
	disposition := TriggerDisposition{Fanout: fanout, At: at}
	if len(context) > 0 {
		disposition.Context = context
	}
	repo.Triggers().SaveDisposition(trigger.ID, disposition)
	// A person's message no ticket decides, and every bot's, goes to Eval.
	if len(p.owners(trigger)) == 0 {
		p.queueForEval(trigger)
	}
	for _, ticketID := range fanout {
		p.tickets.wake(ticketID)
	}
	p.emit("trigger.received", map[string]any{"triggerId": trigger.ID, "threads": keys})
	return "stored"
}
