package multicainbox_test

// MUL-13553: a status change notification tells the Router where the issue moved, so 「需要我处理」 can match it.

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/sources/multicainbox"
)

func TestAStatusChangeTriggerSaysWhereTheIssueMoved(t *testing.T) {
	tr := multicainbox.Trigger(multicainbox.Item{ID: "n1", RecipientID: "u1", Type: "status_changed", Title: "本地测试 MR 是否可以合并",
		IssueID: "i1", ActorType: "member", ActorID: "u2", Details: map[string]string{"from": "in_progress", "to": "in_review"}}, "u1")
	if tr == nil || !strings.Contains(tr.Text, "in_progress → in_review") {
		t.Fatalf("trigger %+v", tr)
	}
}
