// Package multicainbox is the multica-inbox Source: every Multica notification sent to the owner — an
// assignment, a status change, a review request, an automation notice, an @ — is a Trigger on its issue's Thread.
// A notification raised for a comment the multica Source already keeps is left to that Source, so a message is
// one Trigger.
package multicainbox

import (
	"context"
	"fmt"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
)

// Source is the kind's ID.
const Source = "multica-inbox"

// Modes of the kind's switch.
const (
	ModeAll     = "all"
	ModeMention = "mention"
)

// Item is one inbox item created for a member.
type Item struct {
	WorkspaceID string
	ID          string
	RecipientID string
	Type        string
	Title       string
	Body        string
	IssueID     string
	// CommentID is the comment the notification was raised for, if any.
	CommentID string
	// ActorType is member, agent or system.
	ActorType string
	ActorID   string
	At        time.Time
	// Details are the notification's details, such as a status change's from and to.
	Details map[string]string
}

// TriggerID is the Trigger of an inbox item: multica-inbox:<item>.
func TriggerID(itemID string) string { return Source + ":" + itemID }

// Mentioned reports whether the item is an @ of its recipient.
func (i Item) Mentioned() bool { return i.Type == "mentioned" }

// Comment is the comment event the item was raised for, ok false when it was raised for none.
func (i Item) Comment() (multica.Comment, bool) {
	if i.CommentID == "" || i.IssueID == "" {
		return multica.Comment{}, false
	}
	return multica.Comment{WorkspaceID: i.WorkspaceID, IssueID: i.IssueID, CommentID: i.CommentID, AuthorType: i.ActorType,
		AuthorID: i.ActorID, Content: i.Body, At: i.At}, true
}

// Trigger converts an item into a Trigger for the agent of owner; it returns nil for another member's item and for
// the owner's own actions.
func Trigger(i Item, owner string) *core.Trigger {
	if i.ID == "" || i.RecipientID != owner || (i.ActorType == "member" && i.ActorID == owner) {
		return nil
	}
	thread := core.ThreadRef{Source: Source, ID: i.ID}
	if i.IssueID != "" {
		thread = multica.ThreadRef(i.IssueID)
	}
	meta := map[string]any{"inboxItemId": i.ID, "type": i.Type, "actorType": i.ActorType, "mentioned": i.Mentioned()}
	if i.IssueID != "" {
		meta["issueId"] = i.IssueID
	}
	if i.CommentID != "" {
		meta["commentId"] = i.CommentID
	}
	text := i.Title
	if i.Type == "status_changed" && i.Details["to"] != "" {
		text += "\n状态：" + i.Details["from"] + " → " + i.Details["to"]
	}
	if i.Body != "" {
		text += "\n" + i.Body
	}
	t := &core.Trigger{
		ID: TriggerID(i.ID), Source: Source, Threads: []core.ThreadRef{thread},
		Actor: core.ActorRef{Source: multica.Source, ID: i.ActorID}, Human: i.ActorType == "member", Notice: i.CommentID == "" && i.IssueID != "",
		At: i.At.UTC().Format("2006-01-02T15:04:05.000Z"), Text: text, Meta: meta,
	}
	if i.IssueID != "" {
		t.Channel = &core.ChannelRef{Source: multica.Source, Thread: multica.Source + ":" + i.IssueID,
			Address: map[string]string{"issueId": i.IssueID, "commentId": i.CommentID}}
	}
	if i.Mentioned() {
		t.Addressed = "mention"
	}
	return t
}

// Scope is the listening rule: everything under "all", only @s under "mention".
func Scope(meta map[string]any, config sources.Config) core.ScopeResult {
	row := "notifications"
	if config["mode"] == ModeMention && meta["mentioned"] != true {
		return core.ScopeResult{Reason: "not_mentioned"}
	}
	return core.ScopeResult{Listened: true, Observed: config["mode"] != ModeMention, Row: &row}
}

// Kind returns the multica-inbox kind; it reads issues through the multica Source's ports.
func Kind(p multica.Ports) sources.Kind {
	read := multica.Kind(p).ThreadContext
	return sources.Kind{
		Kind:        Source,
		Title:       sources.Localized{Zh: "Multica 收件箱", En: "Multica inbox"},
		Description: sources.Localized{Zh: "接收 Multica 发给你的全部通知：被指派、状态变化、review 请求、自动化通知、@ 你等。", En: "Receives every Multica notification sent to you: assignments, status changes, review requests, automation notices, mentions."},
		Schema: func(raw map[string]any) (sources.Config, error) {
			mode := ModeAll
			for key, v := range raw {
				if key != "mode" {
					return nil, fmt.Errorf("unknown key %s", key)
				}
				s, _ := v.(string)
				if s != ModeAll && s != ModeMention {
					return nil, fmt.Errorf("mode must be all or mention")
				}
				mode = s
			}
			return sources.Config{"mode": mode}, nil
		},
		Fields: []sources.FieldSpec{{Key: "mode", Type: "select", Label: sources.Localized{Zh: "接收", En: "Receive"}, Options: []sources.Option{
			{Value: ModeAll, Label: sources.Localized{Zh: "全部通知", En: "All notifications"}},
			{Value: ModeMention, Label: sources.Localized{Zh: "仅 @ 我", En: "Only @ me"}}}}},
		Health: []string{Source},
		Scope:  Scope,
		ThreadContext: &sources.ThreadContext{
			MessageID: func(t core.Trigger) string { s, _ := t.Meta["inboxItemId"].(string); return s },
			Read: func(ctx context.Context, t core.Trigger, part, cursor string, c sources.Config) (sources.ContextPart, error) {
				if _, ok := t.Meta["issueId"].(string); !ok {
					return sources.ContextPart{Messages: []sources.ContextMessage{}, Status: "no_parent"}, nil
				}
				return read.Read(ctx, t, part, cursor, c)
			},
		},
		Policy: &sources.PolicyPart{
			Schema:   sources.PeoplePolicy,
			Fields:   sources.PeopleFields(sources.Localized{Zh: "Multica 用户 id", En: "Multica user id"}),
			Explicit: sources.Localized{Zh: "@ 你的通知是明确的请求；其他通知、机器人和智能体产生的通知只算推断候选。", En: "A notification that mentions you is an explicit request; other notifications, and those from bots and agents, are inferred candidates."},
		},
	}
}
