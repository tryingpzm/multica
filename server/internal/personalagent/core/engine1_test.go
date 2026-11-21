package core_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// tests/core-engine.spec.ts — describe('intake'), ('eval'), ('subscription fan-out'), ('resume and stop').

func TestIntakeStoresOnceAndRecordsSourceFiltering(t *testing.T) {
	h := newHarness(t)
	eq(t, h.receive(trigger("1")), "stored")
	eq(t, h.receive(trigger("1")), "duplicate")
	eq(t, h.receive(trigger("2", meta(map[string]any{"blocked": true}))), "filtered")
	eq(t, h.disposition("2").Filtered, &core.Reason{Code: "chat not allowed"})
	eq(t, h.pending("chat:t1"), []string{"1"})
}

func TestIntakeCorrelatesThreadsAndEvaluatesOnPrimary(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1", threads("gh:pr-7", "gh:run-9")))
	eq(t, h.thread("gh:pr-7").Pending, []string{"1"})
	eq(t, h.thread("gh:pr-7").Correlated, []string{"gh:run-9"})
	eq(t, h.thread("gh:run-9").Pending, []string{})
	eq(t, h.thread("gh:run-9").Correlated, []string{"gh:pr-7"})
}

func TestEvalIgnoresAtGateWithoutAssessor(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1", bot()))
	h.pump()
	eq(t, len(h.requests), 0)
	ds := h.decisions("chat:t1")
	eq(t, len(ds), 1)
	eq(t, []any{ds[0].Kind, ds[0].TriggerIDs}, []any{"ignore", []string{"1"}})
	if h.disposition("1").DecisionID == "" {
		t.Fatal("decisionId not recorded")
	}
}

func TestEvalReleasesAssessorForEveryEndedEval(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	h.mustDecide(h.requests[0], core.Outcome{Kind: "ignore", Said: "chatter"})
	eq(t, h.released, []string{h.requests[0].EvalID})
}

func TestEvalSerializesPerThreadAndDebounces(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	h.receive(trigger("2"))
	h.receive(trigger("3"))
	h.pump()
	eq(t, len(h.requests), 1)
	eq(t, h.pending("chat:t1"), []string{"2", "3"})
	h.mustDecide(h.requests[0], core.Outcome{Kind: "ignore", Said: "chatter"})
	h.pump()
	eq(t, triggerIDs(h.requests[1]), []string{"2", "3"})
	eq(t, h.requests[1].Strength, "explicit")
}

func TestAcceptCreatesTicketWithMandateSubscriptionAckAndAssignment(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	ticket := h.ticket(decision.TicketID)
	eq(t, ticket.Title, "Fix")
	eq(t, ticket.Mandate, core.Mandate{Strength: "explicit", PolicyVersion: "v1", Basis: core.MandateBasis{Via: "listed"}, Actor: member, Scope: "small"})
	eq(t, ticket.Channels, []core.ChannelRef{*trigger("1").Channel})
	eq(t, ticket.Origin, core.Origin{TriggerID: "1", TriggerIDs: []string{"1"}})
	if ticket.Ended != nil {
		t.Fatal("ticket ended")
	}
	eq(t, len(ticket.Assignments), 1)
	eq(t, ticket.Assignments[0].State, "running")
	if ticket.Assignments[0].StartedAt == "" {
		t.Fatal("no startedAt")
	}
	eq(t, h.thread("chat:t1").Subscriptions, []string{ticket.ID})
	eq(t, h.outbox, []core.OutboundMessage{{ID: core.Key("ack", "1"), Channel: *trigger("1").Channel, Text: "ok", Kind: "acknowledgement", TicketID: ticket.ID}})
	eq(t, len(h.starts), 1)
}

func TestEvalRefusesReplacedEvalAndDecidesLaterOne(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	first := h.requests[0]
	h.version = "v2"
	h.ok(h.engine.PolicyChanged("v1", "v2", nil))
	h.pump()
	second := h.requests[1]
	_, err := h.decide(first, core.Outcome{Kind: "ignore", Said: "chatter"})
	userErr(t, err, "evalFinished")
	d := h.mustDecide(second, core.Outcome{Kind: "ignore", Said: "chatter"})
	eq(t, []string{d.Kind, d.EvalID}, []string{"ignore", second.EvalID})
}

func TestAcceptIsAllOrNothing(t *testing.T) {
	h := newHarnessWith(t, nil, func(core.Ticket, core.Outcome) core.Ticket { panic("No directory for the ticket") })
	h.receive(trigger("1"))
	h.pump()
	evalID := h.requests[0].EvalID
	o := accept("Fix")
	o.Acknowledgement = "ok"
	_, err := h.decide(h.requests[0], o)
	if err == nil || err.Error() != "No directory for the ticket" {
		t.Fatalf("want No directory, got %v", err)
	}
	eq(t, len(h.decisions("chat:t1")), 0)
	eq(t, len(h.store.Tickets().List()), 0)
	eq(t, h.thread("chat:t1").Evaluating.ID, evalID)
	eq(t, h.thread("chat:t1").Subscriptions, []string{})
	eq(t, len(h.outbox), 0)
}

func TestEvalUnavailableWhenPolicyVersionChangedDuringAssessment(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	h.version = "v2"
	d := h.mustDecide(h.requests[0], core.Outcome{Kind: "accept", Title: "x", Scope: "x", Said: "x"})
	eq(t, d.Kind, "unavailable")
	eq(t, len(h.store.Tickets().List()), 0)
}

func TestEvaluateDueStartsEveryPendingEvalOnce(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1", threads("A")))
	h.receive(trigger("2", threads("B")))
	h.ok(h.engine.EvaluateDue())
	h.ok(h.engine.EvaluateDue())
	keys := []string{}
	for _, c := range h.requests {
		keys = append(keys, c.Thread.Key)
	}
	eq(t, keys, []string{"chat:A", "chat:B"})
}

func TestEvalDeadlineFromDeliveryRequeuesTwiceThenUnavailable(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	// A candidate the assessor's Session has not taken does not expire.
	h.tick(600)
	h.pump()
	eq(t, h.engine.Candidate("1").EvalID, h.requests[0].EvalID)
	deliver := func() { h.ok(h.engine.CandidateDelivered(h.last().EvalID)) }
	deliver()
	h.tick(59)
	h.pump()
	eq(t, len(h.requests), 1)
	// Expired, it records nothing and queues the Trigger for a fresh Eval, twice.
	h.tick(2)
	h.pump()
	eq(t, len(h.decisions("chat:t1")), 0)
	eq(t, h.released, []string{h.requests[0].EvalID})
	eq(t, [][]string{triggerIDs(h.requests[0]), triggerIDs(h.requests[1])}, [][]string{{"1"}, {"1"}})
	deliver()
	h.tick(61)
	h.pump()
	eq(t, len(h.requests), 3)
	// The third expiry is recorded unavailable.
	deliver()
	h.tick(61)
	h.pump()
	ds := h.decisions("chat:t1")
	eq(t, len(ds), 1)
	eq(t, ds[0].Kind, "unavailable")
	eq(t, ds[0].Reason, []core.Reason{{Code: "timeout"}})
	th := h.thread("chat:t1")
	eq(t, th.Pending, []string{})
	if th.Evaluating != nil || th.Expired != 0 {
		t.Fatal("thread still evaluating or expired")
	}
}

func TestFanoutDeliversPersonsFollowupToLiveTicketNotToEval(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	requests := len(h.requests)
	h.receive(trigger("2", actor(guest)))
	h.pump()
	ticket := h.ticket(decision.TicketID)
	eq(t, len(ticket.Inputs), 1)
	eq(t, []string{ticket.Inputs[0].Kind, ticket.Inputs[0].TriggerID}, []string{"trigger", "2"})
	eq(t, ticket.Undelivered, []string{})
	eq(t, h.delivered, [][2]string{{ticket.ID, ticket.Inputs[0].ID}})
	d := h.disposition("2")
	eq(t, d.Fanout, []string{ticket.ID})
	eq(t, []any{d.Context, d.EvalID, d.DecisionID, d.Requeued}, []any{nil, "", "", false})
	// No Eval, so no Decision and no timeout, and the Thread's queue stays free for a new request.
	eq(t, []any{len(h.requests), h.pending("chat:t1"), len(h.decisions("chat:t1"))}, []any{requests, []string{}, 1})
	h.tick(600)
	h.ok(h.engine.EvaluateDue())
	eq(t, len(h.decisions("chat:t1")), 1)
	// A bot's message there still goes to Eval, which the gate does not admit.
	h.receive(trigger("3", bot()))
	h.pump()
	last := lastDecision(h.decisions("chat:t1"))
	eq(t, []any{last.Kind, last.TriggerIDs}, []any{"ignore", []string{"3"}})
}

func TestFanoutKeepsInputsUndeliveredUntilRunningAndRelaunches(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	decision := h.mustDecide(h.requests[0], accept("Fix"))
	h.settle(false)
	h.receive(trigger("2"))
	h.ok(h.engine.EvaluateDue())
	h.settle(false)
	ticket := h.ticket(decision.TicketID)
	eq(t, len(ticket.Undelivered), 1)
	eq(t, len(h.delivers), 0)
	eq(t, len(h.starts), 2)
	h.ok(h.engine.AssignmentStarted(ticket.ID, ticket.Assignments[0].ID))
	h.settle()
	eq(t, h.delivered, [][2]string{{ticket.ID, ticket.Undelivered[0]}})
}

func TestFanoutDoesNotHandBotMessageToEndedTicket(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	h.report(decision.TicketID, "ready", "done")
	h.receive(trigger("2", bot()))
	h.pump()
	ticket := h.ticket(decision.TicketID)
	if ticket.Ended == nil {
		t.Fatal("not ended")
	}
	eq(t, ticket.Undelivered, []string{})
	eq(t, len(h.starts), 1)
}

func TestFanoutResumesOnceAndDropsWhatWaitedWhenMandateRevoked(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	h.report(decision.TicketID, "ready", "done")
	h.version = "revoked"
	h.receive(trigger("2", actor(guest)))
	h.pump()
	ticket := h.ticket(decision.TicketID)
	if ticket.Ended == nil || len(ticket.Undelivered) != 0 {
		t.Fatal("want ended with nothing undelivered")
	}
	h.version = "v1"
	h.pump()
	if h.ticket(decision.TicketID).Ended == nil {
		t.Fatal("resumed without a message")
	}
	h.receive(trigger("3", actor(guest)))
	h.pump()
	h.pump()
	ticket = h.ticket(decision.TicketID)
	eq(t, len(ticket.Assignments), 2)
	n := 0
	for _, k := range kinds(ticket) {
		if k == "resumed" {
			n++
		}
	}
	eq(t, n, 1)
}

func TestFanoutTakesDeliveryReportsOnlyFromRunningAssignment(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	assignment := h.ticket(ticketID).Assignments[0].ID
	h.receive(trigger("2"))
	input := core.Key("input", ticketID, "trigger", "2")
	h.ok(h.engine.DeliveryFailed(ticketID, assignment, "Session busy"))
	eq(t, h.ticket(ticketID).Delivery.Error, "Session busy")
	h.ok(h.engine.DeliveryFailed(ticketID, "another", "other"))
	h.ok(h.engine.InputsDelivered(ticketID, "another", []string{input}))
	eq(t, h.ticket(ticketID).Undelivered, []string{input})
	eq(t, h.ticket(ticketID).Delivery.Error, "Session busy")
	h.ok(h.engine.InputsDelivered(ticketID, assignment, []string{input}))
	eq(t, h.ticket(ticketID).Undelivered, []string{})
	if h.ticket(ticketID).Delivery != nil {
		t.Fatal("delivery kept")
	}
	h.receive(trigger("3"))
	_, err := h.engine.StopTicket(ticketID, "")
	h.ok(err)
	h.ok(h.engine.DeliveryFailed(ticketID, assignment, "late"))
	ticket := h.ticket(ticketID)
	if ticket.Ended == nil || len(ticket.Undelivered) != 0 || ticket.Delivery != nil {
		t.Fatal("want ended, nothing undelivered, no delivery")
	}
}

func TestUnsubscribingStopsFanout(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	dropped, err := h.engine.Unsubscribe("chat:t1")
	h.ok(err)
	eq(t, dropped, true)
	h.receive(trigger("2"))
	h.pump()
	eq(t, len(h.ticket(decision.TicketID).Inputs), 0)
}

func TestExecutorDropsAttachedThreadButNotItsOwn(t *testing.T) {
	h := newHarness(t)
	first := h.accepted("1", "t1").TicketID
	second := h.accepted("2", "t2").TicketID
	h.receive(trigger("3", threads("t2")))
	// A follow-up of the second ticket goes to the first only once the second's worker hands it back.
	_, err := h.engine.Handback(second, "3", "It is about the first fix")
	h.ok(err)
	_, err = h.engine.Attach(first, "3", "")
	h.ok(err)
	eq(t, h.thread("chat:t2").Subscriptions, []string{second, first})
	h.events = nil
	h.ok(h.engine.MarkUnrelated(first, "chat:t2", "about another fix"))
	eq(t, h.thread("chat:t2").Subscriptions, []string{second})
	unsub := []map[string]any{}
	for _, e := range h.events {
		if e.Type == "thread.unsubscribed" {
			unsub = append(unsub, e.Data)
		}
	}
	eq(t, unsub, []map[string]any{{"threadKey": "chat:t2", "ticketId": first, "by": "executor", "reason": "about another fix"}})
	userErr(t, h.engine.MarkUnrelated(first, "chat:t2", "again"), "notFollowing")
	userErr(t, h.engine.MarkUnrelated(first, "chat:t1", "its own"), "sourceThread")
	_, err = h.engine.BindThread(first, core.ThreadRef{Source: "gh", ID: "pr-9"}, true)
	h.ok(err)
	userErr(t, h.engine.MarkUnrelated(first, "gh:pr-9", "its PR"), "followedThread")
}

func endedTicket(h *harness, by string) string {
	h.t.Helper()
	d := h.accepted("1", "t1")
	if by == "executor" {
		h.report(d.TicketID, "ready", "done")
	} else {
		_, err := h.engine.StopTicket(d.TicketID, "")
		h.ok(err)
	}
	return d.TicketID
}

func TestResumeEndedTicketOnAnyPersonsMessageWithoutDecision(t *testing.T) {
	h := newHarness(t)
	ticketID := endedTicket(h, "executor")
	decisions := len(h.decisions("chat:t1"))
	// It resumes as the message arrives, before any Eval of it.
	h.receive(trigger("2", actor(guest)))
	if h.ticket(ticketID).Ended != nil {
		t.Fatal("not resumed")
	}
	h.pump()
	ticket := h.ticket(ticketID)
	eq(t, len(ticket.Assignments), 2)
	eq(t, ticket.Mandate.PolicyVersion, "v1")
	eq(t, kinds(ticket), []string{"trigger", "resumed"})
	eq(t, len(h.starts), 2)
	n := 0
	for _, d := range h.decisions("chat:t1") {
		if d.Kind != "ignore" {
			n++
		}
	}
	eq(t, n, decisions)
}

func TestResumeEndedTicketLeftWithWaitingMessageOnNextPass(t *testing.T) {
	h := newHarness(t)
	ticketID := endedTicket(h, "executor")
	h.receive(trigger("2", actor(guest), threads("elsewhere")))
	stored := h.ticket(ticketID)
	stored.Inputs = append(stored.Inputs, &core.TicketInput{ID: "waiting", Kind: "trigger", TriggerID: "2", At: stored.UpdatedAt, Human: true})
	stored.Undelivered = []string{"waiting"}
	h.store.Tickets().Save(*stored)
	h.pump()
	ticket := h.ticket(ticketID)
	if ticket.Ended != nil || !contains(kinds(ticket), "resumed") {
		t.Fatal("not resumed")
	}
}

func TestResumeEndedTicketAtOnceWhenAttached(t *testing.T) {
	h := newHarness(t)
	ticketID := endedTicket(h, "executor")
	h.receive(trigger("2", actor(guest), threads("elsewhere")))
	subject, err := h.engine.Attach(ticketID, "2", "")
	h.ok(err)
	eq(t, len(subject.Assignments), 2)
	eq(t, subject.Assignments[1].State, "pending")
	if subject.Ended != nil {
		t.Fatal("still ended")
	}
}

func TestStoppedTicketStaysEnded(t *testing.T) {
	h := newHarness(t)
	ticketID := endedTicket(h, "human")
	eq(t, len(h.stops), 1)
	h.receive(trigger("2"))
	h.pump()
	eq(t, h.ticket(ticketID).Ended.By, "human")
	eq(t, len(h.starts), 1)
}

func TestTicketStaysEndedWhenMandateFailsPolicy(t *testing.T) {
	h := newHarness(t)
	ticketID := endedTicket(h, "executor")
	h.version = "revoked"
	h.receive(trigger("2"))
	h.pump()
	if h.ticket(ticketID).Ended == nil {
		t.Fatal("resumed")
	}
}

func TestStopOnlyOnExplicitInstruction(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	h.receive(trigger("2", actor(guest)))
	h.pump()
	d, err := h.engine.Decide("2", core.Outcome{Kind: "stop", TicketID: decision.TicketID, Said: "x"})
	h.ok(err)
	eq(t, []any{d.Kind, d.Strength, d.Reason}, []any{"ignore", "inferred", []core.Reason{{Code: "stopNotExplicit"}}})
	h.receive(trigger("3"))
	h.pump()
	d, err = h.engine.Decide("3", core.Outcome{Kind: "stop", TicketID: decision.TicketID, Said: "human said stop"})
	h.ok(err)
	eq(t, d.Kind, "stop")
	eq(t, h.ticket(decision.TicketID).Ended.By, "human")
	eq(t, len(h.stops), 1)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
