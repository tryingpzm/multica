package views_test

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

func TestPendingReplyNamesWhereItGoesAndAnEditIsWhatIsSent(t *testing.T) {
	a := seed(t).Agent
	a.Outbox.Send(core.OutboundMessage{ID: "m1", Kind: "reply", Text: "已修复", TicketID: "k1", Channel: core.ChannelRef{Source: "multica",
		Thread: "multica:" + issueID, Address: map[string]string{"issueId": issueID, "commentId": "c1"}}})
	item := view(a).WithNames(names()).Overview()["confirm"].([]any)[0].(map[string]any)
	where := item["where"].(map[string]any)
	if where["place"] != "MUL-9" || where["comment"] != true {
		t.Fatalf("where = %v", where)
	}
	if !a.Outbox.Revise("m1", "已修复，请验证") || !a.Outbox.Confirm("m1") {
		t.Fatal("the edited reply was not confirmed")
	}
	if got := a.Outbox.Messages("pending")[0].Text; got != "已修复，请验证" {
		t.Fatalf("sent text = %q", got)
	}
}
