package core_test

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// tests/core-engine.spec.ts — interrupted assignments, pause, policy change, worker reports, outcomes, attach, direct
// decisions, automatic input, replies.

func TestInterruptedRestartsMidWorkAndHandsWaitingOneItsInputs(t *testing.T) {
	h := newHarness(t)
	interrupted := h.accepted("1", "t1").TicketID
	first := *h.ticket(interrupted).Assignments[0]
	waiting := h.accepted("2", "t2").TicketID
	h.report(waiting, "waiting", "done for now")
	h.receive(trigger("3", threads("t2")))
	h.starts, h.delivers = nil, nil
	for _, id := range []string{interrupted, waiting} {
		h.ok(h.engine.AssignmentInterrupted(id, core.RunningAssignment(h.ticket(id)).ID))
	}
	h.ok(h.engine.AssignmentInterrupted(interrupted, first.ID))
	ticket := h.ticket(interrupted)
	eq(t, len(ticket.Assignments), 2)
	eq(t, []string{ticket.Assignments[0].ID, ticket.Assignments[0].State, ticket.Assignments[0].EndedReason}, []string{first.ID, "ended", "hostRestarted"})
	eq(t, ticket.Assignments[1].State, "pending")
	eq(t, len(ticket.Inputs), 1)
	if ticket.Inputs[0].Kind != "text" || !strings.Contains(ticket.Inputs[0].Text, "The host restarted") {
		t.Fatal("no recovery note")
	}
	eq(t, ticket.Undelivered, []string{ticket.Inputs[0].ID})
	eq(t, len(h.starts), 1)
	eq(t, []any{h.starts[0].ticket.ID, h.starts[0].assignment}, []any{interrupted, *ticket.Assignments[1]})
	eq(t, len(h.ticket(waiting).Assignments), 1)
	eq(t, len(h.delivers), 1)
	eq(t, []string{h.delivers[0].ticket.ID, h.delivers[0].assignment.State}, []string{waiting, "running"})
}

func TestInterruptedRestartsWorkerThatGotPersonsMessageAfterReporting(t *testing.T) {
	h := newHarness(t)
	other := h.accepted("2", "t2").TicketID
	h.report(other, "waiting", "done for now")
	h.tick(1)
	h.receive(trigger("3", actor(guest), threads("t2")))
	h.settle()
	running := core.RunningAssignment(h.ticket(other))
	eq(t, running.DeliveredAt > running.Report.At, true)
	h.ok(h.engine.AssignmentInterrupted(other, running.ID))
	eq(t, states(*h.ticket(other)), []string{"ended", "pending"})
}

func TestPauseLetsNoEvalExpireAndMovesDeadline(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	h.ok(h.engine.CandidateDelivered(h.requests[0].EvalID))
	deadline, _ := core.ParseTime(h.thread("chat:t1").Evaluating.Deadline)
	h.tick(30)
	h.paused = true
	h.pump()
	h.tick(300)
	h.pump()
	if h.engine.Candidate("1") == nil {
		t.Fatal("expired while paused")
	}
	h.paused = false
	h.pump()
	moved, _ := core.ParseTime(h.thread("chat:t1").Evaluating.Deadline)
	eq(t, moved, deadline+300_000)
	h.tick(29)
	h.pump()
	if h.engine.Candidate("1") == nil {
		t.Fatal("expired early")
	}
	h.tick(1)
	h.pump()
	if c := h.engine.Candidate("1"); c == nil || c.EvalID == h.requests[0].EvalID {
		t.Fatal("not requeued into a fresh Eval")
	}
	eq(t, len(h.decisions("chat:t1")), 0)
}

func TestPolicyChangeTellsBearingTicketsAndRestartsEvals(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	other := h.accepted("3", "t3").TicketID
	h.receive(trigger("2", threads("t2")))
	h.pump()
	effect := core.PolicyEffect{Rules: []core.RuleEffect{{ID: "copy", Name: "Copy", Change: "changed", Instruction: "Fix typos only."}}}
	h.version = "v2"
	h.ok(h.engine.PolicyChanged("v1", "v2", map[string]core.PolicyEffect{decision.TicketID: effect}))
	ins := h.ticket(decision.TicketID).Inputs
	eq(t, len(ins), 1)
	eq(t, []any{ins[0].Kind, ins[0].From, ins[0].To, ins[0].Effect}, []any{"policy-changed", "v1", "v2", &effect})
	for _, i := range h.ticket(other).Inputs {
		if i.Kind == "policy-changed" {
			t.Fatal("unrelated ticket told")
		}
	}
	ds := h.decisions("chat:t2")
	eq(t, len(ds), 1)
	eq(t, []any{ds[0].Kind, ds[0].Reason}, []any{"unavailable", []core.Reason{{Code: "policyChanged"}}})
	eq(t, h.pending("chat:t2"), []string{"2"})
	h.pump()
	eq(t, []any{h.last().PolicyVersion, triggerIDs(h.last())}, []any{"v2", []string{"2"}})
	eq(t, h.delivered, [][2]string{{decision.TicketID, ins[0].ID}})
}

func TestPolicyDoesNotStartEvalsWhilePaused(t *testing.T) {
	h := newHarness(t, paused())
	h.receive(trigger("1"))
	h.pump()
	eq(t, len(h.requests), 0)
}

func TestReportsAskKeepReplaceAndClearQuestion(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	asked, err := h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "need a call", Question: str("Which color?"), AskedBy: "1"})
	h.ok(err)
	eq(t, []string{asked.Attention.Question, asked.Attention.TriggerID}, []string{"Which color?", "1"})
	kept := h.report(ticketID, "blocked", "still waiting")
	eq(t, kept.Attention, asked.Attention)
	eq(t, len(h.withdrawn), 0)
	replaced, err := h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "other question", Question: str("Which size?")})
	h.ok(err)
	eq(t, []string{replaced.Attention.Question, replaced.Attention.TriggerID}, []string{"Which size?", ""})
	eq(t, h.withdrawn, []string{core.Key("attention-reply", asked.Attention.ID)})
	cleared, err := h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "answered", Question: str("")})
	h.ok(err)
	if cleared.Attention != nil {
		t.Fatal("attention kept")
	}
	eq(t, h.withdrawn, []string{core.Key("attention-reply", asked.Attention.ID), core.Key("attention-reply", replaced.Attention.ID)})
}

func TestReportsFinalReportEndsTicket(t *testing.T) {
	outcome := func(state string, follows, closed bool) *core.Ticket {
		h := newHarness(t)
		ticketID := h.accepted("1", "t1").TicketID
		_, err := h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "asking", Question: str("Merge it?")})
		h.ok(err)
		if follows {
			h.bind(ticketID, "gh", "pr-9", false)
			h.bind(ticketID, "gh", "pr-10", true)
		}
		if closed {
			h.ok(h.engine.ThreadClosed(core.ThreadRef{Source: "gh", ID: "pr-9"}, "closed", "2026-09-22T00:00:00Z"))
			h.ok(h.engine.ThreadClosed(core.ThreadRef{Source: "gh", ID: "pr-10"}, "merged", "2026-09-22T00:00:01Z"))
		}
		h.report(ticketID, state, "done")
		return h.ticket(ticketID)
	}
	d := outcome("dropped", true, false)
	eq(t, []string{d.Ended.By, d.Ended.Outcome, d.Ended.Reason}, []string{"executor", "dropped", ""})
	r := outcome("ready", false, false)
	eq(t, r.Ended.Outcome, "completed")
	if r.Attention != nil {
		t.Fatal("attention kept")
	}
	f := outcome("ready", true, false)
	eq(t, f.Follows, []*core.Follow{{Thread: "gh:pr-9", Own: false}, {Thread: "gh:pr-10", Own: true}})
	eq(t, f.Assignments[0].Report.State, "ready")
	if f.Ended != nil || f.Attention != nil {
		t.Fatal("want open without attention")
	}
	eq(t, outcome("ready", true, true).Ended.Outcome, "merged")
	failed := outcome("failed", false, false)
	eq(t, failed.Attention.Question, "Merge it?")
	if failed.Ended != nil {
		t.Fatal("failed ended")
	}
}

func TestReportsRefuseQuestionWithResultAndUnknownAskers(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", bot(), threads("t1")))
	h.receive(trigger("4", threads("elsewhere")))
	report := func(r core.WorkerReport) (core.Ticket, error) {
		if r.State == "" {
			r.State = "progress"
		}
		r.Summary = "x"
		return h.engine.Report(ticketID, r)
	}
	_, err := report(core.WorkerReport{State: "ready", Question: str("Q?")})
	userErr(t, err, "questionWithResult")
	_, err = report(core.WorkerReport{Question: str("Q?"), Answers: "1"})
	userErr(t, err, "questionWithResult")
	_, err = report(core.WorkerReport{AskedBy: "1"})
	userErr(t, err, "askerWithoutQuestion")
	_, err = report(core.WorkerReport{Question: str("Q?"), AskedBy: "2"})
	userErr(t, err, "notAsker")
	_, err = report(core.WorkerReport{Answers: "4"})
	userErr(t, err, "notAnswerable")
	ok, err := report(core.WorkerReport{Answers: "1"})
	h.ok(err)
	eq(t, ok.Answers, []string{"1"})
}

func TestReportsRefuseEndedTicket(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.stop(ticketID, "")
	_, err := h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "x"})
	userErr(t, err, "ticketEnded")
}

func TestOutcomesRecordHowTicketEnded(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.stop(ticketID, "Human asked to stop")
	e := h.ticket(ticketID).Ended
	eq(t, []string{e.By, e.Outcome, e.Reason}, []string{"human", "stopped", "Human asked to stop"})
	eq(t, core.TicketOutcomeOf(*h.ticket(ticketID)), "stopped")
	eq(t, core.TicketOutcomeOf(core.Ticket{}), "")
}

func TestAttachSendsNoteEndsSoleEvalAndRefusesStoppedOrRevoked(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", actor(guest)))
	h.pump()
	ids := func() []string {
		out := []string{}
		for _, d := range h.delivered {
			out = append(out, d[1])
		}
		return out
	}
	eq(t, ids(), []string{core.Key("input", ticketID, "trigger", "2")})
	if h.engine.Candidate("2") != nil {
		t.Fatal("follow-up held by an Eval")
	}
	_, err := h.engine.Attach(ticketID, "2", "")
	userErr(t, err, "workerDecides", "tickets", ticketID)
	eq(t, h.reevaluate("2"), true)
	h.pump()
	if h.engine.Candidate("2") == nil {
		t.Fatal("not held")
	}
	attached := h.attach(ticketID, "2", "The guest asks for the same fix")
	h.settle()
	eq(t, len(attached.Inputs), 2)
	eq(t, []string{attached.Inputs[0].TriggerID, attached.Inputs[1].TriggerID, attached.Inputs[1].Note}, []string{"2", "2", "The guest asks for the same fix"})
	eq(t, ids(), []string{attached.Inputs[0].ID, attached.Inputs[1].ID})
	if h.engine.Candidate("2") != nil {
		t.Fatal("eval kept")
	}
	last := lastDecision(h.decisions("chat:t1"))
	eq(t, []any{last.Kind, last.Reason}, []any{"ignore", []core.Reason{{Code: "routed", Values: map[string]string{"ticket": ticketID}}}})
	h.receive(trigger("5", actor(guest)))
	h.reevaluate("5")
	five := h.attach(ticketID, "5", "Same fix again")
	eq(t, []string{lastInput(&five).TriggerID, lastInput(&five).Note}, []string{"5", "Same fix again"})
	h.settle()
	h.report(ticketID, "ready", "done")
	h.version = "revoked"
	_, err = h.engine.Attach(ticketID, "2", "")
	userErr(t, err, "mandateRevoked")
	h.version = "v1"
	stopped := h.accepted("3", "t3").TicketID
	h.stop(stopped, "")
	_, err = h.engine.Attach(stopped, "2", "")
	userErr(t, err, "ticketStopped")
	_, err = h.engine.Attach("missing", "2", "")
	userErr(t, err, "unknownTicket")
}

func TestAttachEndsBatchEvalAndReassessesRest(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	other := h.accepted("5", "t5").TicketID
	h.receive(trigger("2", threads("x")))
	h.receive(trigger("3", threads("x")))
	h.pump()
	h.attach(ticketID, "3", "")
	h.pump()
	ds := h.decisions("chat:x")
	eq(t, len(ds), 1)
	eq(t, []any{ds[0].Kind, ds[0].TriggerIDs, ds[0].Reason}, []any{"unavailable", []string{"2", "3"},
		[]core.Reason{{Code: "routed", TriggerID: "3", Values: map[string]string{"ticket": ticketID}}}})
	eq(t, triggerIDs(*h.engine.Candidate("2")), []string{"2"})
	eq(t, h.owners(*h.store.Triggers().Get("3")), []string{ticketID})
	_, err := h.engine.Attach(other, "3", "")
	userErr(t, err, "workerDecides", "tickets", ticketID)
}

func TestUserErrorsNameMissingApartAndFillValues(t *testing.T) {
	eq(t, []bool{core.NewUserError("unknownTicket", nil).Missing, core.NewUserError("ticketEnded", nil).Missing}, []bool{true, false})
	e := core.NewUserError("notAdmissible", map[string]string{"reason": "not_a_member"})
	eq(t, []any{e.Code, e.Values, e.Error()}, []any{"notAdmissible", map[string]string{"reason": "not_a_member"}, "Trigger is not admissible: not_a_member"})
}

func TestDirectDecisionsExplicitAcceptOnceAndInferredNeedsRules(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	decision, err := h.engine.Decide("1", core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "routed"})
	h.ok(err)
	eq(t, []any{decision.Kind, decision.TriggerIDs, decision.Strength}, []any{"accept", []string{"1"}, "explicit"})
	eq(t, h.pending("chat:t1"), []string{})
	if h.thread("chat:t1").Evaluating != nil {
		t.Fatal("evaluating")
	}
	again, err := h.engine.Decide("1", core.Outcome{Kind: "accept", Title: "Again", Scope: "small", Said: "routed"})
	h.ok(err)
	eq(t, again, decision)
	h.receive(trigger("2", actor(guest)))
	h.pump()
	ticketID := decision.TicketID
	_, err = h.engine.Decide("2", core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "rule", Rules: []string{"r1"}})
	userErr(t, err, "workerDecides", "tickets", ticketID)
	h.handback(ticketID, "2", "A different fix")
	_, err = h.engine.Decide("2", core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "proactive"})
	userErr(t, err, "notExplicit")
	d, err := h.engine.Decide("2", core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "rule", Rules: []string{"r1"}})
	h.ok(err)
	eq(t, []string{d.Kind, d.Strength}, []string{"accept", "inferred"})
	_, err = h.engine.Decide("missing", core.Outcome{Kind: "ignore", Said: "x"})
	userErr(t, err, "unknownTrigger")
}

func TestDirectDecisionsInferredOnlyOnAdmittedRules(t *testing.T) {
	h := newHarness(t, withAdmits(func(r []string) bool { return contains(r, "enabled") }))
	h.accepted("1", "t1")
	h.receive(trigger("2", actor(guest)))
	h.reevaluate("2")
	h.pump()
	c := h.last()
	_, err := h.decide(c, core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "rule", Rules: []string{"disabled"}})
	userErr(t, err, "notExplicit")
	eq(t, h.mustDecide(c, core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "rule", Rules: []string{"enabled"}}).Kind, "accept")
}

func TestDirectDecisionsReplayedTriggerReturnsItsDecision(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	h.tick(1)
	eq(t, h.reevaluate("1"), true)
	h.pump()
	if h.engine.Candidate("1") != nil {
		t.Fatal("replay held")
	}
	last := lastDecision(h.decisions("chat:t1"))
	eq(t, []any{last.Kind, last.TriggerIDs, last.Reason}, []any{"ignore", []string{"1"},
		[]core.Reason{{Code: "startedTicket", TriggerID: "1", Values: map[string]string{"ticket": decision.TicketID}}}})
	again, err := h.engine.Decide("1", core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "again"})
	h.ok(err)
	eq(t, again, decision)
	eq(t, len(h.store.Tickets().List()), 1)
	_, err = h.engine.Reevaluate("missing")
	userErr(t, err, "unknownTrigger")
}

func TestDirectDecisionsReplayedTriggerLendsNoAuthority(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	h.receive(trigger("2", actor(guest)))
	h.reevaluate("2")
	h.reevaluate("1")
	h.pump()
	if h.engine.Candidate("1") != nil {
		t.Fatal("replay held")
	}
	c := h.engine.Candidate("2")
	eq(t, []any{c.Strength, triggerIDs(*c)}, []any{"inferred", []string{"2"}})
	_, err := h.engine.Decide("2", core.Outcome{Kind: "accept", Title: "Other", Scope: "small", Said: "routed"})
	userErr(t, err, "notExplicit")
	again, err := h.engine.Decide("1", core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "again"})
	h.ok(err)
	eq(t, again, decision)
	eq(t, len(h.store.Tickets().List()), 1)
}

func TestDirectDecisionsRequestBesideReplayStartsOwnTicket(t *testing.T) {
	h := newHarness(t)
	first := h.accepted("1", "t1")
	h.tick(1)
	h.receive(trigger("2"))
	h.reevaluate("2")
	h.reevaluate("1")
	h.pump()
	second := h.mustDecide(h.last(), core.Outcome{Kind: "accept", Title: "Other", Scope: "small", Said: "routed", Acknowledgement: "On it"})
	if second.TicketID == first.TicketID {
		t.Fatal("same ticket")
	}
	eq(t, h.ticket(second.TicketID).Origin, core.Origin{TriggerID: "2", TriggerIDs: []string{"2"}})
	var ack *core.OutboundMessage
	for i := range h.outbox {
		if h.outbox[i].ID == core.Key("ack", "2") {
			ack = &h.outbox[i]
		}
	}
	eq(t, []string{ack.Text, ack.Kind}, []string{"On it", "acknowledgement"})
	again, err := h.engine.Decide("1", core.Outcome{Kind: "accept", Title: "Again", Scope: "small", Said: "again"})
	h.ok(err)
	eq(t, again, first)
}

func TestAutomaticInputHeldWhileWorkerWaitsForPerson(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", bot()))
	h.report(ticketID, "blocked", "needs a decision")
	h.receive(trigger("3", bot()))
	h.settle()
	ticket := h.ticket(ticketID)
	eq(t, ticket.Undelivered, []string{})
	eq(t, []string{ticket.Inputs[0].TriggerID, ticket.Inputs[1].TriggerID}, []string{"2", "3"})
	eq(t, len(h.delivered), 0)
	h.tick(1)
	h.receive(trigger("4", actor(guest)))
	h.receive(trigger("5", bot()))
	h.settle()
	eq(t, h.delivered, [][2]string{{ticketID, core.Key("input", ticketID, "trigger", "4")}, {ticketID, core.Key("input", ticketID, "trigger", "5")}})
}

func TestAutomaticInputWaitingForPersonLifts(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	ticket := func() *core.Ticket { return h.ticket(ticketID) }
	h.report(ticketID, "blocked", "needs a decision")
	eq(t, core.WaitsForPerson(ticket()), true)
	eq(t, core.StageOf(ticket()), core.TicketStage{Stage: "person", Action: "blocked"})
	h.tick(1)
	h.receive(trigger("2", actor(guest)))
	h.settle()
	eq(t, core.WaitsForPerson(ticket()), false)
	eq(t, core.StageOf(ticket()), core.TicketStage{Stage: "working"})
	h.tick(1)
	h.report(ticketID, "failed", "broke")
	eq(t, core.WaitsForPerson(ticket()), true)
	h.tick(1)
	h.report(ticketID, "progress", "found a way")
	eq(t, core.WaitsForPerson(ticket()), false)
	h.tick(1)
	h.report(ticketID, "blocked", "needs access")
	_, err := h.engine.RetryTicket(ticketID)
	h.ok(err)
	eq(t, core.WaitsForPerson(ticket()), false)
}

func TestAutomaticInputRelaunchesForPersonNotForPolicyChange(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	ticketID := h.mustDecide(h.last(), accept("Fix")).TicketID
	h.report(ticketID, "failed", "The prepare command failed")
	h.starts = nil
	h.version = "v2"
	h.ok(h.engine.PolicyChanged("v1", "v2", nil))
	eq(t, len(h.starts), 0)
	h.tick(1)
	h.receive(trigger("2", actor(guest)))
	eq(t, len(h.starts), 1)
	eq(t, []string{h.starts[0].ticket.ID, h.starts[0].assignment.State}, []string{ticketID, "pending"})
}

func TestRepliesAcknowledgeOnceAndRefuseOthers(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	r, err := h.engine.Reply(core.ReplyRequest{Purpose: "acknowledgement", TicketID: ticketID, TriggerID: "1", Text: "On it"})
	h.ok(err)
	eq(t, r, core.Reply{ID: core.Key("ack", "1"), Status: "pending", Reused: true})
	eq(t, []string{h.outbox[0].Text}, []string{"ok"})
	eq(t, len(h.outbox), 1)
	h.receive(trigger("2", actor(guest)))
	r, err = h.engine.Reply(core.ReplyRequest{Purpose: "acknowledgement", TicketID: ticketID, TriggerID: "2", Text: "Looking"})
	h.ok(err)
	eq(t, r, core.Reply{ID: core.Key("ack", "2"), Status: "pending", Reused: false})
	h.receive(trigger("3", bot()))
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "acknowledgement", TicketID: ticketID, TriggerID: "3", Text: "x"})
	userErr(t, err, "ackNotPerson")
	h.receive(trigger("6", threads("elsewhere")))
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "acknowledgement", TicketID: ticketID, TriggerID: "6", Text: "x"})
	userErr(t, err, "notReached")
	h.stop(ticketID, "")
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "acknowledgement", TicketID: ticketID, TriggerID: "2", Text: "x"})
	userErr(t, err, "ackEnded")
}

func TestRepliesAnswerResultAndAttentionRules(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	asked := trigger("2", actor(guest), channel("t1", "2"))
	h.receive(asked)
	_, err := h.engine.Reply(core.ReplyRequest{Purpose: "answer", TriggerID: "2", Text: "Yes"})
	userErr(t, err, "noAnswer")
	_, err = h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "Yes", Answers: "2"})
	h.ok(err)
	r, err := h.engine.Reply(core.ReplyRequest{Purpose: "answer", TriggerID: "2", Text: "Yes"})
	h.ok(err)
	eq(t, []any{r.Status, r.Reused}, []any{"pending", false})
	last := h.outbox[len(h.outbox)-1]
	eq(t, []any{last.Channel, last.Text, last.Kind}, []any{*asked.Channel, "Yes", "reply"})
	eq(t, h.ticket(ticketID).Answers, []string{})
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "result", TicketID: ticketID, Text: "Done"})
	userErr(t, err, "notReady")
	_, err = h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "asking", Question: str("Which color?"), AskedBy: "2"})
	h.ok(err)
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "attention", TicketID: ticketID, Question: "Which size?", Text: "x"})
	userErr(t, err, "notAttention")
	quiet := h.accepted("9", "t9").TicketID
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "attention", TicketID: quiet, Text: "Anything to send."})
	userErr(t, err, "notAttention")
	attention := h.ticket(ticketID).Attention
	r, err = h.engine.Reply(core.ReplyRequest{Purpose: "attention", TicketID: ticketID, Question: "Which color?", Text: "Which color?"})
	h.ok(err)
	eq(t, r.ID, core.Key("attention-reply", attention.ID))
	eq(t, h.outbox[len(h.outbox)-1].Channel, *asked.Channel)
	h.report(ticketID, "ready", "Done")
	r, err = h.engine.Reply(core.ReplyRequest{Purpose: "result", TicketID: ticketID, Text: "Done"})
	h.ok(err)
	eq(t, r.Status, "pending")
	eq(t, h.outbox[len(h.outbox)-1].Channel, *trigger("1").Channel)
}
