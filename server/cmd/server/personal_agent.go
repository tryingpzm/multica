package main

// personal-agent wiring of the personal agent's Sources: Multica comments and inbox items are offered to every enabled personal agent. Nothing is stored, and no existing
// behaviour changes, for a person without an enabled agent.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multicainbox"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const personalAgentTimeout = 10 * time.Second

func newPersonalAgentHost(pool *pgxpool.Pool, h *handler.Handler, bus *events.Bus) *host.Host {
	return newPersonalAgentRuntime(pool, h, bus).Host
}

// registerPersonalAgentInbox offers every new member inbox item to its recipient's personal agent, off the publisher
// . Publishers send the item as a map or a response struct; both are read through their JSON.
func registerPersonalAgentInbox(bus *events.Bus, h *host.Host) {
	bus.Subscribe(protocol.EventInboxNew, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		raw, err := json.Marshal(payload["item"])
		if err != nil {
			return
		}
		var it struct {
			ID            string          `json:"id"`
			WorkspaceID   string          `json:"workspace_id"`
			RecipientType string          `json:"recipient_type"`
			RecipientID   string          `json:"recipient_id"`
			Type          string          `json:"type"`
			Title         string          `json:"title"`
			Body          *string         `json:"body"`
			IssueID       *string         `json:"issue_id"`
			ActorType     *string         `json:"actor_type"`
			ActorID       *string         `json:"actor_id"`
			CreatedAt     string          `json:"created_at"`
			Details       json.RawMessage `json:"details"`
		}
		if json.Unmarshal(raw, &it) != nil || it.ID == "" || it.RecipientType != "member" || it.RecipientID == "" {
			return
		}
		deref := func(s *string) string {
			if s == nil {
				return ""
			}
			return *s
		}
		var details map[string]any
		_ = json.Unmarshal(it.Details, &details)
		commentID, _ := details["comment_id"].(string)
		at, _ := time.Parse(time.RFC3339, it.CreatedAt)
		workspace := it.WorkspaceID
		if workspace == "" {
			workspace = e.WorkspaceID
		}
		item := multicainbox.Item{WorkspaceID: workspace, ID: it.ID, RecipientID: it.RecipientID, Type: it.Type, Title: it.Title,
			Body: deref(it.Body), IssueID: deref(it.IssueID), CommentID: commentID, ActorType: deref(it.ActorType), ActorID: deref(it.ActorID), At: at,
			Details: map[string]string{}}
		for _, key := range []string{"from", "to"} { // a status change's from and to.
			if s, ok := details[key].(string); ok {
				item.Details[key] = s
			}
		}
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), personalAgentTimeout)
			defer cancel()
			if _, err := h.Inbox(ctx, item); err != nil {
				slog.Warn("personal agent: inbox item not offered", "error", err)
			}
		}()
	})
}

// registerPersonalAgentComments offers every new comment to the workspace's personal agents, off the publisher.
func registerPersonalAgentComments(bus *events.Bus, h *host.Host) {
	bus.Subscribe(protocol.EventCommentCreated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			slog.Warn("personal agent: comment payload not a map", "type", fmt.Sprintf("%T", e.Payload))
			return
		}
		c, isStruct := payload["comment"].(handler.CommentResponse)
		if !isStruct {
			// The service layer (agent replies) publishes the comment as a plain map, the
			// handler layer as CommentResponse. Read both through JSON.
			raw, isMap := payload["comment"].(map[string]any)
			if !isMap {
				slog.Warn("personal agent: comment assert failed", "type", fmt.Sprintf("%T", payload["comment"]))
				return
			}
			data, err := json.Marshal(raw)
			if err != nil {
				return
			}
			if err := json.Unmarshal(data, &c); err != nil {
				slog.Warn("personal agent: comment map not decodable", "error", err)
				return
			}
		}
		if c.ID == "" || c.IssueID == "" {
			return
		}
		at, _ := time.Parse(time.RFC3339, c.CreatedAt)
		comment := multica.Comment{WorkspaceID: e.WorkspaceID, IssueID: c.IssueID, CommentID: c.ID, AuthorType: c.AuthorType, AuthorID: c.AuthorID,
			Content: c.Content, At: at}
		if c.ParentID != nil {
			comment.ParentID = *c.ParentID
		}
		slog.Info("personal agent: comment received", "id", c.ID, "authorType", c.AuthorType, "authorID", c.AuthorID, "issueID", c.IssueID)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), personalAgentTimeout)
			defer cancel()
			taken, err := h.Comment(ctx, comment)
			if err != nil {
				slog.Warn("personal agent: comment not offered", "error", err)
			} else {
				slog.Info("personal agent: comment offered", "taken", taken)
			}
		}()
	})
}
