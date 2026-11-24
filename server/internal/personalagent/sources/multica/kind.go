// Package multica is the multica Source: comments on Multica issues, translated from SA src/sources/github*.ts. A
// comment that @'s the owner on any issue is an ordinary Trigger; a comment on a Ticket's own issue reaches the
// Ticket's Worker through its subscription to the issue's Thread (design §3).
package multica

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
)

// Source is the kind's ID.
const Source = "multica"

// Comment is one comment:created event.
type Comment struct {
	WorkspaceID string
	IssueID     string
	Identifier  string
	CommentID   string
	ParentID    string
	// AuthorType is member, agent or system.
	AuthorType string
	AuthorID   string
	AuthorName string
	Content    string
	At         time.Time
}

// TriggerID is the Trigger of a comment: multica:<issue>:<comment> (design §3).
func TriggerID(issueID, commentID string) string { return "multica:" + issueID + ":" + commentID }

// ThreadRef is an issue's Thread.
func ThreadRef(issueID string) core.ThreadRef { return core.ThreadRef{Source: Source, ID: issueID} }

// MentionsMember reports whether content mentions a member: [@name](mention://member/<id>).
func MentionsMember(content, userID string) bool {
	return userID != "" && strings.Contains(content, "mention://member/"+userID+")")
}

// Trigger converts a comment into a Trigger for the agent of owner; it returns nil for system comments.
// The owner's own comments are kept: they become explicit candidates through the allowed list, so the
// Router decides whether the message is actionable instead of the source discarding it.
func Trigger(c Comment, owner string) *core.Trigger {
	if c.AuthorType == "system" {
		return nil
	}
	meta := map[string]any{"issueId": c.IssueID, "commentId": c.CommentID, "authorType": c.AuthorType, "mentioned": false}
	if c.Identifier != "" {
		meta["identifier"] = c.Identifier
	}
	if c.ParentID != "" {
		meta["parentId"] = c.ParentID
	}
	t := &core.Trigger{
		ID: TriggerID(c.IssueID, c.CommentID), Source: Source, Threads: []core.ThreadRef{ThreadRef(c.IssueID)},
		Actor: core.ActorRef{Source: Source, ID: c.AuthorID}, Human: c.AuthorType == "member",
		At: c.At.UTC().Format("2006-01-02T15:04:05.000Z"), Text: c.Content, Meta: meta,
		Channel: &core.ChannelRef{Source: Source, Thread: Source + ":" + c.IssueID,
			Address: map[string]string{"issueId": c.IssueID, "identifier": c.Identifier, "commentId": c.CommentID}},
	}
	if MentionsMember(c.Content, owner) {
		t.Addressed = "mention"
		t.Meta["mentioned"] = true
	}
	return t
}

// Commenter posts a comment on an issue as the owner's agent: author is its Router agent.
type Commenter interface {
	Comment(ctx context.Context, author, issueID, parentID, text string) error
}

// Issue is an issue as the reader sees it.
type Issue struct {
	ID, Identifier, Title, Status, Description string
	AuthorName                                 string
	CreatedAt                                  time.Time
}

// CommentRecord is a comment as the reader sees it.
type CommentRecord struct {
	ID, ParentID, AuthorType, AuthorName, Content string
	At                                            time.Time
}

// Issues reads issues and their comments.
type Issues interface {
	Issue(ctx context.Context, issueID string) (Issue, error)
	// Comments returns the page-th (1-based) page of an issue's comments, oldest first, size per page.
	Comments(ctx context.Context, issueID string, page, size int) ([]CommentRecord, error)
}

// Ports are what the kind reads and writes outside the personal agent.
type Ports struct {
	Commenter Commenter
	Issues    Issues
}

// PageSize is the comments a page holds.
const PageSize = 20

// Kind returns the multica kind on ports.
func Kind(p Ports) sources.Kind {
	return sources.Kind{
		Kind:        Source,
		Title:       sources.Localized{Zh: "Multica", En: "Multica"},
		Description: sources.Localized{Zh: "接收 Multica 里 @ 你的评论，以及工单自己 issue 上的评论。", En: "Receives Multica comments that mention you, and comments on Tickets' own issues."},
		Schema: func(raw map[string]any) (sources.Config, error) {
			for key := range raw {
				return nil, fmt.Errorf("unknown key %s", key)
			}
			return sources.Config{}, nil
		},
		Health: []string{Source},
		Scope: func(map[string]any, sources.Config) core.ScopeResult {
			row := "issues"
			// Observed: every comment on an issue is offered to the agent, not only the ones that
			// @-mention the owner. The Router decides what is actionable; the source does not pre-filter.
			return core.ScopeResult{Listened: true, Observed: true, Row: &row}
		},
		Channel: &sources.Channel{
			Send: func(ctx context.Context, m core.OutboundMessage) (string, error) {
				issue := m.Channel.Address["issueId"]
				if issue == "" {
					return sources.Gone, nil
				}
				author := sources.SenderOf(ctx)
				if p.Commenter == nil || author == "" {
					return "", fmt.Errorf("multica: no commenter")
				}
				if err := p.Commenter.Comment(ctx, author, issue, m.Channel.Address["commentId"], m.Text); err != nil {
					return "", err
				}
				return sources.Sent, nil
			},
			Link: func(address map[string]string, links sources.ChannelLinks) sources.ChannelLink {
				label := address["identifier"]
				if label == "" {
					label = address["issueId"]
				}
				url := ""
				if links.AppURL != "" && address["identifier"] != "" {
					url = strings.TrimRight(links.AppURL, "/") + "/issues/" + address["identifier"]
				}
				return sources.ChannelLink{URL: url, Label: sources.Localized{Zh: label, En: label}}
			},
		},
		ThreadContext: &sources.ThreadContext{
			MessageID: func(t core.Trigger) string { s, _ := t.Meta["commentId"].(string); return s },
			Read: func(ctx context.Context, t core.Trigger, part, cursor string, _ sources.Config) (sources.ContextPart, error) {
				issue, _ := t.Meta["issueId"].(string)
				switch part {
				case sources.PartThread:
					page := 1
					if cursor != "" {
						n, err := strconv.Atoi(cursor)
						if err != nil || n < 1 {
							return sources.ContextPart{}, fmt.Errorf("invalid cursor")
						}
						page = n
					}
					r, err := Read(ctx, p.Issues, fmt.Sprintf("multica/issues/%s/comments/%d", issue, page))
					if err != nil {
						return sources.ContextPart{}, err
					}
					next := ""
					if r.NextResource != "" {
						next = strconv.Itoa(page + 1)
					}
					return sources.ContextPart{Messages: r.Messages, Next: next}, nil
				case sources.PartRoot:
					r, err := Read(ctx, p.Issues, "multica/issues/"+issue)
					if err != nil {
						return sources.ContextPart{}, err
					}
					return sources.ContextPart{Messages: []sources.ContextMessage{r.Message}, Body: r.Body, Status: "available"}, nil
				}
				return sources.ContextPart{Messages: []sources.ContextMessage{}, Status: "no_parent"}, nil
			},
		},
		Find: func(query string) sources.Found {
			threads := []string{}
			for _, m := range issueRef.FindAllStringSubmatch(query, -1) {
				threads = append(threads, core.ThreadKey(ThreadRef(m[1])))
			}
			return sources.Found{Threads: threads, Terms: []string{}}
		},
		RepliesTo: func(t core.Trigger) string {
			parent, _ := t.Meta["parentId"].(string)
			issue, _ := t.Meta["issueId"].(string)
			if parent == "" {
				return ""
			}
			return TriggerID(issue, parent)
		},
		Policy: &sources.PolicyPart{
			Schema:   sources.PeoplePolicy,
			Fields:   sources.PeopleFields(sources.Localized{Zh: "Multica 用户 id", En: "Multica user id"}),
			Explicit: sources.Localized{Zh: "任何人在评论里 @ 你，都是明确的请求；智能体 @ 你只算推断候选。", En: "Anyone who mentions you in a comment makes an explicit request; an agent's mention is only an inferred candidate."},
		},
	}
}

var issueRef = regexp.MustCompile(`multica:([0-9a-f-]{36})`)

// ReadResult is one resource the reader returns.
type ReadResult struct {
	Messages     []sources.ContextMessage
	NextResource string
	Message      sources.ContextMessage
	Body         map[string]any
}

var (
	issuePath   = regexp.MustCompile(`^multica/issues/([0-9A-Za-z-]{1,64})$`)
	commentPath = regexp.MustCompile(`^multica/issues/([0-9A-Za-z-]{1,64})/comments/([1-9][0-9]{0,5})$`)
)

// Read reads a resource (SA readGithub): multica/issues/<id> is the issue as its author, time, title, status and
// description; multica/issues/<id>/comments/<page> a bounded page of comments with the next page's resource while the
// page is full. Any other path is refused before reading.
func Read(ctx context.Context, issues Issues, resource string) (ReadResult, error) {
	if issues == nil {
		return ReadResult{}, fmt.Errorf("multica: no reader")
	}
	if m := commentPath.FindStringSubmatch(resource); m != nil {
		page, _ := strconv.Atoi(m[2])
		records, err := issues.Comments(ctx, m[1], page, PageSize)
		if err != nil {
			return ReadResult{}, err
		}
		out := ReadResult{Messages: []sources.ContextMessage{}}
		for _, c := range records {
			out.Messages = append(out.Messages, sources.ContextMessage{ID: c.ID, From: c.AuthorName, At: c.At.UTC().Format(time.RFC3339),
				Text: c.Content, Bot: c.AuthorType != "member", ReplyTo: c.ParentID})
		}
		if len(records) >= PageSize {
			out.NextResource = fmt.Sprintf("multica/issues/%s/comments/%d", m[1], page+1)
		}
		return out, nil
	}
	if m := issuePath.FindStringSubmatch(resource); m != nil {
		issue, err := issues.Issue(ctx, m[1])
		if err != nil {
			return ReadResult{}, err
		}
		return ReadResult{
			Message: sources.ContextMessage{ID: issue.Identifier, From: issue.AuthorName, At: issue.CreatedAt.UTC().Format(time.RFC3339), Text: issue.Description},
			Body:    map[string]any{"identifier": issue.Identifier, "title": issue.Title, "status": issue.Status},
		}, nil
	}
	return ReadResult{}, fmt.Errorf("Unsupported Multica resource: %s", resource)
}
