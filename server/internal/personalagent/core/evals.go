package core

// requeues is how many times in a row an expired Eval queues its Triggers again before it is recorded unavailable.
const requeues = 2

type finished struct {
	kind     string
	reason   []Reason
	audit    map[string]any
	ticketID string
	mandate  *Mandate
}

func stronger(a, b Strength) Strength {
	if a == Explicit || b == Explicit {
		return Explicit
	}
	return Inferred
}

// Evals runs the Evals of Threads (SA evals.ts).
type Evals struct {
	base
	tickets *Tickets
}

func addMs(at string, ms int64) string {
	t, _ := ParseTime(at)
	return FormatTime(t + ms)
}

// due expires a Thread's Eval past its deadline, unless processing is paused.
func (p *Evals) due(thread Thread, policy Policy) {
	evaluation := thread.Evaluating
	if evaluation == nil || (policy.Paused() && evaluation.PausedAt != "") {
		return
	}
	now := p.now()
	switch {
	case policy.Paused():
		evaluation.PausedAt = now
	case evaluation.PausedAt != "":
		d, _ := ParseTime(evaluation.Deadline)
		n, _ := ParseTime(now)
		s, _ := ParseTime(evaluation.PausedAt)
		evaluation.Deadline = FormatTime(d + n - s)
		evaluation.PausedAt = ""
	case evaluation.Delivered != "" && evaluation.Deadline <= now:
		p.expire(thread)
		return
	default:
		return
	}
	p.repo().Threads().Save(thread)
}

// delivered records that an Eval's candidate reached the assessor's Session: its deadline counts from now.
func (p *Evals) delivered(evalID string, policy Policy) {
	for _, item := range p.repo().Threads().Active() {
		thread := item
		if thread.Evaluating == nil || thread.Evaluating.ID != evalID {
			continue
		}
		if thread.Evaluating.Delivered != "" {
			return
		}
		now := p.now()
		thread.Evaluating.Delivered = now
		thread.Evaluating.Deadline = addMs(now, policy.EvalTimeoutMs())
		p.repo().Threads().Save(thread)
		return
	}
}

func (p *Evals) expire(thread Thread) {
	expired := thread.Expired + 1
	if expired > requeues {
		p.finish(&thread, *thread.Evaluating, finished{kind: "unavailable", reason: []Reason{{Code: "timeout"}}})
		return
	}
	id, triggerIDs := thread.Evaluating.ID, thread.Evaluating.TriggerIDs
	thread.Evaluating = nil
	thread.Expired = expired
	thread.Pending = append(append([]string{}, triggerIDs...), without(thread.Pending, triggerIDs...)...)
	p.repo().Threads().Save(thread)
	p.options.Assessor.Release(id)
}

type strongest struct {
	trigger  Trigger
	strength Strength
	basis    MandateBasis
}

// start starts one Eval of the Triggers queued on a Thread, judged by the gate, and asks the assessor for it.
func (p *Evals) start(threadKey string, policy Policy) {
	repo := p.repo()
	thread := *repo.Threads().Get(threadKey)
	at := p.now()
	// A person's follow-up a ticket still decides is that ticket's: one an older version queued leaves the queue.
	delivered := func(id string) bool {
		trigger := repo.Triggers().Get(id)
		return trigger != nil && len(p.owners(*trigger)) > 0
	}
	followups := []string{}
	for _, id := range thread.Pending {
		if delivered(id) {
			followups = append(followups, id)
		}
	}
	if len(followups) > 0 {
		thread.Pending = without(thread.Pending, followups...)
		repo.Threads().Save(thread)
		for _, id := range followups {
			p.emit("trigger.followup", map[string]any{"triggerId": id})
		}
		if len(thread.Pending) == 0 {
			return
		}
	}
	// Triggers that already originated a ticket cannot lend authority to a new evaluation or spawn a duplicate ticket.
	accepted := p.accepted(thread.Key)
	started := []string{}
	for _, id := range thread.Pending {
		if _, ok := accepted[id]; ok {
			started = append(started, id)
		}
	}
	if len(started) > 0 {
		reasons := []Reason{}
		for _, id := range started {
			reasons = append(reasons, Reason{Code: "startedTicket", TriggerID: id, Values: map[string]string{"ticket": accepted[id].TicketID}})
		}
		p.ignore(&thread, started, reasons, policy, at)
	}
	triggers := []Trigger{}
	for _, id := range thread.Pending {
		if t := repo.Triggers().Get(id); t != nil {
			triggers = append(triggers, *t)
		}
	}
	tickets := p.ticketsOnThread(thread)
	reasons := []Reason{}
	var best *strongest
	for _, trigger := range triggers {
		gate := policy.Gate(trigger, thread, tickets)
		if !gate.Admissible {
			reasons = append(reasons, Reason{Code: gate.Reason, TriggerID: trigger.ID})
			continue
		}
		later := best != nil && gate.Strength == best.strength && trigger.At >= best.trigger.At
		weaker := best == nil || stronger(best.strength, gate.Strength) != best.strength
		if weaker || later {
			best = &strongest{trigger: trigger, strength: gate.Strength, basis: gate.Basis}
		}
	}
	if best == nil {
		if len(thread.Pending) > 0 {
			if len(reasons) == 0 {
				reasons = []Reason{{Code: "noAdmissible"}}
			}
			p.ignore(&thread, append([]string{}, thread.Pending...), reasons, policy, at)
		}
		repo.Threads().Save(thread)
		return
	}
	evalID := Key(append([]string{"eval", thread.Key, policy.Version(), at}, thread.Pending...)...)
	deadline := addMs(at, policy.EvalTimeoutMs())
	thread.Evaluating = &Evaluation{ID: evalID, ThreadKey: thread.Key, TriggerIDs: thread.Pending, Primary: best.trigger.ID, Strength: best.strength,
		Actor: best.trigger.Actor, Basis: best.basis, PolicyVersion: policy.Version(), StartedAt: at, Deadline: deadline}
	thread.Pending = []string{}
	repo.Threads().Save(thread)
	for _, trigger := range triggers {
		d := p.disposition(trigger.ID)
		if repo.Triggers().Disposition(trigger.ID) == nil {
			d.At = at
		}
		d.EvalID = evalID
		repo.Triggers().SaveDisposition(trigger.ID, d)
	}
	p.emit("eval.started", map[string]any{"evalId": evalID, "threadKey": thread.Key, "triggerIds": thread.Evaluating.TriggerIDs, "strength": best.strength})
	p.options.Assessor.Request(p.candidateOf(thread))
}

// candidate returns the candidate of the Eval that holds triggerID, or nil while no Eval holds it.
func (p *Evals) candidate(triggerID string) *Candidate {
	thread := p.holding(triggerID)
	if thread == nil {
		return nil
	}
	c := p.candidateOf(*thread)
	return &c
}

// decide decides a Trigger: the Eval that holds it, or a direct Eval of it alone when none does.
func (p *Evals) decide(triggerID string, outcome Outcome) Decision {
	repo := p.repo()
	trigger := repo.Triggers().Get(triggerID)
	if trigger == nil {
		refuse("unknownTrigger")
	}
	held := p.holding(triggerID)
	if outcome.EvalID != "" && (held == nil || held.Evaluating.ID != outcome.EvalID) {
		refuse("evalFinished", "eval", outcome.EvalID)
	}
	// Prevent a replayed trigger that already created a ticket from spawning a second one.
	if outcome.Kind == "accept" {
		if earlier, ok := p.accepted(ThreadKey(trigger.Threads[0]))[triggerID]; ok {
			others := []string{}
			if held != nil {
				others = without(held.Evaluating.TriggerIDs, triggerID)
			}
			if held != nil && len(others) > 0 {
				p.reassess(held, Reason{Code: "acceptedBefore", Values: map[string]string{"trigger": triggerID}}, []string{triggerID})
			} else if held != nil {
				p.finish(held, *held.Evaluating, finished{kind: "ignore", reason: []Reason{{Code: "startedTicket", TriggerID: triggerID,
					Values: map[string]string{"ticket": earlier.TicketID}}}})
			}
			return earlier
		}
	}
	// A follow-up that reached a ticket is its worker's to judge: the main Session may only stop the work on it.
	if outcome.Kind != "stop" {
		p.leftToWorkers(*trigger)
	}
	var thread Thread
	if held != nil {
		thread = *held
	} else {
		thread = p.thread(trigger.Threads[0])
	}
	var evaluation Evaluation
	if held != nil {
		evaluation = *held.Evaluating
	} else {
		ticketID := ""
		if outcome.Kind == "stop" {
			ticketID = outcome.TicketID
		}
		evaluation = p.directEval(&thread, *trigger, outcome.Kind, ticketID)
	}
	// An inferred candidate is accepted only on enabled rules its assessment matched, while assessments may accept.
	if outcome.Kind == "accept" && evaluation.Strength != Explicit && !p.policy().AdmitsInferred(append([]string{}, outcome.Rules...)) {
		refuse("notExplicit")
	}
	return p.decideEval(&thread, evaluation, outcome)
}

// reevaluate queues a stored Trigger for a fresh Eval; false while an Eval holds it.
func (p *Evals) reevaluate(triggerID string) bool {
	if !p.requeue(triggerID) {
		return false
	}
	p.emit("trigger.requeued", map[string]any{"triggerId": triggerID})
	return true
}

func (p *Evals) requeue(triggerID string) bool {
	trigger := p.repo().Triggers().Get(triggerID)
	if trigger == nil {
		refuse("unknownTrigger")
	}
	if p.holding(triggerID) != nil {
		return false
	}
	p.queueForEval(*trigger)
	d := p.disposition(triggerID)
	d.Requeued = true
	p.repo().Triggers().SaveDisposition(triggerID, d)
	return true
}

// handback records that a ticket's Executor hands back a Trigger that reached it as beyond the ticket.
func (p *Evals) handback(ticketID, triggerID, reason string) bool {
	ticket := p.require(ticketID)
	reached := false
	for _, input := range ticket.Inputs {
		if input.Kind == "trigger" && input.TriggerID == triggerID {
			reached = true
		}
	}
	if !reached {
		refuse("notReached")
	}
	if d := p.repo().Triggers().Disposition(triggerID); d != nil {
		for _, item := range d.Handbacks {
			if item.TicketID == ticketID {
				refuse("handedBack")
			}
		}
	}
	return p.handBack(ticketID, *p.repo().Triggers().Get(triggerID), reason)
}

// dismiss marks an unavailable Decision dismissed.
func (p *Evals) dismiss(decisionID string) Decision {
	decision := p.repo().Decisions().Get(decisionID)
	if decision == nil || decision.Kind != "unavailable" {
		refuse("unknownProblem")
	}
	decision.Dismissed = true
	p.repo().Decisions().Save(*decision)
	return *decision
}

// withdraw takes a Trigger out of its Thread's queue because it was routed by hand.
func (p *Evals) withdraw(trigger Trigger) {
	thread := p.repo().Threads().Get(ThreadKey(trigger.Threads[0]))
	if thread == nil || !contains(thread.Pending, trigger.ID) {
		return
	}
	thread.Pending = without(thread.Pending, trigger.ID)
	p.repo().Threads().Save(*thread)
}

// routed ends the Eval that holds a Trigger routed to a ticket.
func (p *Evals) routed(triggerID, ticketID string) {
	held := p.holding(triggerID)
	reason := Reason{Code: "routed", Values: map[string]string{"ticket": ticketID}}
	if held != nil && len(held.Evaluating.TriggerIDs) == 1 {
		p.finish(held, *held.Evaluating, finished{kind: "ignore", reason: []Reason{reason}})
	} else if held != nil {
		reason.TriggerID = triggerID
		p.reassess(held, reason, []string{triggerID})
	}
}

// policyChanged starts the Evals in progress again under the new Policy version.
func (p *Evals) policyChanged(from, to string) {
	for _, item := range p.repo().Threads().Active() {
		thread := item
		if thread.Evaluating != nil {
			p.reassess(&thread, Reason{Code: "policyChanged"}, nil)
		}
	}
	p.emit("policy.changed", map[string]any{"from": from, "to": to})
}

// reassess ends a Thread's Eval as unavailable and queues its Triggers, except without, for a new Eval ahead of the others.
func (p *Evals) reassess(thread *Thread, reason Reason, withoutIDs []string) Decision {
	ids := without(thread.Evaluating.TriggerIDs, withoutIDs...)
	decision := p.finish(thread, *thread.Evaluating, finished{kind: "unavailable", reason: []Reason{reason}})
	current := *p.repo().Threads().Get(thread.Key)
	current.Pending = append(append([]string{}, ids...), without(current.Pending, ids...)...)
	p.repo().Threads().Save(current)
	return decision
}

// holding returns the Thread whose Eval holds a Trigger.
func (p *Evals) holding(triggerID string) *Thread {
	trigger := p.repo().Triggers().Get(triggerID)
	if trigger == nil {
		return nil
	}
	thread := p.repo().Threads().Get(ThreadKey(trigger.Threads[0]))
	if thread != nil && thread.Evaluating != nil && contains(thread.Evaluating.TriggerIDs, triggerID) {
		return thread
	}
	return nil
}

func (p *Evals) candidateOf(thread Thread) Candidate {
	evaluation := thread.Evaluating
	triggers := []Trigger{}
	for _, id := range evaluation.TriggerIDs {
		if t := p.repo().Triggers().Get(id); t != nil {
			triggers = append(triggers, *t)
		}
	}
	return Candidate{EvalID: evaluation.ID, Thread: clone(thread), Triggers: triggers, Strength: evaluation.Strength, PolicyVersion: evaluation.PolicyVersion,
		Tickets: p.ticketsOnThread(thread)}
}

// ignore records that the queued Triggers ids of a Thread start no Eval, and takes them out of its queue.
func (p *Evals) ignore(thread *Thread, ids []string, reason []Reason, policy Policy, at string) {
	evaluation := Evaluation{ID: Key(append([]string{"eval", thread.Key, "ignored", policy.Version(), at}, ids...)...), ThreadKey: thread.Key,
		TriggerIDs: ids, Strength: Inferred, Actor: ActorRef{Source: thread.Ref.Source}, Basis: MandateBasis{Via: "followup"},
		PolicyVersion: policy.Version(), StartedAt: at, Deadline: at}
	p.record(thread, evaluation, finished{kind: "ignore", reason: reason})
	thread.Pending = without(thread.Pending, ids...)
	thread.Expired = 0
}

// accepted returns the accept Decisions of the Triggers whose primary Thread is threadKey, by Trigger.
func (p *Evals) accepted(threadKey string) map[string]Decision {
	out := map[string]Decision{}
	for _, decision := range p.repo().Decisions().ByThread(threadKey) {
		if decision.Kind != "accept" || decision.TicketID == "" {
			continue
		}
		for _, id := range decision.TriggerIDs {
			if _, ok := out[id]; !ok {
				out[id] = decision
			}
		}
	}
	return out
}

// directEval is a gate-only Eval of one Trigger, for an outcome decided without a queued Eval.
func (p *Evals) directEval(thread *Thread, trigger Trigger, kind, ticketID string) Evaluation {
	policy := p.policy()
	gate := policy.Gate(trigger, *thread, p.ticketsOnThread(*thread))
	if !gate.Admissible {
		refuse("notAdmissible", "reason", gate.Reason)
	}
	at := p.now()
	if contains(thread.Pending, trigger.ID) {
		thread.Pending = without(thread.Pending, trigger.ID)
		p.repo().Threads().Save(*thread)
	}
	parts := []string{"eval", thread.Key, "direct", trigger.ID, kind}
	if ticketID != "" {
		parts = append(parts, ticketID)
	}
	return Evaluation{ID: Key(append(parts, at)...), ThreadKey: thread.Key, TriggerIDs: []string{trigger.ID}, Primary: trigger.ID,
		Strength: gate.Strength, Actor: trigger.Actor, Basis: gate.Basis, PolicyVersion: policy.Version(), StartedAt: at, Deadline: at}
}

// decideEval applies an outcome to an Eval of a Thread: the one in progress, or a gate-only one.
func (p *Evals) decideEval(thread *Thread, evaluation Evaluation, outcome Outcome) Decision {
	// Requeue if Policy changed without a live policyChanged call (such as across an upgrade or restart).
	if p.policy().Version() != evaluation.PolicyVersion {
		if thread.Evaluating != nil && thread.Evaluating.ID == evaluation.ID {
			return p.reassess(thread, Reason{Code: "policyChanged"}, nil)
		}
		return p.finish(thread, evaluation, finished{kind: "unavailable", reason: []Reason{{Code: "policyChanged"}}, audit: outcome.Audit})
	}
	switch outcome.Kind {
	case "ignore", "unavailable":
		reasons := []Reason{}
		// An inferred candidate ignored without an assessment, from its card, says so beside the reason given.
		if outcome.Kind == "ignore" && evaluation.Strength != Explicit && outcome.Audit == nil {
			reasons = append(reasons, Reason{Code: "ignoredFromCard"})
		}
		return p.finish(thread, evaluation, finished{kind: outcome.Kind, reason: append(reasons, outcome.reasons()...), audit: outcome.Audit})
	case "stop":
		ticket := p.repo().Tickets().Get(outcome.TicketID)
		if ticket == nil || !LiveTicket(*ticket) {
			return p.finish(thread, evaluation, finished{kind: "ignore", reason: []Reason{{Code: "stopUnknown"}}, audit: outcome.Audit})
		}
		if evaluation.Strength != Explicit {
			return p.finish(thread, evaluation, finished{kind: "ignore", reason: []Reason{{Code: "stopNotExplicit"}}, audit: outcome.Audit})
		}
		decision := p.finish(thread, evaluation, finished{kind: "stop", reason: outcome.reasons(), ticketID: ticket.ID, audit: outcome.Audit})
		reason := ""
		if outcome.ReasonCode == nil {
			reason = outcome.Said
		}
		p.tickets.end(ticket.ID, "human", "stopped", p.now(), reason)
		return decision
	}
	return p.accept(thread, evaluation, outcome)
}

// accept records an accept with the Mandate the Eval gives, and has Tickets create its ticket.
func (p *Evals) accept(thread *Thread, evaluation Evaluation, outcome Outcome) Decision {
	at := p.now()
	basis := clone(evaluation.Basis)
	if outcome.Rules != nil {
		basis.Rules = append([]string{}, outcome.Rules...)
	}
	mandate := Mandate{Strength: evaluation.Strength, PolicyVersion: evaluation.PolicyVersion, Basis: basis, Actor: evaluation.Actor, Scope: outcome.Scope}
	ticketID := Key("ticket", evaluation.ID)
	decision := p.finish(thread, evaluation, finished{kind: "accept", reason: outcome.reasons(), audit: outcome.Audit, mandate: &mandate, ticketID: ticketID})
	p.tickets.create(ticketID, decision.ID, mandate, evaluation, outcome, at)
	return decision
}

// finish records an Eval's Decision; the Thread's own Eval ends with it, while a gate-only one leaves it going.
func (p *Evals) finish(thread *Thread, evaluation Evaluation, outcome finished) Decision {
	decision := p.record(thread, evaluation, outcome)
	if thread.Evaluating == nil || thread.Evaluating.ID != evaluation.ID {
		return decision
	}
	thread.Evaluating = nil
	thread.Expired = 0
	p.repo().Threads().Save(*thread)
	p.options.Assessor.Release(evaluation.ID)
	return decision
}

func (p *Evals) record(thread *Thread, evaluation Evaluation, outcome finished) Decision {
	at := p.now()
	decision := Decision{ID: Key("decision", evaluation.ID), EvalID: evaluation.ID, ThreadKey: thread.Key, TriggerIDs: evaluation.TriggerIDs,
		PolicyVersion: evaluation.PolicyVersion, At: at, Kind: outcome.kind, Reason: outcome.reason, Strength: evaluation.Strength,
		TicketID: outcome.ticketID, Mandate: outcome.mandate, Audit: outcome.audit}
	p.repo().Decisions().Save(decision)
	for _, id := range evaluation.TriggerIDs {
		d := p.disposition(id)
		d.EvalID, d.DecisionID = evaluation.ID, decision.ID
		p.repo().Triggers().SaveDisposition(id, d)
	}
	p.emit("decision.recorded", map[string]any{"decisionId": decision.ID, "kind": decision.Kind, "threadKey": thread.Key})
	return decision
}
