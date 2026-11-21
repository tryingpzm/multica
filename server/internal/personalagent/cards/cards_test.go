package cards_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/cards"
	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// MUL-13446: a channel's @{name} mark, stored before MUL-13380, reads as @name on every card.
func TestCardsShowChannelMentionsWithoutBraces(t *testing.T) {
	tr := core.Trigger{ID: "t1", Threads: []core.ThreadRef{{Source: "feishu", ID: "c:p"}}, Actor: core.ActorRef{ID: "alice"},
		Text: "@{bob} 看下首页改版"}
	if got := cards.TriggerCard(tr, nil)["preview"]; got != "@bob 看下首页改版" {
		t.Fatalf("card preview = %v", got)
	}
	if got := cards.TriggerMessage(tr, cards.Link{}, "", nil)["text"]; got != "@bob 看下首页改版" {
		t.Fatalf("message text = %v", got)
	}
}

// MUL-13529 F-04: a ready result is not delivered by the host; its card tells the Router to reply to the message it
// answers (dispatch: complete the handoff in the originating conversation).
func TestAReadyReportTellsTheRouterToReplyToTheMessageItAnswers(t *testing.T) {
	var card map[string]any
	_ = json.Unmarshal([]byte(cards.ResultRelay(core.Ticket{ID: "k1"}, "ready", "MUL-601 还在规划中", "multica:i:c2").Text), &card)
	next, _ := card["next_action"].(string)
	for _, want := range []string{"multica ticket reply", "purpose=result", "trigger_id=multica:i:c2"} {
		if !strings.Contains(next, want) {
			t.Fatalf("next_action = %q; want it to contain %q", next, want)
		}
	}
	waiting := map[string]any{}
	_ = json.Unmarshal([]byte(cards.ResultRelay(core.Ticket{ID: "k1"}, "waiting", "等确认", "").Text), &waiting)
	if _, ok := waiting["next_action"]; ok {
		t.Fatalf("a waiting report has next_action %v; want none", waiting["next_action"])
	}
}
