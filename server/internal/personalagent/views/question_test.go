package views_test

// MUL-13594: a ticket waiting for the owner's answer shows the question and a short reason on its card and page.

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

func blockedTicket(id string, attention *core.Attention, summary string) core.Ticket {
	return core.Ticket{ID: id, Title: "排期", CreatedAt: "2026-11-18T10:00:00Z", UpdatedAt: "2026-11-18T10:38:00Z", Attention: attention,
		Assignments: []*core.Assignment{{ID: "a-" + id, State: "running", CreatedAt: "2026-11-18T10:00:00Z",
			Report: &core.Report{State: "blocked", Summary: summary, At: "2026-11-18T10:38:00Z"}}},
		Inputs: []*core.TicketInput{}, Undelivered: []string{}}
}

func TestABlockedReportsQuestionAndReasonShowOnTheCardAndPage(t *testing.T) {
	a := open(t)
	q := "要排期哪个需求单（链接或需求 ID + 模块）？"
	a.Store.Atomic(func() {
		a.Store.Tickets().Save(blockedTicket("q1", &core.Attention{ID: "at1", Question: q}, "MUL-562 是咨询单，没有关联的需求单，无法排期。\n\n细节：查过 需求系统。"))
	})
	row := view(a).Tickets("open", 0, 20).Items[0].(map[string]any)
	got, _ := row["question"].(map[string]string)
	if got["text"] != q || got["reason"] != "MUL-562 是咨询单，没有关联的需求单，无法排期。" {
		t.Fatalf("card question = %v", row["question"])
	}
	if page := view(a).Ticket("q1", nil); page["question"].(map[string]string)["text"] != q {
		t.Fatalf("page question = %v", page["question"])
	}
}

// An older blocked report without human_action: the question is the summary's last sentence that asks one.
func TestAnOlderBlockedReportWithoutHumanActionStillShowsItsQuestion(t *testing.T) {
	a := open(t)
	a.Store.Atomic(func() {
		a.Store.Tickets().Save(blockedTicket("q2", nil, "MUL-562 是咨询单，无法排期。需要指定具体的需求单：是哪一个？"))
	})
	row := view(a).Tickets("open", 0, 20).Items[0].(map[string]any)
	got, _ := row["question"].(map[string]string)
	if got["text"] != "需要指定具体的需求单：是哪一个？" || got["reason"] != "MUL-562 是咨询单，无法排期。" {
		t.Fatalf("question = %v", row["question"])
	}
}
