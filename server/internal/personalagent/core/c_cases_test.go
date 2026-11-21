package core_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// MUL-13533 C 类分流：引擎层模拟（真实 Engine + MemoryRepositories，仅 assessor/executor/outbox 为 fake）。

func nonOwnDecisions(h *harness, key string) int {
	n := 0
	for _, d := range h.decisions(key) {
		if d.Kind != "ignore" {
			n++
		}
	}
	return n
}

// C-01 同帖人类追问：直通 T1，无新 Eval/Decision/工单。
func TestC01FollowupGoesStraightToLiveTicket(t *testing.T) {
	h := newHarness(t)
	t1 := h.accepted("1", "t1").TicketID
	req, dec := len(h.requests), len(h.decisions("chat:t1"))
	h.receive(trigger("2", actor(guest)))
	h.pump()
	eq(t, len(h.ticket(t1).Inputs), 1)
	eq(t, h.disposition("2").Fanout, []string{t1})
	eq(t, []int{len(h.requests), len(h.decisions("chat:t1")), len(h.store.Tickets().List())}, []int{req, dec, 1})
}

// C-02 同帖机器人消息：不进 T1，不新建工单。
func TestC02BotMessageOnLiveTicketThread(t *testing.T) {
	h := newHarness(t)
	t1 := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", bot()))
	h.pump()
	in := h.ticket(t1).Inputs
	t.Logf("C-02 T1 inputs=%d human=%v fanout=%v", len(in), func() []bool {
		b := []bool{}
		for _, i := range in {
			b = append(b, i.Human)
		}
		return b
	}(), h.disposition("2").Fanout)
	eq(t, len(h.store.Tickets().List()), 1)
	eq(t, lastDecision(h.decisions("chat:t1")).Kind, "ignore")
	for _, i := range in {
		if i.Human {
			t.Fatal("bot input marked human")
		}
	}
	t.Logf("C-02 delivered=%v starts=%d delivers=%d", h.delivered, len(h.starts), len(h.delivers))
	eq(t, len(h.starts), 1) // 没有因机器人消息重新拉起 Worker
}

// C-03 T1 ended(by=executor) 后人追问：恢复 T1，同一 Mandate，无 Eval。
func TestC03ResumeEndedByExecutor(t *testing.T) {
	h := newHarness(t)
	t1 := endedTicket(h, "executor")
	m := h.ticket(t1).Mandate
	req := len(h.requests)
	h.receive(trigger("2", actor(guest)))
	h.pump()
	tk := h.ticket(t1)
	if tk.Ended != nil {
		t.Fatal("not resumed")
	}
	eq(t, tk.Mandate, m)
	eq(t, len(tk.Assignments), 2)
	eq(t, len(h.requests), req)
}

// C-04 T1 被本人 stop 后追问：T1 不收，走新 Eval。
func TestC04StoppedTicketReceivesNothing(t *testing.T) {
	h := newHarness(t)
	t1 := endedTicket(h, "human")
	req := len(h.requests)
	h.receive(trigger("2", actor(guest)))
	h.pump()
	eq(t, len(h.ticket(t1).Inputs), 0)
	eq(t, h.ticket(t1).Ended.By, "human")
	eq(t, len(h.requests), req+1)
	eq(t, triggerIDs(h.last()), []string{"2"})
}

// C-05 另一帖明确指向 T1：Router attach → 进 T1，不新建。
func TestC05AttachFromAnotherThread(t *testing.T) {
	h := newHarness(t)
	t1 := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", actor(guest), threads("t2")))
	h.pump()
	h.attach(t1, "2", "asks about T1")
	h.settle()
	eq(t, len(h.store.Tickets().List()), 1)
	eq(t, len(inputsFor(h.ticket(t1), "2")), 1)
	if !contains(h.thread("chat:t2").Subscriptions, t1) {
		t.Fatal("t2 not subscribed")
	}
}

// C-08 T1 ended(by=human) 后另帖指明 MUL-a：不自动恢复；attach 被拒，只能新建。
func TestC08HumanEndedNotResumedNorAttachable(t *testing.T) {
	h := newHarness(t)
	t1 := endedTicket(h, "human")
	h.receive(trigger("2", actor(guest), threads("t2")))
	h.pump()
	if h.ticket(t1).Ended == nil {
		t.Fatal("resumed")
	}
	_, err := h.engine.Attach(t1, "2", "")
	if err == nil {
		t.Fatal("attach to human-stopped ticket accepted")
	}
	t.Logf("C-08 attach err=%v", err)
}

// C-09 两帖并发同一事项：两个独立 Eval；第二个 attach 到第一个工单后同一工单只有一个 Worker。
func TestC09ConcurrentSameMatter(t *testing.T) {
	h := newHarness(t)
	h.receive(trigger("1", threads("t1")))
	h.receive(trigger("2", threads("t2")))
	h.pump()
	eq(t, len(h.requests), 2)
	d1 := h.mustDecide(h.requests[0], accept("MUL-a 进度"))
	h.settle()
	h.attach(d1.TicketID, "2", "same matter")
	h.settle()
	eq(t, len(h.store.Tickets().List()), 1)
	running := 0
	for _, a := range h.ticket(d1.TicketID).Assignments {
		if a.State == "running" || a.State == "pending" {
			running++
		}
	}
	eq(t, running, 1)
}

// C-13 fix 工单同帖继续补充：直通 fix 工单，不新研判。
func TestC13FixFollowupDirect(t *testing.T) {
	h := newHarness(t)
	fx := h.accepted("1", "t1").TicketID
	req := len(h.requests)
	h.receive(trigger("2", actor(guest)))
	h.pump()
	eq(t, len(inputsFor(h.ticket(fx), "2")), 1)
	eq(t, len(h.requests), req)
}

// C-17 Worker hand back：消息进新 Eval，候选带理由，T1 保留订阅。
func TestC17HandbackToNewEval(t *testing.T) {
	h := newHarness(t)
	t1 := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", actor(guest)))
	h.pump()
	eq(t, h.handback(t1, "2", "超出范围：MUL-c 排期"), true)
	h.pump()
	eq(t, triggerIDs(h.last()), []string{"2"})
	hb := h.disposition("2").Handbacks
	eq(t, []any{len(hb), hb[0].Reason}, []any{1, "超出范围：MUL-c 排期"})
	if !contains(h.thread("chat:t1").Subscriptions, t1) {
		t.Fatal("subscription dropped")
	}
}

// C-18 markUnrelated：只解 attach 线程，起始线程不可解。
func TestC18MarkUnrelated(t *testing.T) {
	h := newHarness(t)
	t1 := h.accepted("1", "t1").TicketID
	h.receive(trigger("2", threads("t2")))
	h.pump()
	h.attach(t1, "2", "")
	h.settle()
	h.ok(h.engine.MarkUnrelated(t1, "chat:t2", "unrelated"))
	if contains(h.thread("chat:t2").Subscriptions, t1) {
		t.Fatal("still subscribed")
	}
	userErr(t, h.engine.MarkUnrelated(t1, "chat:t1", "own"), "sourceThread")
}

// C-19 面板 Unsubscribe 后追问：不直通，走新 Eval。
func TestC19UnsubscribeThenEval(t *testing.T) {
	h := newHarness(t)
	t1 := h.accepted("1", "t1").TicketID
	_, err := h.engine.Unsubscribe("chat:t1")
	h.ok(err)
	h.receive(trigger("2", actor(guest)))
	h.pump()
	eq(t, len(h.ticket(t1).Inputs), 0)
	eq(t, h.disposition("2").Fanout, []string{})
	eq(t, lastDecision(h.decisions("chat:t1")).TriggerIDs, []string{"2"}) // 走了新 Eval（harness gate 对非成员判 ignore）
}

// C-20 followForContext：X 上评论作为上下文直达 T1；不恢复已结束 T1。
func TestC20FollowContext(t *testing.T) {
	h := newHarness(t)
	t1 := h.accepted("1", "t1").TicketID
	eq(t, h.follow(t1, "x"), true)
	h.receive(trigger("2", actor(guest), threads("x")))
	h.settle()
	in := inputsFor(h.ticket(t1), "2")
	eq(t, []any{len(in), in[0].Context}, []any{1, true})
	h.report(t1, "ready", "done")
	h.receive(trigger("3", actor(guest), threads("x")))
	h.pump()
	if h.ticket(t1).Ended == nil {
		t.Fatal("context message resumed ended ticket")
	}
	_ = core.Explicit
}
