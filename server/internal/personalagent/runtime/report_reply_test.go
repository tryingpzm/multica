package runtime_test

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// MUL-13449: a Worker's result reaches the person only through a ready report the Router replies to; a run that ends
// without one is reported waiting, and (as SA src/core/replies.ts answer(): result requires ready) the Router may not
// deliver it as a result.
func TestTheWorkerIsToldToReportItsResultReadyToTheTriggerAndNotAsAnIssueComment(t *testing.T) {
	f := setup(t)
	f.accept(t, "n1")
	desc := f.runs.posts[0]
	for _, want := range []string{"ticket report --kind ready", "结论不要写成 issue 评论"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description lacks %q:\n%s", want, desc)
		}
	}
	// MUL-13594: the trigger a ready report answers by default is in multica ticket read, not on the issue.
	ticketID := f.open(t).Store.Tickets().List()[0].ID
	read, err := f.rt.Executor.Read(f.open(t), ticketID)
	if err != nil || read["trigger"].(map[string]any)["trigger_id"] != "panel:n1" {
		t.Fatalf("read %v %v", read, err)
	}
}

func TestARunThatEndsWithoutAReportIsMarkedUnreportedAndAWorkersOwnReportIsNot(t *testing.T) {
	f := setup(t)
	f.accept(t, "n1")
	ticketID := f.open(t).Store.Tickets().List()[0].ID
	if err := f.rt.Executor.RunEnded(context.Background(), "00000000-0000-4000-8000-000000000001", "", "completed", "答案写在评论里了"); err != nil {
		t.Fatal(err)
	}
	if got := core.LatestReport(f.open(t).Store.Tickets().Get(ticketID)); got == nil || got.State != "waiting" || !got.Unreported {
		t.Fatalf("fallback report %+v", got)
	}
	if _, err := f.rt.Executor.Report(f.open(t), ticketID, executorReport("waiting", "等人回复", nil)); err != nil {
		t.Fatal(err)
	}
	if got := core.LatestReport(f.open(t).Store.Tickets().Get(ticketID)); got.Unreported {
		t.Fatalf("worker report %+v", got)
	}
}
