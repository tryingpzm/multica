package core_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

// Translated from tests/followups.spec.ts, the part the Engine and Policy decide: a person's follow-up on a subscribed
// Thread goes to the ticket's worker without an Eval, and the main Session may only stop the work on it until the worker
// hands it back. The main-Session notices, the model-call budget and the effect queue of that spec belong to the Router
// stage. The Source is faked as a chat whose scope receives mentions; the real settingsPolicy gates.

type chatScope struct{}

func (chatScope) Scope(string, map[string]any) core.ScopeResult {
	row := "chat"
	return core.ScopeResult{Listened: true, Row: &row}
}

func followupHarness(t *testing.T) *harness {
	s := policy.MustParseSettings(`{"policy":{"sources":{"chat":{"allowed":[{"id":"alice"}]}}}}`)
	return newHarness(t, withPolicy(policy.SettingsPolicy(s, chatScope{})))
}

func mention() tOpt { return func(t *core.Trigger) { t.Addressed = "mention" } }

// acceptedTicket accepts alice's mention on thread as a ticket, the way the main Session routes an explicit request.
func (h *harness) acceptedTicket(id, thread string) string {
	h.t.Helper()
	h.receive(trigger(id, threads(thread), mention()))
	h.pump()
	d := h.mustDecide(h.last(), core.Outcome{Kind: "accept", Title: "Fix " + id, Scope: "small", Said: "asked", Acknowledgement: "收到"})
	h.settle()
	return d.TicketID
}

func TestFollowupsDeliveredToWorkerWithoutEval(t *testing.T) {
	h := followupHarness(t)
	ticketID := h.acceptedTicket("1", "t1")
	requests := len(h.requests)
	h.receive(trigger("2"))
	h.pump()
	eq(t, h.delivered, [][2]string{{ticketID, core.Key("input", ticketID, "trigger", "2")}})
	eq(t, len(h.requests), requests)
	th := h.thread("chat:t1")
	eq(t, th.Pending, []string{})
	if th.Evaluating != nil {
		t.Fatal("follow-up started an Eval")
	}
	eq(t, len(h.decisions("chat:t1")), 1)
	d := h.disposition("2")
	eq(t, []any{d.Fanout, d.EvalID, d.DecisionID}, []any{[]string{ticketID}, "", ""})
}

func TestFollowupsReachingOnlyStoppedTicketGoesToEval(t *testing.T) {
	h := followupHarness(t)
	ticketID := h.acceptedTicket("1", "t1")
	h.stop(ticketID, "")
	h.delivered = nil
	h.receive(trigger("2", mention()))
	h.pump()
	eq(t, len(h.delivered), 0)
	eq(t, triggerIDs(h.last()), []string{"2"})
}

func TestFollowupsLeftToWorkerUntilHandedBack(t *testing.T) {
	h := followupHarness(t)
	ticketID := h.acceptedTicket("1", "t1")
	other := h.acceptedTicket("3", "t3")
	settle := func(id string, opts ...tOpt) string { h.receive(trigger(id, opts...)); h.pump(); return id }
	// A guest's follow-up stays inferred, so its stop does not stop the work; the main Session cannot route it.
	g := settle("4", actor(guest))
	d, err := h.engine.Decide(g, core.Outcome{Kind: "stop", TicketID: ticketID, Said: "别做了"})
	h.ok(err)
	eq(t, d.Kind, "ignore")
	_, err = h.engine.Decide(g, core.Outcome{Kind: "accept", Title: "More", Scope: "small", Said: "asked"})
	userErr(t, err, "workerDecides")
	// A person may still send it to Eval from the panel.
	eq(t, h.reevaluate(g), true)
	h.pump()
	eq(t, triggerIDs(h.last()), []string{g})
	// Alice's follow-ups carry her authority once the worker hands them back: one goes to the other ticket, one becomes new work.
	a := settle("5")
	_, err = h.engine.Attach(other, a, "")
	userErr(t, err, "workerDecides")
	h.handback(ticketID, a, "It is about the other request.")
	if got := h.attach(other, a, ""); got.ID != other {
		t.Fatal("attached elsewhere")
	}
	work := settle("6")
	request := core.Outcome{Kind: "accept", Title: "导出按钮", Scope: "small", Said: "asked", Acknowledgement: "收到，加导出按钮。"}
	_, err = h.engine.Decide(work, request)
	userErr(t, err, "workerDecides")
	// The attach subscribed the other ticket to this thread, so its worker decides the message too.
	h.handback(ticketID, work, "It asks for an export button.")
	_, err = h.engine.Decide(work, request)
	userErr(t, err, "workerDecides")
	h.handback(other, work, "Not this ticket either.")
	accepted, err := h.engine.Decide(work, request)
	h.ok(err)
	eq(t, h.ticket(accepted.TicketID).Mandate.Strength, "explicit")
	// A stop on a person's request needs no hand-back.
	stop := settle("7")
	d, err = h.engine.Decide(stop, core.Outcome{Kind: "stop", TicketID: ticketID, Said: "先停一下"})
	h.ok(err)
	eq(t, d.Kind, "stop")
	e := h.ticket(ticketID).Ended
	eq(t, []string{e.By, e.Outcome}, []string{"human", "stopped"})
}
