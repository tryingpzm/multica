// Package sources is a Go translation of dsh-superagent-prototype src/sources/kind.ts, registry.ts, host.ts and
// admission-context.ts: the SourceKind plugin contract and the Registry through which the engine, Gate, panel and Outbox
// reach every Source (design §10).
package sources

import (
	"context"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// Signed marks a reply the bot sends for someone's agent (design Q4/Q5).
func Signed(owner, text string) string {
	if strings.TrimSpace(owner) == "" {
		return text
	}
	return "（代 " + owner + " 回复）\n" + text
}

// Relayed reports whether a bot's text is a reply the bot sent for someone's agent; it never reaches a Source again, so
// agents do not answer each other in a loop.
func Relayed(text string) bool {
	head, _, _ := strings.Cut(text, "\n")
	return strings.HasPrefix(head, "（代 ") && strings.HasSuffix(head, " 回复）")
}

// Localized is a text in both interface languages.
type Localized struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// Option is one choice of a select field or column.
type Option struct {
	Value string    `json:"value"`
	Label Localized `json:"label"`
}

// Column is one column of a rows field.
type Column struct {
	Key      string    `json:"key"`
	Label    Localized `json:"label"`
	Type     string    `json:"type"` // text | select
	Pattern  string    `json:"pattern,omitempty"`
	Optional bool      `json:"optional,omitempty"`
	Options  []Option  `json:"options,omitempty"`
	// Choose picks the column's value from the Source's labels; Refresh names the button that reloads them (SA).
	Choose  *Localized `json:"choose,omitempty"`
	Refresh *Localized `json:"refresh,omitempty"`
}

// FieldSpec is a configuration form field: switch, select, number, text, fact or rows (SA FieldSpec).
type FieldSpec struct {
	Key      string     `json:"key"`
	Type     string     `json:"type"`
	Label    Localized  `json:"label"`
	Hint     *Localized `json:"hint,omitempty"`
	Advanced bool       `json:"advanced,omitempty"`
	Options  []Option   `json:"options,omitempty"`
	Min      int        `json:"min,omitempty"`
	Max      int        `json:"max,omitempty"`
	Pattern  string     `json:"pattern,omitempty"`
	Columns  []Column   `json:"columns,omitempty"`
}

// Config is a Source instance's validated configuration.
type Config = map[string]any

// Schema validates a stored or submitted configuration and fills its defaults; it returns an error for an invalid one.
type Schema func(raw map[string]any) (Config, error)

// AdmissionRowView is one receiving scope of the admission table as the panel shows it.
type AdmissionRowView struct {
	ID    string    `json:"id"`
	Label string    `json:"label"`
	Note  Localized `json:"note"`
	Cells struct {
		ListedUnaddressed   bool `json:"listedUnaddressed"`
		UnlistedAddressed   bool `json:"unlistedAddressed"`
		UnlistedUnaddressed bool `json:"unlistedUnaddressed"`
	} `json:"cells"`
}

// PolicyPart is a Source kind's extension of the Policy (SA SourcePolicyPart). Schema validates the part without
// `admission`, which the Registry validates for every kind; its result always holds `allowed`.
type PolicyPart struct {
	Schema   Schema
	Fields   []FieldSpec
	Explicit Localized
	Unnamed  *struct{ Person, Bot Localized }
	Rows     func(config Config) []AdmissionRowView
}

// ChannelLink is the panel's link for a channel address.
type ChannelLink struct {
	URL       string    `json:"url"`
	Label     Localized `json:"label"`
	TriggerID string    `json:"triggerId,omitempty"`
}

// ChannelLinks are the deployment facts links are built from.
type ChannelLinks struct {
	AppURL string
}

// Send results.
const (
	Sent    = "sent"
	Gone    = "gone"
	Waiting = "waiting"
)

// Channel is a kind's outbound adapter (SA SourceChannel). Send is idempotent by message ID; it returns Gone when the
// conversation no longer accepts replies and an error when a later retry may succeed.
type Channel struct {
	Allowed func(address map[string]string, config Config) bool
	Send    func(ctx context.Context, message core.OutboundMessage) (string, error)
	Link    func(address map[string]string, links ChannelLinks) ChannelLink
}

// ContextMessage is a normalized message a thread context reader returns; Bot marks a non-human sender.
type ContextMessage struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	At      string `json:"at"`
	Text    string `json:"text"`
	Bot     bool   `json:"bot,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
	URL     string `json:"url,omitempty"`
}

// Context parts.
const (
	PartThread = "thread"
	PartRoot   = "root"
	PartParent = "parent"
)

// ContextPart is a page of messages; Next is the opaque cursor of the next page, empty on the last.
type ContextPart struct {
	Messages []ContextMessage `json:"messages"`
	Next     string           `json:"next,omitempty"`
	Body     map[string]any   `json:"body,omitempty"`
	Status   string           `json:"status,omitempty"`
}

// ThreadContext is a kind's bounded context reader for assessments (SA ThreadContext, without images).
type ThreadContext struct {
	MessageID func(trigger core.Trigger) string
	Read      func(ctx context.Context, trigger core.Trigger, part, cursor string, config Config) (ContextPart, error)
}

// TriggerContext is a ThreadContext bound to one Trigger and its kind's configuration.
type TriggerContext struct {
	MessageID string
	Read      func(ctx context.Context, part, cursor string) (ContextPart, error)
}

// Found is what a search query names: Thread keys and Trigger search terms.
type Found struct {
	Threads []string `json:"threads"`
	Terms   []string `json:"terms"`
}

// Kind is a pluggable Source kind (SA SourceKind). Nil optional members behave as SA's absent ones.
type Kind struct {
	Kind        string
	Title       Localized
	Description Localized
	Schema      Schema
	Fields      []FieldSpec
	// Health names the health entries this kind owns; no other kind may report them.
	Health []string
	// Scope is the listening rule; without it the kind listens to everything it produces and observes none of it.
	Scope         func(meta map[string]any, config Config) core.ScopeResult
	Channel       *Channel
	ThreadContext *ThreadContext
	Find          func(query string) Found
	RepliesTo     func(trigger core.Trigger) string
	Policy        *PolicyPart
	// Facts are derived notes shown under configuration fields, by field key (SA facts).
	Facts func(config Config) map[string]any
}

// Instance is a Source's resolved switch and configuration.
type Instance struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	Config    Config `json:"config"`
	Installed bool   `json:"installed"`
}
