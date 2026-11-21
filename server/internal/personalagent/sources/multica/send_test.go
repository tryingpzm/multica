package multica

import (
	"context"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
)

type fakeCommenter struct{ author, issue, parent, text string }

func (f *fakeCommenter) Comment(_ context.Context, author, issueID, parentID, text string) error {
	f.author, f.issue, f.parent, f.text = author, issueID, parentID, text
	return nil
}

// MUL-13529 E-10: a reply on a Multica thread is posted on the issue, under the comment, as the agent's Router.
func TestAReplyOnAMulticaThreadIsPostedOnTheIssueAsTheRouter(t *testing.T) {
	c := &fakeCommenter{}
	ctx := sources.WithSender(context.Background(), "router-1")
	got, err := Kind(Ports{Commenter: c}).Channel.Send(ctx, core.OutboundMessage{Text: "MUL-601 还在规划中",
		Channel: core.ChannelRef{Source: Source, Address: map[string]string{"issueId": "i1", "commentId": "c1"}}})
	if err != nil || got != sources.Sent {
		t.Fatalf("send = %q, %v; want sent", got, err)
	}
	if *c != (fakeCommenter{author: "router-1", issue: "i1", parent: "c1", text: "MUL-601 还在规划中"}) {
		t.Fatalf("comment = %+v", *c)
	}
}

func TestAReplyWithNoSenderIsNotPosted(t *testing.T) {
	c := &fakeCommenter{}
	if _, err := Kind(Ports{Commenter: c}).Channel.Send(context.Background(), core.OutboundMessage{Text: "x",
		Channel: core.ChannelRef{Source: Source, Address: map[string]string{"issueId": "i1"}}}); err == nil || c.issue != "" {
		t.Fatalf("err = %v, comment = %+v; want refused", err, *c)
	}
}
