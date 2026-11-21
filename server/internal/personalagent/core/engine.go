package core

import (
	"errors"
	"fmt"
)

// Engine is the single transactional entry point for mutating Triggers, Threads, Decisions, Tickets and Assignments.
// Every command runs in one Repositories.Atomic unit; a refused command rolls back and returns a *UserError.
type Engine struct {
	options     *EngineOptions
	replies     *Replies
	tickets     *Tickets
	assignments *Assignments
	evals       *Evals
	intake      *Intake
}

// NewEngine builds an Engine on options.
func NewEngine(options EngineOptions) *Engine {
	o := &options
	e := &Engine{options: o}
	e.replies = &Replies{base{o}}
	e.tickets = &Tickets{base{o}, e.replies}
	e.assignments = &Assignments{base{o}, e.tickets, e.replies}
	e.evals = &Evals{base{o}, e.tickets}
	e.intake = &Intake{base{o}, e.tickets}
	return e
}

// atomic runs work in one transaction and turns a panic into a returned error.
func (e *Engine) atomic(work func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			switch v := r.(type) {
			case *UserError:
				err = v
			case error:
				err = v
			default:
				err = errors.New(fmt.Sprint(v))
			}
		}
	}()
	e.options.Repo.Atomic(work)
	return nil
}

// Receive is a Source's request to take in a Trigger: "stored", "duplicate" or "filtered".
func (e *Engine) Receive(trigger Trigger) (result string, err error) {
	err = e.atomic(func() { result = e.intake.Receive(trigger) })
	return
}

// BindThread makes an open ticket follow one of its Source's Threads, own when the ticket opened it.
func (e *Engine) BindThread(ticketID string, thread ThreadRef, own bool) (ticket Ticket, err error) {
	err = e.atomic(func() { ticket = e.tickets.bind(ticketID, thread, own) })
	return
}

// Subscribe makes a ticket receive a Thread's Triggers without following it: the Thread's closing does not end the
// ticket. A ticket's own Multica issue is such a Thread (sources/multica/kind.go).
func (e *Engine) Subscribe(ticketID string, thread ThreadRef) error {
	return e.atomic(func() {
		p := e.tickets
		p.require(ticketID)
		p.subscribe(ticketID, thread)
	})
}

// ThreadClosed is a Source's fact that a Thread closed, at the time it happened.
func (e *Engine) ThreadClosed(thread ThreadRef, outcome, at string) error {
	return e.atomic(func() { e.tickets.threadClosed(thread, outcome, ISOTime(at)) })
}

// EvaluateDue expires or requeues overdue Evals, resumes ended tickets a person's message reached while paused, and
// starts the Evals of Threads whose queued Triggers wait. Each Eval and each ticket update takes one transaction.
func (e *Engine) EvaluateDue() (err error) {
	// Reads between the transactions can fail too, such as a lost database connection.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	policy := e.options.Policy()
	repo := e.options.Repo
	for _, thread := range repo.Threads().Active() {
		if thread.Evaluating != nil {
			t := thread
			if err := e.atomic(func() { e.evals.due(t, policy) }); err != nil {
				return err
			}
		}
	}
	if policy.Paused() {
		return nil
	}
	for _, ticket := range repo.Tickets().Active() {
		if !LiveTicket(ticket) && len(ticket.Undelivered) > 0 {
			id := ticket.ID
			if err := e.atomic(func() { e.tickets.wake(id) }); err != nil {
				return err
			}
		}
	}
	for _, thread := range repo.Threads().Active() {
		if len(thread.Pending) > 0 && thread.Evaluating == nil {
			k := thread.Key
			if err := e.atomic(func() { e.evals.start(k, policy) }); err != nil {
				return err
			}
		}
	}
	return nil
}

// Candidate returns the candidate of the Eval that holds a Trigger, or nil while no Eval holds it.
func (e *Engine) Candidate(triggerID string) (c *Candidate) {
	defer func() {
		if recover() != nil {
			c = nil
		}
	}()
	return e.evals.candidate(triggerID)
}

// Decide decides a Trigger: the Eval that holds it, or a direct Eval of it alone when none does.
func (e *Engine) Decide(triggerID string, outcome Outcome) (decision Decision, err error) {
	err = e.atomic(func() { decision = e.evals.decide(triggerID, outcome) })
	return
}

// Attach attaches a Trigger to a ticket that the message points at.
func (e *Engine) Attach(ticketID, triggerID, note string) (ticket Ticket, err error) {
	err = e.atomic(func() {
		// A message this ticket's worker handed back is not its work.
		if d := e.options.Repo.Triggers().Disposition(triggerID); d != nil {
			for _, item := range d.Handbacks {
				if item.TicketID == ticketID {
					refuse("handedBackHere")
				}
			}
		}
		trigger := e.tickets.attachable(ticketID, triggerID)
		e.evals.withdraw(trigger)
		e.tickets.attach(ticketID, trigger, note)
		e.evals.routed(triggerID, ticketID)
		e.tickets.wake(ticketID)
		ticket = *e.options.Repo.Tickets().Get(ticketID)
	})
	return
}

// RequestStatus asks a running worker that waits in waiting state for its status.
func (e *Engine) RequestStatus(ticketID string) (asked bool, err error) {
	err = e.atomic(func() { asked = e.tickets.requestStatus(ticketID) })
	return
}

// Reply queues a message for the Channel its purpose names.
func (e *Engine) Reply(request ReplyRequest) (reply Reply, err error) {
	err = e.atomic(func() { reply = e.replies.reply(request) })
	return
}

// CandidateDelivered records that the assessor delivered an Eval's candidate to its Session.
func (e *Engine) CandidateDelivered(evalID string) error {
	return e.atomic(func() { e.evals.delivered(evalID, e.options.Policy()) })
}

// Reevaluate queues a stored Trigger for a fresh Eval; false while an Eval holds it.
func (e *Engine) Reevaluate(triggerID string) (queued bool, err error) {
	err = e.atomic(func() { queued = e.evals.reevaluate(triggerID) })
	return
}

// Dismiss dismisses the problem an unavailable Decision shows.
func (e *Engine) Dismiss(decisionID string) (decision Decision, err error) {
	err = e.atomic(func() { decision = e.evals.dismiss(decisionID) })
	return
}

// Unsubscribe drops every ticket's Subscription to a Thread.
func (e *Engine) Unsubscribe(thread string) (dropped bool, err error) {
	err = e.atomic(func() { dropped = e.tickets.dropSubscriptions(thread, "person", "", "") })
	return
}

// StopTicket is a person stopping a ticket, which withdraws its Mandate.
func (e *Engine) StopTicket(ticketID, reason string) (ticket Ticket, err error) {
	err = e.atomic(func() { ticket = e.tickets.stop(ticketID, reason) })
	return
}

// RetryTicket is a person retrying a ticket whose worker reported failed or blocked, or whose input delivery failed.
func (e *Engine) RetryTicket(ticketID string) (ticket Ticket, err error) {
	err = e.atomic(func() { ticket = e.tickets.retry(ticketID) })
	return
}

// AssignmentStarted is the Executor's fact that an Assignment runs.
func (e *Engine) AssignmentStarted(ticketID, assignmentID string) error {
	return e.atomic(func() { e.assignments.started(ticketID, assignmentID) })
}

// InputsDelivered is the Executor's fact that Inputs reached the Assignment that runs the ticket.
func (e *Engine) InputsDelivered(ticketID, assignmentID string, inputIDs []string) error {
	return e.atomic(func() { e.assignments.delivered(ticketID, assignmentID, inputIDs) })
}

// InputsRead is the Executor's fact that the worker's Session took Inputs into a step.
func (e *Engine) InputsRead(ticketID string, inputIDs []string) error {
	return e.atomic(func() { e.assignments.read(ticketID, inputIDs) })
}

// TurnEnded is the Executor's fact that a turn of the worker of the running Assignment ended.
func (e *Engine) TurnEnded(ticketID, assignmentID string) error {
	return e.atomic(func() { e.assignments.turnEnded(ticketID, assignmentID) })
}

// DeliveryFailed is the Executor's fact that delivering Inputs to the running Assignment failed.
func (e *Engine) DeliveryFailed(ticketID, assignmentID, message string) error {
	return e.atomic(func() { e.assignments.deliveryFailed(ticketID, assignmentID, message) })
}

// MarkUnrelated is a worker's request to mark a Thread attached to its ticket unrelated, for reason.
func (e *Engine) MarkUnrelated(ticketID, thread, reason string) error {
	return e.atomic(func() { e.tickets.markUnrelated(ticketID, thread, reason) })
}

// FollowForContext subscribes an open ticket to a Thread its Executor found related, for context.
func (e *Engine) FollowForContext(ticketID string, thread ThreadRef) (followed bool, err error) {
	err = e.atomic(func() { followed = e.tickets.followForContext(ticketID, thread) })
	return
}

// Note gives an open ticket's Executor a fact from the main Session.
func (e *Engine) Note(ticketID, text string) (ticket Ticket, err error) {
	err = e.atomic(func() { ticket = e.tickets.note(ticketID, text) })
	return
}

// Handback is an Executor's hand-back of a message that reached its ticket but asks for more than it.
func (e *Engine) Handback(ticketID, triggerID, reason string) (queued bool, err error) {
	err = e.atomic(func() { queued = e.evals.handback(ticketID, triggerID, reason) })
	return
}

// Report records a report on an open ticket's current Assignment.
func (e *Engine) Report(ticketID string, report WorkerReport) (ticket Ticket, err error) {
	err = e.atomic(func() { ticket = e.assignments.report(ticketID, report) })
	return
}

// AssignmentInterrupted handles an assignment that was running when the host stopped.
func (e *Engine) AssignmentInterrupted(ticketID, assignmentID string) error {
	return e.atomic(func() { e.assignments.interrupted(ticketID, assignmentID) })
}

// PolicyChanged tells each open ticket in effects how the change bears on it, and restarts Evals in progress.
func (e *Engine) PolicyChanged(from, to string, effects map[string]PolicyEffect) error {
	return e.atomic(func() {
		e.tickets.policyChanged(from, to, effects)
		e.evals.policyChanged(from, to)
	})
}
