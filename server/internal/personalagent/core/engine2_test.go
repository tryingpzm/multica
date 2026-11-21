package core_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// tests/core-engine.spec.ts — describe('follow-ups'), ('hand-back'), ('context threads and notes'), ('follow-up owners').

func (h *harness) reevaluate(id string) bool {
	h.t.Helper()
	q, err := h.engine.Reevaluate(id)
	h.ok(err)
	return q
}
func (h *harness) attach(ticketID, triggerID, note string) core.Ticket {
	h.t.Helper()
	t, err := h.engine.Attach(ticketID, triggerID, note)
	h.ok(err)
	return t
}
func (h *harness) handback(ticketID, triggerID, reason string) bool {
	h.t.Helper()
	q, err := h.engine.Handback(ticketID, triggerID, reason)
	h.ok(err)
	return q
}
func (h *harness) stop(ticketID, reason string) {
	h.t.Helper()
	_, err := h.engine.StopTicket(ticketID, reason)
	h.ok(err)
}
func (h *harness) follow(ticketID, thread string) bool {
	h.t.Helper()
	f, err := h.engine.FollowForContext(ticketID, core.ThreadRef{Source: "chat", ID: thread})
	h.ok(err)
	return f
}
func (h *harness) bind(ticketID, src, id string, own bool) core.Ticket {
	h.t.Helper()
	t, err := h.engine.BindThread(ticketID, core.ThreadRef{Source: src, ID: id}, own)
	h.ok(err)
	return t
}
func (h *harness) ack(ticketID, triggerID string) core.Reply {
	h.t.Helper()
	r, err := h.engine.Reply(core.ReplyRequest{Purpose: "acknowledgement", TicketID: ticketID, TriggerID: triggerID, Text: "On it"})
	h.ok(err)
	return r
}
func (h *harness) stopOn(triggerID, ticketID string) core.Decision {
	h.t.Helper()
	d, err := h.engine.Decide(triggerID, core.Outcome{Kind: "stop", TicketID: ticketID, Said: "asked to stop"})
	h.ok(err)
	return d
}

func TestFollowupOlderVersionQueuedLeavesQueueWithoutDecision(t *testing.T) {
	h := newHarness(t)
	h.accepted("1", "t1")
	h.receive(trigger("2", actor(guest)))
	h.receive(trigger("3", actor(guest)))
	// As an older version left them: both queued for Eval; a person sent the second there again.
	th := h.thread("chat:t1")
	th.Pending = []string{"2", "3"}
	h.store.Threads().Save(*th)
	h.reevaluate("3")
	h.pump()
	eq(t, triggerIDs(h.last()), []string{"3"})
	eq(t, h.pending("chat:t1"), []string{})
	ids := []string{}
	for _, d := range h.decisions("chat:t1") {
		ids = append(ids, d.TriggerIDs...)
	}
	eq(t, ids, []string{"1"})
}

func TestFollowupDecidedInGateOnlyEvalBesideEvalInProgress(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	h.receive(trigger("2", actor(guest)))
	h.reevaluate("2")
	h.pump()
	evaluating := h.thread("chat:t1").Evaluating
	h.receive(trigger("3"))
	d, err := h.engine.Decide("3", core.Outcome{Kind: "stop", TicketID: decision.TicketID, Said: "stop it"})
	h.ok(err)
	eq(t, []any{d.Kind, d.TriggerIDs}, []any{"stop", []string{"3"}})
	eq(t, h.thread("chat:t1").Evaluating, evaluating)
	d = h.mustDecide(h.last(), core.Outcome{Kind: "ignore", Said: "chat"})
	eq(t, []any{d.Kind, d.TriggerIDs}, []any{"ignore", []string{"2"}})
}

func TestFollowupLeavesThreadFreeForLaterRequest(t *testing.T) {
	h := newHarness(t)
	decision := h.accepted("1", "t1")
	h.receive(trigger("2", actor(guest)))
	h.pump()
	h.stop(decision.TicketID, "not needed")
	h.receive(trigger("3"))
	h.pump()
	eq(t, triggerIDs(h.last()), []string{"2", "3"})
}

func TestHandbackQueuesWithGateStrengthOncePerTicketKeepsSubscription(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2"))
	h.receive(trigger("3", actor(guest)))
	h.pump()
	eq(t, h.handback(ticketID, "2", "A new request"), true)
	h.pump()
	eq(t, []any{h.last().Strength, triggerIDs(h.last())}, []any{"explicit", []string{"2"}})
	hb := h.disposition("2").Handbacks
	eq(t, []any{len(hb), hb[0].TicketID, hb[0].Reason}, []any{1, ticketID, "A new request"})
	h.mustDecide(h.last(), core.Outcome{Kind: "ignore", Said: "chat"})
	_, err := h.engine.Handback(ticketID, "2", "Again")
	userErr(t, err, "handedBack")
	eq(t, h.handback(ticketID, "3", "A guest asks for more"), true)
	h.pump()
	eq(t, []any{h.last().Strength, triggerIDs(h.last())}, []any{"inferred", []string{"3"}})
	if !contains(h.thread("chat:t1").Subscriptions, ticketID) {
		t.Fatal("subscription dropped")
	}
	// Only a message that reached the ticket can go back.
	h.receive(trigger("4", threads("elsewhere")))
	_, err = h.engine.Handback(ticketID, "4", "Not mine")
	userErr(t, err, "notReached")
}

func TestHandbackQueuesNoMessageTheTicketDidNotDecide(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.follow(ticketID, "issue")
	h.receive(trigger("2"))
	h.reevaluate("2")
	h.receive(trigger("3", bot()))
	h.receive(trigger("4", threads("issue")))
	h.pump()
	for _, c := range h.requests[len(h.requests)-2:] {
		h.mustDecide(c, core.Outcome{Kind: "ignore", Said: "chat"})
	}
	for _, id := range []string{"2", "3", "4"} {
		eq(t, h.handback(ticketID, id, "Not this ticket"), false, id)
	}
	eq(t, [][]string{h.pending("chat:t1"), h.pending("chat:issue")}, [][]string{{}, {}})
	hb := h.disposition("4").Handbacks
	eq(t, []any{len(hb), hb[0].TicketID, hb[0].Reason}, []any{1, ticketID, "Not this ticket"})
	_, err := h.engine.Attach(ticketID, "4", "")
	userErr(t, err, "handedBackHere")
}

func TestHandbackOfAttachedMessageSendsItToEval(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", threads("x")))
	h.pump()
	h.attach(ticketID, "2", "")
	h.settle()
	_, err := h.engine.Decide("2", core.Outcome{Kind: "accept", Title: "Y", Scope: "small", Said: "requested"})
	userErr(t, err, "workerDecides", "tickets", ticketID)
	eq(t, h.handback(ticketID, "2", "Asks for another change"), true)
	eq(t, h.pending("chat:x"), []string{"2"})
}

func TestContextThreadFansOutButResumesNoEndedTicketUnlessAttached(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	eq(t, h.follow(ticketID, "issue"), true)
	eq(t, h.follow(ticketID, "issue"), false)
	h.receive(trigger("2", threads("issue")))
	h.settle()
	ids := []string{}
	for _, i := range h.ticket(ticketID).Inputs {
		ids = append(ids, i.TriggerID)
	}
	eq(t, ids, []string{"2"})
	h.report(ticketID, "dropped", "nothing to do")
	h.receive(trigger("3", threads("issue")))
	if h.ticket(ticketID).Ended == nil {
		t.Fatal("resumed from a context thread")
	}
	// Attached by the main Session, the Thread is the ticket's own, and a person's later message there resumes it.
	h.attach(ticketID, "3", "")
	h.settle()
	eq(t, h.thread("chat:issue").Context, []string{})
	h.report(ticketID, "dropped", "nothing to do")
	if h.ticket(ticketID).Ended == nil {
		t.Fatal("not ended")
	}
	h.receive(trigger("4", threads("issue")))
	if h.ticket(ticketID).Ended != nil {
		t.Fatal("not resumed")
	}
}

func TestContextMessageIsNoFollowupAndLiftsNoBlock(t *testing.T) {
	h := newHarness(t)
	owner := h.accepted("1", "t1").TicketID
	follower := h.accepted("2", "other").TicketID
	h.follow(follower, "t1")
	h.receive(trigger("3", actor(guest)))
	h.pump()
	d := h.disposition("3")
	eq(t, []any{d.Fanout, d.Context}, []any{[]string{owner, follower}, []string{follower}})
	last := lastInput(h.ticket(follower))
	eq(t, []any{last.TriggerID, last.Context}, []any{"3", true})
	h.stop(owner, "done")
	h.report(follower, "blocked", "needs a decision")
	h.receive(trigger("4"))
	h.pump()
	eq(t, triggerIDs(h.last()), []string{"3", "4"})
	eq(t, core.WaitsForPerson(h.ticket(follower)), true)
}

func TestContextOnlyTicketLeftSendsMessageToEval(t *testing.T) {
	h := newHarness(t)
	owner := h.accepted("1", "t1").TicketID
	follower := h.accepted("2", "other").TicketID
	h.follow(follower, "t1")
	h.report(owner, "ready", "done")
	h.version = "revoked"
	h.receive(trigger("3"))
	h.version = "v1"
	h.pump()
	d := h.disposition("3")
	eq(t, []any{d.Fanout, d.Context}, []any{[]string{follower}, []string{follower}})
	eq(t, triggerIDs(h.last()), []string{"3"})
}

func TestNoteIsHeldLikeAutomaticInputAndRefusedForEndedTicket(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	noted, err := h.engine.Note(ticketID, "The other ticket renamed the API")
	h.ok(err)
	eq(t, []string{lastInput(&noted).Kind, lastInput(&noted).Text}, []string{"note", "The other ticket renamed the API"})
	h.settle()
	h.report(ticketID, "blocked", "needs a decision")
	noted, err = h.engine.Note(ticketID, "Ticket B changed the schema")
	h.ok(err)
	note := lastInput(&noted)
	h.settle()
	for _, d := range h.delivered {
		if d[1] == note.ID {
			t.Fatal("note delivered to a blocked worker")
		}
	}
	found := false
	for _, i := range h.ticket(ticketID).Inputs {
		if i.ID == note.ID {
			found = true
		}
	}
	eq(t, found, true)
	h.stop(ticketID, "done")
	_, err = h.engine.Note(ticketID, "Too late")
	userErr(t, err, "ticketEnded")
}

var acceptY = core.Outcome{Kind: "accept", Title: "Y", Scope: "small", Said: "requested"}

func TestOwnersMainSessionTakesFollowupAsNewWorkOnceItStoppedTicket(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2"))
	h.settle()
	_, err := h.engine.Decide("2", acceptY)
	userErr(t, err, "workerDecides", "tickets", ticketID)
	eq(t, h.stopOn("2", ticketID).Kind, "stop")
	d, err := h.engine.Decide("2", acceptY)
	h.ok(err)
	eq(t, []any{d.Kind, d.TriggerIDs}, []any{"accept", []string{"2"}})
	ks := []string{}
	for _, item := range h.decisions("chat:t1") {
		if contains(item.TriggerIDs, "2") {
			ks = append(ks, item.Kind)
		}
	}
	eq(t, ks, []string{"stop", "accept"})
}

func TestOwnersUndeliveredFollowupGoesToEvalWhenTicketEnds(t *testing.T) {
	for _, end := range []string{"report", "stop"} {
		h := newHarness(t)
		ticketID := h.accepted("1", "t1").TicketID
		h.receive(trigger("2"))
		eq(t, len(h.ticket(ticketID).Undelivered), 1)
		if end == "report" {
			h.report(ticketID, "dropped", "nothing to do")
		} else {
			h.stop(ticketID, "not needed")
		}
		eq(t, h.pending("chat:t1"), []string{"2"}, end)
		h.pump()
		eq(t, triggerIDs(h.last()), []string{"2"})
	}
}

func TestOwnersFollowupSinceLastReportGoesToEvalUnlessAcknowledged(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "progress", "working")
	h.tick(1)
	h.receive(trigger("2"))
	h.receive(trigger("3"))
	h.settle()
	h.ack(ticketID, "2")
	h.report(ticketID, "dropped", "nothing to do")
	eq(t, h.pending("chat:t1"), []string{"3"})
	eq(t, [][]string{h.disposition("2").Fanout, h.disposition("3").Fanout}, [][]string{{ticketID}, {}})
}

func TestOwnersKeepsTicketOpenAfterMergeWhileWorkerIsOnTakenUpMessage(t *testing.T) {
	for _, taken := range []bool{true, false} {
		h := newHarness(t)
		ticketID := h.accepted("1", "t1").TicketID
		h.bind(ticketID, "gh", "pr-9", true)
		h.report(ticketID, "ready", "PR ready")
		h.tick(1)
		h.receive(trigger("2"))
		h.settle()
		if taken {
			h.ack(ticketID, "2")
		}
		h.ok(h.engine.ThreadClosed(core.ThreadRef{Source: "gh", ID: "pr-9"}, "merged", "2026-09-23T00:00:01Z"))
		got := []any{core.TicketOutcomeOf(*h.ticket(ticketID)), h.pending("chat:t1")}
		if taken {
			eq(t, got, []any{"", []string{}})
			h.report(ticketID, "ready", "Done")
		} else {
			eq(t, got, []any{"merged", []string{"2"}})
		}
		eq(t, h.ticket(ticketID).Ended.Outcome, "merged")
	}
}

func TestOwnersAttachGivesLetGoFollowupBack(t *testing.T) {
	for _, resumed := range []bool{false, true} {
		h := newHarness(t)
		ticketID := h.accepted("1", "t1").TicketID
		h.report(ticketID, "progress", "working")
		h.tick(1)
		h.receive(trigger("2"))
		h.settle()
		h.report(ticketID, "dropped", "nothing to do")
		if resumed {
			h.tick(1)
			h.receive(trigger("3"))
		}
		h.pump()
		h.tick(1)
		h.attach(ticketID, "2", "")
		h.settle()
		copies := inputsFor(h.ticket(ticketID), "2")
		if len(copies) < 2 {
			t.Fatalf("want two copies, got %d", len(copies))
		}
		found := false
		for _, d := range h.delivered {
			if d == [2]string{ticketID, copies[1].ID} {
				found = true
			}
		}
		eq(t, found, true)
		if h.ticket(ticketID).Ended != nil {
			t.Fatal("ended")
		}
		eq(t, h.owners(trigger("2")), []string{ticketID})
	}
}

func TestOwnersWakesEndedTicketForAttachedMessageReadForContextOrBeforeReport(t *testing.T) {
	for _, read := range []string{"context", "report"} {
		h := newHarness(t)
		ticketID := h.accepted("1", "t1").TicketID
		th := "t1"
		if read == "context" {
			h.follow(ticketID, "issue")
			th = "issue"
		}
		h.tick(1)
		h.receive(trigger("2", threads(th)))
		h.settle()
		h.tick(1)
		h.report(ticketID, "progress", "read it")
		h.report(ticketID, "dropped", "nothing to do")
		if read == "report" {
			h.reevaluate("2")
		}
		h.pump()
		h.tick(1)
		h.attach(ticketID, "2", "")
		h.settle()
		if h.ticket(ticketID).Ended != nil {
			t.Fatal(read, ": still ended")
		}
		eq(t, h.owners(*h.store.Triggers().Get("2")), []string{ticketID}, read)
	}
}

func TestOwnersQuestionTheFinalReportAnswersIsTakenUp(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	_, err := h.engine.Report(ticketID, core.WorkerReport{State: "ready", Summary: "It reads the config.", Answers: "2"})
	h.ok(err)
	eq(t, h.pending("chat:t1"), []string{})
	d := h.disposition("2")
	eq(t, []any{d.Fanout, d.Acked}, []any{[]string{ticketID}, []string{ticketID}})
}

func TestOwnersMessageAttachedAfterUnavailableGoesToEvalWhenTicketEnds(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", threads("x")))
	h.pump()
	h.mustDecide(h.last(), core.Outcome{Kind: "unavailable", Said: "model down"})
	h.tick(1)
	h.attach(ticketID, "2", "")
	h.settle()
	h.report(ticketID, "dropped", "nothing to do")
	eq(t, h.pending("chat:x"), []string{"2"})
}

func TestOwnersLegacyAcknowledgementCountsAsTakenUp(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "progress", "working")
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.outbox = append(h.outbox, core.OutboundMessage{ID: core.Key("ack", "2"), Channel: *trigger("2").Channel, Text: "On it", Kind: "acknowledgement", TicketID: ticketID})
	h.report(ticketID, "dropped", "nothing to do")
	eq(t, []any{h.pending("chat:t1"), h.disposition("2").Fanout}, []any{[]string{}, []string{ticketID}})
}

func TestOwnersResumingMessageIsTakenUpByEndingReportUnlikeLaterOne(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.report(ticketID, "ready", "done")
	h.tick(1)
	h.receive(trigger("2"))
	h.tick(1)
	h.ok(h.engine.AssignmentStarted(ticketID, core.LatestAssignment(h.ticket(ticketID)).ID))
	h.tick(1)
	h.settle()
	h.read(ticketID)
	h.tick(1)
	h.receive(trigger("3"))
	h.settle()
	h.report(ticketID, "dropped", "nothing to do")
	eq(t, h.pending("chat:t1"), []string{"3"})
}

func TestOwnersRefusedStopLetsFollowupGoToEvalButNotTheStoppingOne(t *testing.T) {
	for _, who := range []core.ActorRef{guest, member} {
		h := newHarness(t)
		ticketID := h.accepted("1", "t1").TicketID
		h.receive(trigger("2", actor(who)))
		h.stopOn("2", ticketID)
		if who == guest {
			h.report(ticketID, "dropped", "nothing to do")
			eq(t, h.pending("chat:t1"), []string{"2"})
		} else {
			eq(t, h.pending("chat:t1"), []string{})
		}
	}
}

func TestOwnersStopMessageGoesToEvalOnceOtherTicketEndsWithoutTakingIt(t *testing.T) {
	h := newHarness(t)
	stopped := h.accepted("1", "t1").TicketID
	other := h.accepted("5", "t5").TicketID
	h.bind(other, "chat", "t1", false)
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.stopOn("2", stopped)
	eq(t, h.pending("chat:t1"), []string{})
	h.report(other, "dropped", "nothing to do")
	eq(t, h.pending("chat:t1"), []string{"2"})
}

func TestOwnersUnrelatedThreadFollowupsGoBackWithReason(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", threads("x")))
	h.pump()
	h.attach(ticketID, "2", "")
	h.settle()
	h.receive(trigger("3", threads("x")))
	h.settle()
	_, err := h.engine.Decide("3", acceptY)
	userErr(t, err, "workerDecides", "tickets", ticketID)
	h.ok(h.engine.MarkUnrelated(ticketID, "chat:x", "about something else"))
	eq(t, h.pending("chat:x"), []string{"2", "3"})
	hb := h.disposition("3").Handbacks
	eq(t, []any{len(hb), hb[0].TicketID, hb[0].Reason}, []any{1, ticketID, "about something else"})
	h.pump()
	eq(t, triggerIDs(h.last()), []string{"2", "3"})
}

func TestOwnersFollowupTwoTicketsReachedStaysWithTheOneThatKeptIt(t *testing.T) {
	h := newHarness(t)
	first := h.accepted("1", "t1").TicketID
	h.tick(1)
	h.receive(trigger("2"))
	h.settle()
	h.handback(first, "2", "another change")
	h.pump()
	second := h.mustDecide(h.last(), acceptY).TicketID
	h.settle()
	h.tick(1)
	h.receive(trigger("3"))
	h.settle()
	eq(t, h.disposition("3").Fanout, []string{first, second})
	eq(t, h.handback(first, "3", "for the other ticket"), false)
	eq(t, h.pending("chat:t1"), []string{})
	_, err := h.engine.Decide("3", acceptY)
	userErr(t, err, "workerDecides", "tickets", second)
	_, err = h.engine.Decide("3", core.Outcome{Kind: "ignore", Said: "chat"})
	userErr(t, err, "workerDecides", "tickets", second)
	eq(t, h.handback(second, "3", "neither ticket"), true)
	eq(t, h.pending("chat:t1"), []string{"3"})
}

func TestOwnersLegacyEvalOnFollowupDecidesIt(t *testing.T) {
	h := newHarness(t)
	ticketID := h.accepted("1", "t1").TicketID
	h.receive(trigger("2"))
	h.settle()
	th := h.thread("chat:t1")
	th.Evaluating = &core.Evaluation{ID: "e-legacy", ThreadKey: "chat:t1", TriggerIDs: []string{"2"}, Strength: "explicit", Actor: member,
		Basis: core.MandateBasis{Via: "listed"}, PolicyVersion: "v1", StartedAt: "2026-09-23T00:00:00Z", Deadline: "2026-09-24T00:00:00Z"}
	h.store.Threads().Save(*th)
	eq(t, h.owners(trigger("2")), []string{})
	d, err := h.engine.Decide("2", core.Outcome{Kind: "ignore", Said: "chat", EvalID: "e-legacy"})
	h.ok(err)
	eq(t, []any{d.Kind, d.TriggerIDs}, []any{"ignore", []string{"2"}})
	if h.ticket(ticketID).Ended != nil {
		t.Fatal("ended")
	}
}
