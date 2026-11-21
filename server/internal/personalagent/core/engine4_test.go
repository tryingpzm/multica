package core_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// tests/core-engine.spec.ts — ticket commands through 'ignores from the card'.

func (h *harness) closeThread(id, outcome, at string) {
	h.t.Helper()
	h.ok(h.engine.ThreadClosed(core.ThreadRef{Source: "gh", ID: id}, outcome, at))
}

func TestTicketCommandsCloseAndReadyEndBySameRule(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.bind(ticketID, "gh", "pr-5", false)
	h.bind(ticketID, "gh", "pr-7", true)
	h.closeThread("pr-7", "merged", "2026-09-23T00:00:00Z")
	eq(t, h.ticket(ticketID).Ended.Outcome, "merged")
	h.tick(10)
	h.receive(trigger("2", threads("gh:pr-5")))
	h.settle()
	if h.ticket(ticketID).Ended != nil {
		t.Fatal("not resumed")
	}
	h.report(ticketID, "ready", "Answered")
	eq(t, h.ticket(ticketID).Ended.Outcome, "merged")
	h.closeThread("pr-5", "closed", "2026-09-23T00:00:30Z")
	eq(t, h.ticket(ticketID).Ended.Outcome, "merged")
	other := h.accepted("3", "t3").TicketID
	h.bind(other, "gh", "pr-8", false)
	h.report(other, "ready", "Pushed to their branch")
	if h.ticket(other).Ended != nil {
		t.Fatal("ended before its PR closed")
	}
	h.closeThread("pr-8", "closed", "2026-09-23T00:01:00Z")
	eq(t, h.ticket(other).Ended.Outcome, "closed")
}

func TestTicketCommandsUnsubscribeStopsFollowing(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.bind(ticketID, "gh", "pr-7", true)
	dropped, err := h.engine.Unsubscribe("gh:pr-7")
	h.ok(err)
	eq(t, dropped, true)
	if h.ticket(ticketID).Follows != nil {
		t.Fatal("follows kept")
	}
	h.report(ticketID, "ready", "Done")
	eq(t, h.ticket(ticketID).Ended.Outcome, "completed")
}

func TestTicketCommandsFollowsEndOnceOwnThreadsClosedUnlessResumedWorkUnfinished(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	closeAll := func(id, outcome, at string) []string {
		open := []string{}
		for _, tk := range h.store.Tickets().Open() {
			open = append(open, tk.ID)
		}
		h.closeThread(id, outcome, at)
		ended := []string{}
		for _, id := range open {
			if h.ticket(id).Ended != nil {
				ended = append(ended, id)
			}
		}
		return ended
	}
	bound := h.bind(ticketID, "gh", "pr-9", false)
	eq(t, bound.Follows, []*core.Follow{{Thread: "gh:pr-9", Own: false}})
	h.bind(ticketID, "gh", "pr-10", true)
	eq(t, h.thread("gh:pr-9").Subscriptions, []string{ticketID})
	eq(t, closeAll("pr-9", "closed", "2026-09-23T00:00:00Z"), []string{})
	f := h.ticket(ticketID).Follows
	eq(t, []any{f[0].Thread, f[0].Closed.Outcome, f[1].Thread, f[1].Closed}, []any{"gh:pr-9", "closed", "gh:pr-10", (*core.Closed)(nil)})
	if h.ticket(ticketID).Ended != nil {
		t.Fatal("ended")
	}
	eq(t, closeAll("pr-10", "merged", "2026-09-23T00:00:01Z"), []string{ticketID})
	e := h.ticket(ticketID).Ended
	eq(t, []string{e.By, e.Outcome, e.At}, []string{"executor", "merged", "2026-09-23T00:00:01.000Z"})
	eq(t, closeAll("pr-10", "merged", "2026-09-23T00:00:01Z"), []string{})
	_, err := h.engine.BindThread(ticketID, core.ThreadRef{Source: "gh", ID: "pr-11"}, true)
	userErr(t, err, "ticketEnded")
	other := h.accepted("2", "t2").TicketID
	h.bind(other, "gh", "pr-12", true)
	h.report(other, "failed", "tests fail")
	h.tick(10)
	_, err = h.engine.RetryTicket(other)
	h.ok(err)
	eq(t, closeAll("pr-12", "closed", "2026-09-23T00:00:05Z"), []string{})
	if h.ticket(other).Ended != nil {
		t.Fatal("ended while resumed work unfinished")
	}
	h.report(other, "ready", "done")
	eq(t, h.ticket(other).Ended.Outcome, "closed")
}

func TestTicketCommandsRetryFailedAndRequestStatusOfWaitingWorker(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	_, err := h.engine.RetryTicket(ticketID)
	userErr(t, err, "notRetryable")
	asked, err := h.engine.RequestStatus(ticketID)
	h.ok(err)
	eq(t, asked, false)
	eq(t, len(h.ticket(ticketID).Inputs), 0)
	h.report(ticketID, "waiting", "idle")
	asked, err = h.engine.RequestStatus(ticketID)
	h.ok(err)
	eq(t, asked, true)
	ins := h.ticket(ticketID).Inputs
	eq(t, len(ins), 1)
	eq(t, ins[0].Kind, "text")
	if !containsStr(ins[0].Text, "status-only") {
		t.Fatal("not a status request")
	}
	h.report(ticketID, "failed", "broke")
	retried, err := h.engine.RetryTicket(ticketID)
	h.ok(err)
	eq(t, []string{retried.Assignments[0].State, retried.Assignments[0].EndedReason, retried.Assignments[1].State}, []string{"ended", "retried", "pending"})
	eq(t, len(h.stops), 1)
	lastStart := h.starts[len(h.starts)-1]
	eq(t, []any{lastStart.ticket.ID, lastStart.assignment}, []any{ticketID, *retried.Assignments[1]})
}

func TestFailedDeliveryForgottenOnNewAssignmentOrEnd(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	failed := func() {
		h.ok(h.engine.DeliveryFailed(ticketID, core.RunningAssignment(h.ticket(ticketID)).ID, "Session busy"))
		eq(t, h.ticket(ticketID).Delivery.Error, "Session busy")
	}
	failed()
	h.report(ticketID, "blocked", "needs access")
	_, err := h.engine.RetryTicket(ticketID)
	h.ok(err)
	h.settle()
	if h.ticket(ticketID).Delivery != nil {
		t.Fatal("delivery kept after retry")
	}
	failed()
	h.stop(ticketID, "")
	if h.ticket(ticketID).Delivery != nil {
		t.Fatal("delivery kept after end")
	}
}

func TestProblemsDismissOnlyUnavailable(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	_, err := h.engine.Dismiss(decision.ID)
	userErr(t, err, "unknownProblem")
	h.receive(trigger("2", threads("t2")))
	h.pump()
	unavailable := h.mustDecide(h.last(), core.Outcome{Kind: "unavailable", Said: "model down"})
	d, err := h.engine.Dismiss(unavailable.ID)
	h.ok(err)
	eq(t, []any{d.Kind, d.Dismissed}, []any{"unavailable", true})
	eq(t, h.store.Decisions().Get(unavailable.ID).Dismissed, true)
}

func TestRetryAfterFailedDeliveryTakesTheInput(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "failed", "tests fail")
	h.tick(5)
	h.receive(trigger("2"))
	answered := h.ticket(ticketID)
	eq(t, len(answered.Undelivered), 1)
	h.ok(h.engine.DeliveryFailed(ticketID, core.RunningAssignment(answered).ID, "Session gone"))
	eq(t, core.StageOf(h.ticket(ticketID)), core.TicketStage{Stage: "person", Action: "delivery"})
	retried, err := h.engine.RetryTicket(ticketID)
	h.ok(err)
	eq(t, states(retried), []string{"ended", "pending"})
	if retried.Delivery != nil {
		t.Fatal("delivery kept")
	}
	eq(t, len(retried.Undelivered), 1)
}

func TestVersionsBatchQueuedAgainWhenDecidedUnderOlderPolicy(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	c := h.last()
	h.version = "v2"
	d := h.mustDecide(c, core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "r"})
	eq(t, []any{d.Kind, d.Reason}, []any{"unavailable", []core.Reason{{Code: "policyChanged"}}})
	eq(t, h.pending("chat:t1"), []string{"1"})
	h.pump()
	eq(t, []any{h.last().PolicyVersion, triggerIDs(h.last())}, []any{"v2", []string{"1"}})
}

func TestVersionsMessageQueuedWhenOnlyTicketCannotResume(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "ready", "Done")
	h.settle()
	h.version = "revoked"
	h.tick(5)
	h.receive(trigger("2"))
	tk := h.ticket(ticketID)
	if tk.Ended == nil || len(tk.Undelivered) != 0 {
		t.Fatal("want ended with nothing waiting")
	}
	eq(t, h.pending("chat:t1"), []string{"2"})
	eq(t, h.owners(trigger("2")), []string{})
}

func TestVersionsMessageThatWaitedOutPauseQueuedWhenTicketCannotResume(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "ready", "Done")
	h.settle()
	h.paused = true
	h.tick(5)
	h.receive(trigger("2"))
	h.pump()
	eq(t, len(h.ticket(ticketID).Undelivered), 1)
	h.version = "revoked"
	h.paused = false
	h.ok(h.engine.EvaluateDue())
	tk := h.ticket(ticketID)
	if tk.Ended == nil || len(tk.Undelivered) != 0 {
		t.Fatal("want ended with nothing waiting")
	}
	eq(t, contains(h.pending("chat:t1"), "2") || h.engine.Candidate("2") != nil, true)
}

func TestVersionsOriginAndAckFromStrongestAdmissibleTrigger(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.tick(1)
	ci := trigger("2", bot(), actor(core.ActorRef{Source: "chat", ID: "ci-bot"}), channel("chat:t1", "bot"))
	h.receive(ci)
	h.pump()
	eq(t, triggerIDs(h.last()), []string{"1", "2"})
	d := h.mustDecide(h.last(), core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "r", Acknowledgement: "ok"})
	eq(t, h.ticket(d.TicketID).Origin, core.Origin{TriggerID: "1", TriggerIDs: []string{"1", "2"}})
	eq(t, len(h.outbox), 1)
	eq(t, []any{h.outbox[0].ID, h.outbox[0].Channel}, []any{core.Key("ack", "1"), *trigger("1").Channel})
}

func TestGapsMixedBatchTakesStrongestLatestOfEqualStrength(t *testing.T) {
	gate := func(t core.Trigger, _ core.Thread, _ []core.Ticket) core.GateResult {
		if t.Actor.ID == "alice" {
			return core.GateResult{Admissible: true, Strength: "explicit", Basis: core.MandateBasis{Via: "listed"}}
		}
		return core.GateResult{Admissible: true, Strength: "inferred", Basis: core.MandateBasis{Via: "followup"}}
	}
	for _, order := range [][]string{{"guest", "alice"}, {"alice", "guest"}} {
		h := newHarness(t, withGate(gate))
		alice := ""
		for i, who := range order {
			h.tick(1)
			id := itoa(10 + i)
			a := guest
			if who == "alice" {
				a, alice = member, id
			}
			h.receive(trigger(id, actor(a)))
		}
		h.pump()
		e := h.thread("chat:t1").Evaluating
		eq(t, []any{e.Strength, e.Actor, e.Basis.Via, e.Primary}, []any{"explicit", member, "listed", alice})
	}
	named := newHarness(t, withGate(func(t core.Trigger, _ core.Thread, _ []core.Ticket) core.GateResult {
		return core.GateResult{Admissible: true, Strength: "explicit", Basis: core.MandateBasis{Via: "listed", Sender: t.Actor.ID}}
	}))
	named.receive(trigger("1", actor(core.ActorRef{Source: "chat", ID: "bob"})))
	named.tick(1)
	named.receive(trigger("2", actor(core.ActorRef{Source: "chat", ID: "carol"})))
	named.pump()
	e := named.thread("chat:t1").Evaluating
	eq(t, []string{e.Actor.ID, e.Basis.Sender, e.Primary}, []string{"carol", "carol", "2"})
}

func TestGapsLateStartOfReplacedAssignmentChangesNothing(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	first := h.ticket(ticketID).Assignments[0].ID
	h.report(ticketID, "failed", "broke")
	_, err := h.engine.RetryTicket(ticketID)
	h.ok(err)
	before := h.ticket(ticketID)
	delivered := len(h.delivered)
	h.ok(h.engine.AssignmentStarted(ticketID, first))
	h.settle(false)
	eq(t, h.ticket(ticketID).Assignments, before.Assignments)
	eq(t, len(h.delivered), delivered)
}

func TestGapsStopNamingUnknownOrEndedTicketIsIgnored(t *testing.T) {
	h := newHarness(t)
	ended := h.accepted("1", "t1").TicketID
	h.report(ended, "ready", "Done")
	for _, pair := range [][2]string{{"2", "missing"}, {"3", ended}} {
		h.tick(1)
		h.receive(trigger(pair[0], threads("t"+pair[0])))
		h.pump()
		d := h.mustDecide(h.last(), core.Outcome{Kind: "stop", TicketID: pair[1], Said: "stop it"})
		eq(t, []any{d.Kind, d.Reason}, []any{"ignore", []core.Reason{{Code: "stopUnknown"}}})
	}
	e := h.ticket(ended).Ended
	eq(t, []string{e.By, e.Outcome}, []string{"executor", "completed"})
}

func TestGapsDirectDecisionOnInadmissibleTriggerRefusedQueueKept(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1", bot()))
	pending := h.pending("chat:t1")
	_, err := h.engine.Decide("1", core.Outcome{Kind: "ignore", Said: "noise"})
	userErr(t, err, "notAdmissible", "reason", "not human")
	eq(t, h.pending("chat:t1"), pending)
	eq(t, len(h.decisions("chat:t1")), 0)
}

func TestGapsNoRetryOfTicketStoppedAfterFailedReport(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "failed", "broke")
	h.stop(ticketID, "")
	_, err := h.engine.RetryTicket(ticketID)
	userErr(t, err, "notRetryable")
}

func TestGapsAnswerOnceResultOnlyForFinishedWorkAndOnce(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	_, err := h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "It reads the config.", Answers: "2"})
	h.ok(err)
	r, err := h.engine.Reply(core.ReplyRequest{Purpose: "answer", TriggerID: "2", Text: "It reads the config."})
	h.ok(err)
	eq(t, r.Reused, false)
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "answer", TriggerID: "2", Text: "Something else."})
	userErr(t, err, "noAnswer")
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "result", TicketID: ticketID, Text: "Done"})
	userErr(t, err, "notReady")
	h.report(ticketID, "ready", "Done")
	sent := len(h.outbox)
	r, err = h.engine.Reply(core.ReplyRequest{Purpose: "result", TicketID: ticketID, Text: "Done"})
	h.ok(err)
	eq(t, r.Reused, false)
	r, err = h.engine.Reply(core.ReplyRequest{Purpose: "result", TicketID: ticketID, Text: "Done"})
	h.ok(err)
	eq(t, r.Reused, true)
	eq(t, len(h.outbox), sent+1)
	closed := h.accepted("5", "t5").TicketID
	h.bind(closed, "gh", "pr-1", true)
	h.closeThread("pr-1", "closed", "2026-09-23T00:01:00Z")
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "result", TicketID: closed, Text: "x"})
	userErr(t, err, "notReady")
	stopped := h.accepted("6", "t6").TicketID
	h.stop(stopped, "")
	_, err = h.engine.Reply(core.ReplyRequest{Purpose: "result", TicketID: stopped, Text: "x"})
	userErr(t, err, "notReady")
}

func TestGapsDeadlineKeptOnRepeatedDeliveryHeldNotRequeuedExpiredFirst(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1"))
	h.pump()
	evalID := h.requests[0].EvalID
	h.ok(h.engine.CandidateDelivered(evalID))
	deadline := h.thread("chat:t1").Evaluating.Deadline
	h.tick(30)
	h.ok(h.engine.CandidateDelivered(evalID))
	eq(t, h.thread("chat:t1").Evaluating.Deadline, deadline)
	eq(t, h.reevaluate("1"), false)
	h.tick(1)
	h.receive(trigger("2", actor(guest)))
	h.tick(30)
	h.ok(h.engine.EvaluateDue())
	eq(t, h.thread("chat:t1").Evaluating.TriggerIDs, []string{"1", "2"})
	eq(t, len(h.decisions("chat:t1")), 0)
}

func TestFinalReviewLegacyEvalHoldingOnlyStartedTriggerIsIgnored(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	th := h.thread("chat:t1")
	th.Evaluating = &core.Evaluation{ID: "old-eval", ThreadKey: "chat:t1", TriggerIDs: []string{"1"}, Primary: "1", Strength: "explicit", Actor: member,
		Basis: core.MandateBasis{Via: "listed"}, PolicyVersion: "v1", Deadline: "2999-01-01T00:00:00.000Z"}
	h.store.Threads().Save(*th)
	d, err := h.engine.Decide("1", core.Outcome{Kind: "accept", Title: "Again", Scope: "small", Said: "replayed"})
	h.ok(err)
	eq(t, d, decision)
	last := lastDecision(h.decisions("chat:t1"))
	eq(t, []any{last.Kind, last.Reason}, []any{"ignore", []core.Reason{{Code: "startedTicket", TriggerID: "1", Values: map[string]string{"ticket": decision.TicketID}}}})
	for _, item := range h.decisions("chat:t1") {
		if item.Kind == "unavailable" {
			t.Fatal("recorded unavailable")
		}
	}
	if h.thread("chat:t1").Evaluating != nil {
		t.Fatal("still evaluating")
	}
}

func TestFinalReviewGateIgnoreClearsExpiredCount(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1", bot()))
	th := h.thread("chat:t1")
	th.Expired = 1
	h.store.Threads().Save(*th)
	h.pump()
	ds := h.decisions("chat:t1")
	eq(t, []any{len(ds), ds[0].Kind}, []any{1, "ignore"})
	eq(t, h.thread("chat:t1").Expired, 0)
}

func TestFinalReviewEveryPolicyTransitionToldIncludingRepeats(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	no := false
	effect := core.PolicyEffect{RequesterAllowed: &no}
	for _, p := range [][2]string{{"A", "B"}, {"B", "A"}, {"A", "B"}} {
		h.tick(1)
		h.ok(h.engine.PolicyChanged(p[0], p[1], map[string]core.PolicyEffect{ticketID: effect}))
	}
	n := 0
	for _, k := range kinds(h.ticket(ticketID)) {
		if k == "policy-changed" {
			n++
		}
	}
	eq(t, n, 3)
}

func TestFinalReviewNoAckOrQuestionOnContextThread(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.follow(ticketID, "side")
	h.tick(1)
	h.receive(trigger("5", threads("side"), channel("side", "5")))
	_, err := h.engine.Reply(core.ReplyRequest{Purpose: "acknowledgement", TicketID: ticketID, TriggerID: "5", Text: "On it"})
	userErr(t, err, "notReached")
	_, err = h.engine.Report(ticketID, core.WorkerReport{State: "progress", Summary: "A question", Question: str("Which page?"), AskedBy: "5"})
	userErr(t, err, "notAsker")
}

func TestFinalReviewCloseTimeCanonicalAndUpdatedAtForward(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.bind(ticketID, "gh", "pr-1", true)
	h.tick(30)
	before := h.ticket(ticketID).UpdatedAt
	h.closeThread("pr-1", "merged", "2026-09-23T00:00:05Z")
	ended := h.ticket(ticketID)
	eq(t, []string{ended.Ended.At, ended.Ended.Outcome}, []string{"2026-09-23T00:00:05.000Z", "merged"})
	eq(t, ended.Follows, []*core.Follow{{Thread: "gh:pr-1", Own: true, Closed: &core.Closed{At: "2026-09-23T00:00:05.000Z", Outcome: "merged"}}})
	eq(t, ended.UpdatedAt >= before, true)
}

func TestModelSurfaceOnlyPersonsMessageResumesNotAttachedBots(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "ready", "Done")
	h.tick(1)
	h.receive(trigger("2", bot(), actor(core.ActorRef{Source: "chat", ID: "ci"}), threads("ci")))
	h.attach(ticketID, "2", "")
	if h.ticket(ticketID).Ended == nil {
		t.Fatal("bot resumed it")
	}
	h.tick(1)
	h.receive(trigger("3"))
	if h.ticket(ticketID).Ended != nil {
		t.Fatal("person did not resume it")
	}
}

func merge(h *harness) { h.closeThread("pr-9", "merged", "2026-09-23T00:00:01Z") }

func TestTurnEndsMergeSendsOnlyUnreadMessagesToEval(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.bind(ticketID, "gh", "pr-9", true)
	h.report(ticketID, "ready", "PR ready")
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.read(ticketID)
	h.tick(1)
	h.ok(h.engine.TurnEnded(ticketID, core.RunningAssignment(h.ticket(ticketID)).ID))
	h.tick(1)
	h.receive(trigger("3"))
	h.settle()
	merge(h)
	eq(t, h.pending("chat:t1"), []string{"3"})
}

func TestTurnEndsMergeKeepsTicketOpenWhileWorkerOnTakenUpMessage(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.bind(ticketID, "gh", "pr-9", true)
	h.report(ticketID, "ready", "PR ready")
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.ack(ticketID, "2")
	h.tick(1)
	h.report(ticketID, "progress", "Answered the other question")
	merge(h)
	if h.ticket(ticketID).Ended != nil {
		t.Fatal("ended")
	}
}

func TestTurnEndsDeferredCloseEndsAsCloseDidOnDrop(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.bind(ticketID, "gh", "pr-9", true)
	h.report(ticketID, "ready", "PR ready")
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.ack(ticketID, "2")
	merge(h)
	h.report(ticketID, "dropped", "Nothing more to do")
	eq(t, h.ticket(ticketID).Ended.Outcome, "merged")
}

func TestTurnEndsHandedBackMessageGoesToEvalWhenStopEndsKeeper(t *testing.T) {
	h := newHarness(t)
	first := h.accepted("1", "t1").TicketID
	second := h.accepted("2", "t2").TicketID
	h.bind(second, "chat", "t1", false)
	h.receive(trigger("3"))
	h.settle()
	eq(t, h.handback(second, "3", "It is about the first fix"), false)
	h.stopOn("3", first)
	eq(t, h.pending("chat:t1"), []string{"3"})
}

func TestTurnEndsRequeuedAndAttachedBackMessageReachesWorkerAgain(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.tick(1)
	h.report(ticketID, "progress", "working")
	h.reevaluate("2")
	h.pump()
	h.attach(ticketID, "2", "")
	eq(t, len(inputsFor(h.ticket(ticketID), "2")), 2)
}

func TestDeliveriesStopSendsMessageDeliveredAfterTurnEndToEval(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "progress", "working")
	h.tick(1)
	h.receive(trigger("2"))
	assignmentID := core.RunningAssignment(h.ticket(ticketID)).ID
	inputID := h.ticket(ticketID).Undelivered[0]
	h.tick(1)
	h.ok(h.engine.TurnEnded(ticketID, assignmentID))
	h.tick(1)
	h.ok(h.engine.InputsDelivered(ticketID, assignmentID, []string{inputID}))
	h.tick(1)
	h.stop(ticketID, "not needed")
	eq(t, h.pending("chat:t1"), []string{"2"})
}

func TestDeliveriesStopSendsMessageDeliveredAfterWaitingReportToEval(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.tick(1)
	h.receive(trigger("2"))
	assignmentID := core.RunningAssignment(h.ticket(ticketID)).ID
	inputID := h.ticket(ticketID).Undelivered[0]
	h.tick(1)
	h.report(ticketID, "waiting", "Waiting for input")
	h.tick(1)
	h.ok(h.engine.InputsDelivered(ticketID, assignmentID, []string{inputID}))
	h.tick(1)
	h.stop(ticketID, "not needed")
	eq(t, h.pending("chat:t1"), []string{"2"})
}

func TestRoutingTwoStopsOfOneFollowupAreTwoDecisions(t *testing.T) {
	h := newHarness(t)
	first := h.accepted("1", "t1").TicketID
	second := h.accepted("2", "t2").TicketID
	h.bind(second, "chat", "t1", false)
	h.receive(trigger("3"))
	h.settle()
	a, b := h.stopOn("3", first), h.stopOn("3", second)
	if a.ID == b.ID {
		t.Fatal("one decision")
	}
	eq(t, []string{h.store.Decisions().Get(a.ID).TicketID, h.store.Decisions().Get(b.ID).TicketID}, []string{first, second})
	eq(t, []string{h.ticket(first).Ended.Outcome, h.ticket(second).Ended.Outcome}, []string{"stopped", "stopped"})
}

type resumedCase struct {
	h                                     *harness
	ticketID, assignmentID, note, message string
}

func resumed(t *testing.T) resumedCase {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "ready", "done")
	h.tick(1)
	h.receive(trigger("2"))
	assignmentID := core.LatestAssignment(h.ticket(ticketID)).ID
	h.tick(1)
	h.ok(h.engine.AssignmentStarted(ticketID, assignmentID))
	u := h.ticket(ticketID).Undelivered
	return resumedCase{h, ticketID, assignmentID, u[0], u[1]}
}

func TestReadMessageReachedAfterProgressReportGoesToEvalAtDrop(t *testing.T) {
	c := resumed(t)
	h := c.h
	h.ok(h.engine.InputsDelivered(c.ticketID, c.assignmentID, []string{c.note}))
	h.ok(h.engine.DeliveryFailed(c.ticketID, c.assignmentID, "Session busy"))
	h.tick(1)
	h.report(c.ticketID, "progress", "Looking")
	h.tick(1)
	h.ok(h.engine.InputsDelivered(c.ticketID, c.assignmentID, []string{c.message}))
	h.tick(1)
	h.report(c.ticketID, "dropped", "Nothing to do")
	eq(t, h.pending("chat:t1"), []string{"2"})
}

func TestReadMessageTakenIntoStepCountsAsReadAtDrop(t *testing.T) {
	c := resumed(t)
	h := c.h
	h.ok(h.engine.InputsRead(c.ticketID, []string{c.message}))
	h.ok(h.engine.InputsDelivered(c.ticketID, c.assignmentID, []string{c.note, c.message}))
	h.tick(1)
	h.report(c.ticketID, "progress", "Looking")
	h.tick(1)
	h.report(c.ticketID, "dropped", "Nothing to do")
	eq(t, h.pending("chat:t1"), []string{})
}

func TestReadMergeSendsMessageNoStepTookBeforeTurnEnded(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.bind(ticketID, "gh", "pr-9", true)
	h.report(ticketID, "ready", "PR ready")
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.tick(1)
	h.ok(h.engine.TurnEnded(ticketID, core.RunningAssignment(h.ticket(ticketID)).ID))
	merge(h)
	eq(t, h.pending("chat:t1"), []string{"2"})
}

func TestReadRetriedDeliveryAfterTurnEndReadOnlyOnceStepTookIt(t *testing.T) {
	for _, taken := range []bool{false, true} {
		c := resumed(t)
		h := c.h
		h.ok(h.engine.InputsDelivered(c.ticketID, c.assignmentID, []string{c.note}))
		h.ok(h.engine.DeliveryFailed(c.ticketID, c.assignmentID, "Session busy"))
		h.tick(1)
		h.ok(h.engine.TurnEnded(c.ticketID, c.assignmentID))
		h.tick(1)
		h.ok(h.engine.InputsDelivered(c.ticketID, c.assignmentID, []string{c.message}))
		if taken {
			h.read(c.ticketID)
		}
		h.tick(1)
		h.report(c.ticketID, "dropped", "Nothing to do")
		want := []string{"2"}
		if taken {
			want = []string{}
		}
		eq(t, h.pending("chat:t1"), want)
	}
}

func TestReadStepBeforeRestartCountsOnlyOnceRestartedWorkerReports(t *testing.T) {
	for _, reported := range []bool{false, true} {
		h := newHarness(t)
		ticketID := h.accepted("1", "t1").TicketID
		h.report(ticketID, "progress", "working")
		h.tick(1)
		h.receive(trigger("2"))
		h.settle()
		h.read(ticketID)
		h.tick(1)
		h.ok(h.engine.AssignmentInterrupted(ticketID, core.RunningAssignment(h.ticket(ticketID)).ID))
		h.settle()
		if reported {
			h.tick(1)
			h.report(ticketID, "progress", "Recovered")
		}
		h.tick(1)
		h.stop(ticketID, "not needed")
		want := []string{"2"}
		if reported {
			want = []string{}
		}
		eq(t, h.pending("chat:t1"), want)
	}
}

func inputOf(h *harness, ticketID, triggerID string) string {
	return inputsFor(h.ticket(ticketID), triggerID)[0].ID
}

func TestReadClaimsRetryGetsDeliveredButUntakenMessages(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	inputID := inputOf(h, ticketID, "2")
	eq(t, h.ticket(ticketID).Undelivered, []string{})
	h.tick(1)
	h.report(ticketID, "failed", "The turn broke.")
	h.tick(1)
	_, err := h.engine.RetryTicket(ticketID)
	h.ok(err)
	eq(t, h.ticket(ticketID).Undelivered, []string{inputID})
}

func TestReadClaimsRetryGetsOnlyWhatReplacedAssignmentHad(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.tick(1)
	h.report(ticketID, "dropped", "nothing to do")
	eq(t, h.pending("chat:t1"), []string{"2"})
	h.tick(1)
	h.receive(trigger("3"))
	h.settle()
	h.tick(1)
	h.report(ticketID, "blocked", "Which page?")
	h.tick(1)
	_, err := h.engine.RetryTicket(ticketID)
	h.ok(err)
	eq(t, h.ticket(ticketID).Undelivered, []string{inputOf(h, ticketID, "3")})
}

func TestReadClaimsRestartAndItsRetryGetUntakenMessage(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.tick(1)
	h.ok(h.engine.AssignmentInterrupted(ticketID, core.RunningAssignment(h.ticket(ticketID)).ID))
	eq(t, h.ticket(ticketID).Undelivered[0], inputOf(h, ticketID, "2"))
	h.settle()
	h.tick(1)
	h.report(ticketID, "blocked", "Which page?")
	h.tick(1)
	_, err := h.engine.RetryTicket(ticketID)
	h.ok(err)
	eq(t, h.ticket(ticketID).Undelivered, []string{inputOf(h, ticketID, "2")})
}

func TestIgnoresFromCardMarkUnassessedInferredIgnore(t *testing.T) {
	h := newHarness(t)
	h.accepted("1", "t1")
	h.receive(trigger("2", actor(guest)))
	h.reevaluate("2")
	h.pump()
	eq(t, h.last().Strength, "inferred")
	d, err := h.engine.Decide("2", core.Outcome{Kind: "ignore", Said: "Plain chat."})
	h.ok(err)
	eq(t, d.Reason, []core.Reason{{Code: "ignoredFromCard"}, core.Said("Plain chat.")})
	h.receive(trigger("3", actor(guest)))
	h.reevaluate("3")
	h.pump()
	d = h.mustDecide(h.last(), core.Outcome{Kind: "ignore", Said: "Plain chat.", Audit: map[string]any{"rule_assessments": []any{}}})
	eq(t, d.Reason, []core.Reason{core.Said("Plain chat.")})
	if _, ok := d.Audit["rule_assessments"]; !ok {
		t.Fatal("audit dropped")
	}
	h.receive(trigger("4", threads("t9")))
	h.pump()
	d, err = h.engine.Decide("4", core.Outcome{Kind: "ignore", Said: "Thanks."})
	h.ok(err)
	eq(t, d.Reason, []core.Reason{core.Said("Thanks.")})
	h.receive(trigger("5", actor(guest)))
	h.reevaluate("5")
	h.pump()
	_, err = h.engine.Decide("5", core.Outcome{Kind: "accept", Title: "Fix", Scope: "small", Said: "chat"})
	userErr(t, err, "notExplicit")
}

func itoa(n int) string {
	return string(rune('0'+n/10)) + string(rune('0'+n%10))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
