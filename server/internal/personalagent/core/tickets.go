package core

import "encoding/json"

func sameChannel(a, b ChannelRef) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// automatic reports whether an Input is a message no person wrote, such as CI's, a bot's, or the main Session's note.
func automatic(input *TicketInput) bool {
	return input != nil && (input.Kind == "note" || (input.Kind == "trigger" && !input.Human))
}

// readBefore reports whether a ticket's worker read a Trigger Input before until.
func readBefore(input *TicketInput, until string) bool {
	if input.ReadAt == nil {
		at := input.At
		if input.DeliveredAt != "" && input.DeliveredAt > input.At {
			at = input.DeliveredAt
		}
		return at <= until
	}
	return *input.ReadAt != "" && *input.ReadAt < until
}

const statusRequest = "Report the current ticket status with multica ticket report. This is a status-only request: do not resume implementation or perform external actions."

// Tickets manages each ticket's lifecycle (SA tickets.ts).
type Tickets struct {
	base
	replies *Replies
}

func (p *Tickets) findInput(ticket *Ticket, id string) *TicketInput {
	for _, input := range ticket.Inputs {
		if input.ID == id {
			return input
		}
	}
	return nil
}

// create creates the ticket an accept decided, under mandate, and queues its launch.
func (p *Tickets) create(id, decisionID string, mandate Mandate, evaluation Evaluation, outcome Outcome, at string) {
	repo := p.repo()
	triggerIDs := evaluation.TriggerIDs
	primaryID := evaluation.Primary
	if primaryID == "" {
		primaryID = triggerIDs[len(triggerIDs)-1]
	}
	primary := repo.Triggers().Get(primaryID)
	origin := Origin{TriggerID: triggerIDs[0], TriggerIDs: append([]string{}, triggerIDs...)}
	if primary != nil {
		origin.TriggerID = primary.ID
	}
	ticket := Ticket{ID: id, Title: outcome.Title, DecisionID: decisionID, Mandate: mandate, Channels: []ChannelRef{}, Assignments: []*Assignment{},
		Inputs: []*TicketInput{}, Undelivered: []string{}, CreatedAt: at, UpdatedAt: at, Origin: origin}
	for _, triggerID := range triggerIDs {
		if t := repo.Triggers().Get(triggerID); t != nil && t.Channel != nil {
			seen := false
			for _, item := range ticket.Channels {
				if sameChannel(item, *t.Channel) {
					seen = true
				}
			}
			if !seen {
				ticket.Channels = append(ticket.Channels, *t.Channel)
			}
		}
	}
	if p.options.PrepareTicket != nil {
		ticket = p.options.PrepareTicket(ticket, outcome)
	}
	assignment := p.newAssignment(&ticket)
	repo.Tickets().Save(ticket)
	for _, triggerID := range triggerIDs {
		if t := repo.Triggers().Get(triggerID); t != nil {
			for _, ref := range t.Threads {
				p.subscribe(id, ref)
			}
		}
	}
	if outcome.Acknowledgement != "" && primary != nil {
		p.replies.acknowledgeAccepted(id, *primary, outcome.Acknowledgement)
	}
	p.options.Executor.Start(*p.require(id), *assignment)
	p.emit("ticket.created", map[string]any{"ticketId": id, "decisionId": decisionID, "strength": mandate.Strength})
}

// bind subscribes an open ticket to thread and records it in ticket.Follows.
func (p *Tickets) bind(ticketID string, thread ThreadRef, own bool) Ticket {
	if !LiveTicket(*p.require(ticketID)) {
		refuse("ticketEnded")
	}
	p.subscribe(ticketID, thread)
	ticket := p.require(ticketID)
	followed := ThreadKey(thread)
	var follow *Follow
	for _, item := range ticket.Follows {
		if item.Thread == followed {
			follow = item
		}
	}
	if follow != nil {
		follow.Own = follow.Own || own
		follow.Closed = nil
	} else {
		ticket.Follows = append(ticket.Follows, &Follow{Thread: followed, Own: own})
	}
	ticket.UpdatedAt = p.now()
	p.repo().Tickets().Save(*ticket)
	return *ticket
}

// threadClosed records that a Thread open tickets follow closed, and ends each one done with its Threads.
func (p *Tickets) threadClosed(thread ThreadRef, outcome, at string) {
	closed := ThreadKey(thread)
	for _, t := range p.repo().Tickets().Open() {
		ticket := t
		var follow *Follow
		for _, item := range ticket.Follows {
			if item.Thread == closed && item.Closed == nil {
				follow = item
				break
			}
		}
		if follow == nil {
			continue
		}
		follow.Closed = &Closed{At: at, Outcome: outcome}
		p.repo().Tickets().Save(ticket)
		done := p.followsDone(&ticket)
		if done == nil || p.unfinished(&ticket, at) {
			continue
		}
		// Work resumed after the close and finished ends now.
		endAt := done.At
		if latest := LatestAssignment(&ticket); latest != nil && laterThan(latest.CreatedAt, at) {
			endAt = p.now()
		}
		p.end(ticket.ID, "executor", done.Outcome, endAt, "")
	}
}

// followsDone says how a ticket that follows Threads is done with them, or nil while it is not done.
func (p *Tickets) followsDone(ticket *Ticket) *Closed {
	own := []*Follow{}
	for _, item := range ticket.Follows {
		if item.Own {
			own = append(own, item)
		}
	}
	counted := own
	if len(own) == 0 {
		counted = ticket.Follows
	}
	if len(counted) == 0 {
		return nil
	}
	var latest *Closed
	for _, item := range counted {
		if item.Closed == nil {
			return nil
		}
		if latest == nil || item.Closed.At >= latest.At {
			latest = item.Closed
		}
	}
	c := *latest
	return &c
}

// attachable returns the Trigger to attach to a ticket, once the ticket may take it.
func (p *Tickets) attachable(ticketID, triggerID string) Trigger {
	trigger := p.repo().Triggers().Get(triggerID)
	if trigger == nil {
		refuse("unknownTrigger")
	}
	p.leftToWorkers(*trigger)
	current := p.require(ticketID)
	if current.Ended != nil && current.Ended.By == "human" {
		refuse("ticketStopped")
	}
	if !LiveTicket(*current) && !p.policy().Revalidate(current.Mandate) {
		refuse("mandateRevoked")
	}
	return *trigger
}

// attach subscribes a ticket to an attached Trigger's Threads and adds the Trigger as an Input, with its note.
func (p *Tickets) attach(ticketID string, trigger Trigger, note string) {
	for _, ref := range trigger.Threads {
		p.subscribe(ticketID, ref)
	}
	current := p.disposition(trigger.ID)
	context, requeued := current.Context, current.Requeued
	disposition := current
	disposition.Context, disposition.Requeued = nil, false
	followed := without(context, ticketID)
	disposition.Fanout = uniq(append(append([]string{}, current.Fanout...), ticketID))
	if len(followed) > 0 {
		disposition.Context = followed
	}
	p.repo().Triggers().SaveDisposition(trigger.ID, disposition)
	ticket := p.require(ticketID)
	id := Key("input", ticketID, "trigger", trigger.ID)
	existing := p.findInput(ticket, id)
	at := p.now()
	input := func(inputID string) TicketInput {
		return TicketInput{ID: inputID, Kind: "trigger", TriggerID: trigger.ID, At: at, Note: note, Human: trigger.Human}
	}
	switch {
	case existing == nil || existing.Kind != "trigger":
		p.addInput(ticket, input(id))
	case note != "" && contains(ticket.Undelivered, id):
		existing.Note = note
		ticket.UpdatedAt = at
		p.repo().Tickets().Save(*ticket)
	case requeued || !contains(current.Fanout, ticketID) || contains(context, ticketID) || !LiveTicket(*ticket):
		// A person sent it to Eval, or the ticket let it go, read it for context only, or ended after its worker had it.
		p.addInput(ticket, input(Key("input", ticketID, "trigger", trigger.ID, "attached", at)))
	case note != "":
		// The worker has the message already, so the note comes with it again.
		p.addInput(ticket, input(Key("input", ticketID, "trigger", trigger.ID, "note", note)))
	}
}

// requestStatus asks a running worker whose latest report is waiting for a status report.
func (p *Tickets) requestStatus(ticketID string) bool {
	ticket := p.require(ticketID)
	running := RunningAssignment(ticket)
	if !LiveTicket(*ticket) || running == nil || running.Report == nil || running.Report.State != "waiting" {
		return false
	}
	p.addInput(ticket, TicketInput{ID: "status:" + ticketID + ":" + ticket.UpdatedAt, Kind: "text", Text: statusRequest, At: p.now()})
	return true
}

// stop stops a ticket on behalf of a human.
func (p *Tickets) stop(ticketID, reason string) Ticket {
	p.end(ticketID, "human", "stopped", p.now(), reason)
	return *p.require(ticketID)
}

// retry ends the stalled assignment of a retryable open ticket and starts a new one.
func (p *Tickets) retry(ticketID string) Ticket {
	ticket := p.require(ticketID)
	latest := LatestAssignment(ticket)
	if !CanRetry(ticket) || latest == nil {
		refuse("notRetryable")
	}
	p.endAssignment(ticket, latest, "retried")
	p.requeueUntaken(ticket, latest)
	assignment := p.newAssignment(ticket)
	ticket.UpdatedAt = p.now()
	p.repo().Tickets().Save(*ticket)
	p.options.Executor.Stop(*ticket, *latest, "retried")
	p.options.Executor.Start(*ticket, *assignment)
	return *ticket
}

// markUnrelated drops a ticket's Subscription to a Thread its worker found unrelated, and hands back the person's messages
// there that the ticket decides.
func (p *Tickets) markUnrelated(ticketID, thread, reason string) {
	ticket := p.require(ticketID)
	if src := p.source(ticket); src != nil {
		for _, ref := range src.Threads {
			if ThreadKey(ref) == thread {
				refuse("sourceThread")
			}
		}
	}
	for _, item := range ticket.Follows {
		if item.Thread == thread {
			refuse("followedThread")
		}
	}
	if t := p.repo().Threads().Get(thread); t == nil || !contains(t.Subscriptions, ticketID) {
		refuse("notFollowing")
	}
	p.dropSubscriptions(thread, "executor", ticketID, reason)
	for _, trigger := range p.triggersOf(ticket, nil) {
		on := false
		for _, ref := range trigger.Threads {
			if ThreadKey(ref) == thread {
				on = true
			}
		}
		if on && contains(p.owners(trigger), ticketID) {
			p.handBack(ticketID, trigger, reason)
		}
	}
}

// policyChanged tells each open ticket a Policy change bears on how it does, oldest first.
func (p *Tickets) policyChanged(from, to string, effects map[string]PolicyEffect) {
	at := p.now()
	open := sortedBy(p.repo().Tickets().Open(), func(a, b Ticket) bool { return a.CreatedAt < b.CreatedAt })
	for i := range open {
		ticket := open[i]
		if effect, ok := effects[ticket.ID]; ok {
			e := effect
			p.addInput(&ticket, TicketInput{ID: Key("input", ticket.ID, "policy", from, to, at), Kind: "policy-changed", From: from, To: to, At: at, Effect: &e})
		}
	}
}

// dropSubscriptions drops a Thread's Subscriptions, every ticket's or only ticketID's; false when none dropped.
func (p *Tickets) dropSubscriptions(thread, by, ticketID, reason string) bool {
	current := p.repo().Threads().Get(thread)
	if current == nil {
		return false
	}
	tickets := []string{}
	for _, id := range current.Subscriptions {
		if ticketID == "" || id == ticketID {
			tickets = append(tickets, id)
		}
	}
	if len(tickets) == 0 {
		return false
	}
	current.Subscriptions = without(current.Subscriptions, tickets...)
	p.repo().Threads().Save(*current)
	for _, id := range tickets {
		ticket := p.repo().Tickets().Get(id)
		if ticket == nil {
			continue
		}
		follows := []*Follow{}
		found := false
		for _, item := range ticket.Follows {
			if item.Thread == thread {
				found = true
			} else {
				follows = append(follows, item)
			}
		}
		if !found {
			continue
		}
		if len(follows) > 0 {
			ticket.Follows = follows
		} else {
			ticket.Follows = nil
		}
		p.repo().Tickets().Save(*ticket)
	}
	for _, id := range tickets {
		data := map[string]any{"threadKey": current.Key, "ticketId": id, "by": by}
		if reason != "" {
			data["reason"] = reason
		}
		p.emit("thread.unsubscribed", data)
	}
	return true
}

// addInput adds an Input once and asks the Executor to deliver it, or to launch again a pending Assignment whose
// launch failed once no person is waited for.
func (p *Tickets) addInput(ticket *Ticket, input TicketInput) {
	if p.findInput(ticket, input.ID) != nil {
		return
	}
	in := input
	ticket.Inputs = append(ticket.Inputs, &in)
	ticket.UpdatedAt = p.now()
	// Hold back automated inputs while the worker is waiting on a person.
	held := LiveTicket(*ticket) && automatic(&in) && WaitsForPerson(ticket)
	if !held {
		ticket.Undelivered = append(ticket.Undelivered, in.ID)
	}
	p.repo().Tickets().Save(*ticket)
	if !LiveTicket(*ticket) || held {
		return
	}
	running, latest := RunningAssignment(ticket), LatestAssignment(ticket)
	if running != nil {
		p.options.Executor.Deliver(*ticket, *running)
	} else if latest != nil && latest.State == "pending" && !WaitsForPerson(ticket) {
		p.options.Executor.Start(*ticket, *latest)
	}
}

// wake resumes an ended ticket under its unchanged Mandate when a person's message on one of its Threads waits for it.
func (p *Tickets) wake(ticketID string) {
	ticket := p.repo().Tickets().Get(ticketID)
	if ticket == nil || LiveTicket(*ticket) || ticket.Ended.By == "human" || len(ticket.Undelivered) == 0 {
		return
	}
	policy := p.policy()
	if policy.Paused() {
		return
	}
	if !policy.Revalidate(ticket.Mandate) {
		p.refuse(ticket)
		return
	}
	// Only a human's message resumes an ended ticket.
	var first *TicketInput
	for _, id := range ticket.Undelivered {
		if input := p.findInput(ticket, id); input != nil && input.Kind == "trigger" && input.Human {
			first = input
			break
		}
	}
	if first == nil {
		return
	}
	at := p.now()
	ticket.Ended = nil
	ticket.UpdatedAt = at
	assignment := p.newAssignment(ticket)
	// Prepend the resumed marker so it is delivered ahead of the waiting messages.
	resumed := &TicketInput{ID: Key("input", ticket.ID, "resumed", first.TriggerID), Kind: "resumed", TriggerID: first.TriggerID, At: at}
	ticket.Inputs = append(ticket.Inputs, resumed)
	ticket.Undelivered = append([]string{resumed.ID}, ticket.Undelivered...)
	p.repo().Tickets().Save(*ticket)
	p.options.Executor.Start(*ticket, *assignment)
	p.emit("ticket.resumed", map[string]any{"ticketId": ticketID, "triggerId": first.TriggerID})
}

// triggersOf returns the Triggers of a ticket's trigger Inputs, or of those whose Input ids are ids.
func (p *Tickets) triggersOf(ticket *Ticket, ids []string) []Trigger {
	out := []Trigger{}
	for _, input := range ticket.Inputs {
		if input.Kind == "trigger" && (ids == nil || contains(ids, input.ID)) {
			if t := p.repo().Triggers().Get(input.TriggerID); t != nil {
				out = append(out, *t)
			}
		}
	}
	return out
}

// takenUp reports whether a ticket's worker took a person's message up, by acknowledging or answering it.
func (p *Tickets) takenUp(ticket *Ticket, trigger Trigger) bool {
	if d := p.repo().Triggers().Disposition(trigger.ID); d != nil && d.Acked != nil {
		return contains(d.Acked, ticket.ID)
	}
	return p.options.Outbox.Status(Key("ack", trigger.ID)) != ""
}

// untaken returns the person's messages among Inputs ids that the ticket decides and its worker did not take up.
func (p *Tickets) untaken(ticket *Ticket, ids []string) []Trigger {
	out := []Trigger{}
	for _, trigger := range p.triggersOf(ticket, ids) {
		if contains(p.owners(trigger), ticket.ID) && !p.takenUp(ticket, trigger) {
			out = append(out, trigger)
		}
	}
	return out
}

// letGo makes a ticket that ended, or cannot resume, let go of the messages its worker never received and of the
// person's messages it decided but may not have taken up.
func (p *Tickets) letGo(ticketID string, waiting, left []Trigger) {
	for _, trigger := range append(append([]Trigger{}, waiting...), left...) {
		d := p.repo().Triggers().Disposition(trigger.ID)
		if d != nil && contains(d.Fanout, ticketID) {
			d.Fanout = without(d.Fanout, ticketID)
			p.repo().Triggers().SaveDisposition(trigger.ID, *d)
		}
	}
	for _, trigger := range left {
		d := p.repo().Triggers().Disposition(trigger.ID)
		var decision *Decision
		if d != nil && d.DecisionID != "" {
			decision = p.repo().Decisions().Get(d.DecisionID)
		}
		// The main Session that stopped this ticket on the message decides the rest of it itself, unless a ticket handed it back.
		stopped := decision != nil && decision.Kind == "stop" && decision.TicketID == ticketID && (d == nil || len(d.Handbacks) == 0)
		decided := decision != nil && (Settled(*decision) || stopped)
		if len(p.owners(trigger)) == 0 && (d == nil || len(d.Acked) == 0) && !decided {
			p.queueForEval(trigger)
		}
	}
}

// refuse makes an ended ticket that cannot resume take none of the messages that waited for it.
func (p *Tickets) refuse(ticket *Ticket) {
	waiting, left := p.triggersOf(ticket, ticket.Undelivered), p.untaken(ticket, ticket.Undelivered)
	ticket.Undelivered = []string{}
	p.repo().Tickets().Save(*ticket)
	p.letGo(ticket.ID, waiting, left)
}

// restart replaces a ticket's running Assignment with a new one that receives note first, and queues it for launch.
func (p *Tickets) restart(ticket *Ticket, running *Assignment, reason, note string) {
	p.endAssignment(ticket, running, reason)
	p.requeueUntaken(ticket, running)
	assignment := p.newAssignment(ticket)
	at := p.now()
	input := &TicketInput{ID: Key("input", ticket.ID, "restart", assignment.ID), Kind: "text", Text: note, At: at}
	ticket.Inputs = append(ticket.Inputs, input)
	ticket.Undelivered = append(ticket.Undelivered, input.ID)
	ticket.UpdatedAt = at
	p.repo().Tickets().Save(*ticket)
	p.options.Executor.Start(*ticket, *assignment)
}

// requeueUntaken puts back, ahead of what waits, the Trigger Inputs delivered to replaced that no step of its worker took.
func (p *Tickets) requeueUntaken(ticket *Ticket, replaced *Assignment) {
	untaken := []string{}
	for _, input := range ticket.Inputs {
		if input.Kind == "trigger" && input.ReadAt != nil && *input.ReadAt == "" && input.DeliveredTo == replaced.ID && !contains(ticket.Undelivered, input.ID) {
			untaken = append(untaken, input.ID)
		}
	}
	ticket.Undelivered = append(untaken, ticket.Undelivered...)
}

// end ends a ticket with its outcome; at records when the ending fact happened and reason says why in words.
func (p *Tickets) end(ticketID, by, outcome, at, reason string) {
	ticket := p.require(ticketID)
	if !LiveTicket(*ticket) {
		return
	}
	stopping := []*Assignment{}
	for _, a := range ticket.Assignments {
		if a.State != "ended" {
			stopping = append(stopping, a)
		}
	}
	why := ""
	if reason != "" && reason != outcome {
		why = reason
	}
	until := ReadUntil(ticket, true)
	waiting := p.triggersOf(ticket, ticket.Undelivered)
	leftIDs := []string{}
	for _, input := range ticket.Inputs {
		if input.Kind == "trigger" && (!readBefore(input, until) || contains(ticket.Undelivered, input.ID)) {
			leftIDs = append(leftIDs, input.ID)
		}
	}
	left := p.untaken(ticket, leftIDs)
	ticket.Ended = &Ended{At: at, By: by, Outcome: outcome, Reason: why}
	ticket.Undelivered = []string{}
	ticket.UpdatedAt = p.now()
	ticket.Attention, ticket.Delivery = nil, nil
	for _, a := range stopping {
		a.State = "ended"
		a.EndedAt = at
	}
	p.repo().Tickets().Save(*ticket)
	p.letGo(ticketID, waiting, left)
	data := map[string]any{"ticketId": ticketID, "by": by, "outcome": outcome}
	if why != "" {
		data["reason"] = why
	}
	p.emit("ticket.ended", data)
	stopReason := reason
	if stopReason == "" {
		stopReason = outcome
	}
	for _, a := range stopping {
		p.options.Executor.Stop(*ticket, *a, stopReason)
	}
}

// unfinished reports whether a ticket's work has not finished at a close at at.
func (p *Tickets) unfinished(ticket *Ticket, at string) bool {
	until := ReadUntil(ticket, false)
	ids := []string{}
	for _, input := range ticket.Inputs {
		if input.Kind == "trigger" && !readBefore(input, until) {
			ids = append(ids, input.ID)
		}
	}
	taken := false
	for _, trigger := range p.triggersOf(ticket, ids) {
		if contains(p.owners(trigger), ticket.ID) && p.takenUp(ticket, trigger) {
			taken = true
			break
		}
	}
	latest := LatestAssignment(ticket)
	createdAt := ""
	if latest != nil {
		createdAt = latest.CreatedAt
	}
	if taken || !laterThan(createdAt, at) {
		return taken
	}
	report := latest.Report
	reportAt := ""
	if report != nil {
		reportAt = report.At
	}
	later := false
	for _, input := range ticket.Inputs {
		if input.Kind != "policy-changed" && laterThan(input.At, reportAt) {
			later = true
		}
	}
	return report == nil || report.State != "ready" || len(ticket.Undelivered) > 0 || later
}

// newAssignment adds a pending Assignment; a failed delivery to the one before it no longer concerns the ticket.
func (p *Tickets) newAssignment(ticket *Ticket) *Assignment {
	assignment := &Assignment{ID: Key("assignment", ticket.ID, itoa(len(ticket.Assignments))), State: "pending", CreatedAt: p.now()}
	ticket.Assignments = append(ticket.Assignments, assignment)
	ticket.Delivery = nil
	return assignment
}

func (p *Tickets) endAssignment(ticket *Ticket, assignment *Assignment, reason string) {
	assignment.State = "ended"
	assignment.EndedAt = p.now()
	assignment.EndedReason = reason
	p.emit("assignment.ended", map[string]any{"ticketId": ticket.ID, "assignmentId": assignment.ID, "reason": reason})
}

// note gives an open ticket's Executor a fact as a note Input.
func (p *Tickets) note(ticketID, text string) Ticket {
	ticket := p.require(ticketID)
	if !LiveTicket(*ticket) {
		refuse("ticketEnded")
	}
	at := p.now()
	p.addInput(ticket, TicketInput{ID: Key("input", ticketID, "note", at, text), Kind: "note", Text: text, At: at})
	return *p.require(ticketID)
}

// followForContext subscribes an open ticket to a Thread its Executor found related, for context.
func (p *Tickets) followForContext(ticketID string, ref ThreadRef) bool {
	if !LiveTicket(*p.require(ticketID)) {
		refuse("ticketEnded")
	}
	if contains(p.thread(ref).Subscriptions, ticketID) {
		return false
	}
	p.subscribe(ticketID, ref)
	thread := p.thread(ref)
	thread.Context = append(thread.Context, ticketID)
	p.repo().Threads().Save(thread)
	return true
}

// subscribe subscribes a ticket to a Thread.
func (p *Tickets) subscribe(ticketID string, ref ThreadRef) {
	thread := p.thread(ref)
	if !contains(thread.Subscriptions, ticketID) {
		thread.Subscriptions = append(thread.Subscriptions, ticketID)
		p.emit("thread.subscribed", map[string]any{"threadKey": thread.Key, "ticketId": ticketID})
	}
	if contains(thread.Context, ticketID) {
		thread.Context = without(thread.Context, ticketID)
	}
	p.repo().Threads().Save(thread)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
