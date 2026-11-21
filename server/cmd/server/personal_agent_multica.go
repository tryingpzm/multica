package main

// personal-agent the multica Source's outbound port: a reply on a Multica thread is a comment on the
// issue, under the comment it answers, by the owner's Router agent. Written through the queries, so it raises no
// agent run on the issue (no loop).

import (
	"context"

	"github.com/multica-ai/multica/server/internal/handler"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type personalAgentCommenter struct{ h *handler.Handler }

func (c personalAgentCommenter) Comment(ctx context.Context, author, issueID, parentID, text string) error {
	issue, err := c.h.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		return err
	}
	params := db.CreateCommentParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: "agent",
		AuthorID: parseUUID(author), Content: text, Type: "comment"}
	if parentID != "" {
		params.ParentID = parseUUID(parentID)
	}
	_, err = c.h.Queries.CreateComment(ctx, params)
	return err
}
