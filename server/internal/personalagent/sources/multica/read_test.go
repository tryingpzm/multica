package multica_test

// Translated from dsh-superagent-prototype tests/github-read.spec.ts onto Multica issues and comments. SA's CI-check
// and review-thread cases have no Multica counterpart and are not translated.

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
)

type issues struct {
	comments []multica.CommentRecord
	issue    multica.Issue
	calls    [][]any
}

func (f *issues) Issue(_ context.Context, id string) (multica.Issue, error) {
	f.calls = append(f.calls, []any{"issue", id})
	return f.issue, nil
}

func (f *issues) Comments(_ context.Context, id string, page, size int) ([]multica.CommentRecord, error) {
	f.calls = append(f.calls, []any{"comments", id, page, size})
	return f.comments, nil
}

func equal(t *testing.T, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %#v\nwant %#v", got, want)
	}
}

func TestReadsABoundedCommentPageAndExposesTheNextPageReference(t *testing.T) {
	f := &issues{}
	for i := 0; i < 20; i++ {
		f.comments = append(f.comments, multica.CommentRecord{ID: fmt.Sprint(i), Content: "comment", AuthorType: "member"})
	}
	r, err := multica.Read(context.Background(), f, "multica/issues/123/comments/2")
	equal(t, err, nil)
	equal(t, r.NextResource, "multica/issues/123/comments/3")
	equal(t, f.calls, [][]any{{"comments", "123", 2, 20}})
}

func TestRejectsUnsupportedOrEscapingPathsWithoutARequest(t *testing.T) {
	f := &issues{}
	for _, resource := range []string{"multica/../outside", "multica/issues/1/reviews", "multica/issues/1/comments/0", "multica/issues/1?workspace=other"} {
		if _, err := multica.Read(context.Background(), f, resource); err == nil {
			t.Fatalf("%s read", resource)
		}
	}
	equal(t, len(f.calls), 0)
}

func TestReadsAPageOfCommentsAsMessagesWithTheCommentEachAnswers(t *testing.T) {
	at := time.Date(2026, 9, 9, 0, 0, 1, 0, time.UTC)
	f := &issues{comments: []multica.CommentRecord{
		{ID: "1", AuthorType: "member", AuthorName: "carol", Content: "comment 1", At: at},
		{ID: "2", ParentID: "1", AuthorType: "agent", AuthorName: "review-bot", Content: "comment 2", At: at.Add(time.Second)}}}
	r, _ := multica.Read(context.Background(), f, "multica/issues/9/comments/1")
	equal(t, r.Messages, []sources.ContextMessage{
		{ID: "1", From: "carol", At: "2026-09-09T00:00:01Z", Text: "comment 1"},
		{ID: "2", From: "review-bot", At: "2026-09-09T00:00:02Z", Text: "comment 2", Bot: true, ReplyTo: "1"}})
	equal(t, r.NextResource, "")
}

func TestReadsAnIssueAsItsAuthorTimeTitleStateAndDescription(t *testing.T) {
	f := &issues{issue: multica.Issue{ID: "x", Identifier: "MUL-9", Title: "Fix the margin", Status: "todo", Description: "Details",
		AuthorName: "carol", CreatedAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)}}
	r, _ := multica.Read(context.Background(), f, "multica/issues/x")
	equal(t, r.Message, sources.ContextMessage{ID: "MUL-9", From: "carol", At: "2026-09-09T00:00:00Z", Text: "Details"})
	equal(t, r.Body, map[string]any{"identifier": "MUL-9", "title": "Fix the margin", "status": "todo"})
}

func TestACommentMentioningTheOwnerIsAddressedAndTheOwnersOwnIsNoTrigger(t *testing.T) {
	owner := "87ac841f-6a6e-42bd-8183-ec5db10d9334"
	c := multica.Comment{IssueID: "i1", CommentID: "c1", AuthorType: "member", AuthorID: "u2", Content: "[@alex](mention://member/" + owner + ") 看下", At: time.Unix(0, 0)}
	tr := multica.Trigger(c, owner)
	equal(t, []any{tr.ID, tr.Threads[0].ID, tr.Addressed, tr.Human}, []any{"multica:i1:c1", "i1", "mention", true})
	c.Content = "无关"
	equal(t, multica.Trigger(c, owner).Addressed, "")
	c.AuthorID = owner
	if multica.Trigger(c, owner) != nil {
		t.Fatal("the owner's own comment became a Trigger")
	}
	c.AuthorType, c.AuthorID = "agent", "a1"
	equal(t, multica.Trigger(c, owner).Human, false)
}
