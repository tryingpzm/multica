// Package cards renders the bounded, model-facing views of triggers, tickets, inputs and reports that the Router and
// the Workers read: a Go translation of dsh-superagent-prototype src/app/cards.ts, src/app/notices.ts and the message
// helpers of src/executor/dsh.ts. Texts longer than a view's limit are cut and point at the resource holding them.
package cards

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

var spaces = regexp.MustCompile(`\s+`)

func cut(text string, limit int) string {
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	return string(r[:limit])
}

// mentionMark is a channel's @{name} mark of a person; cards show it as @name.
var mentionMark = regexp.MustCompile(`@\{([^{}\s]{1,64})\}`)

func readable(text string) string { return mentionMark.ReplaceAllString(text, "@$1") }

func length(text string) int { return len([]rune(text)) }

// Preview collapses whitespace and keeps 160 characters.
func Preview(text string) string { return cut(spaces.ReplaceAllString(readable(text), " "), 160) }

// Capped is text up to limit, or its first limit characters with an ellipsis, its length and the resource to read.
func Capped(text string, limit int, read string) map[string]any {
	if length(text) <= limit {
		return map[string]any{"text": text}
	}
	return map[string]any{"text": cut(text, limit) + "…", "chars": length(text), "read": read}
}

// TriggerChars and AssignmentChars bound inline trigger and input texts in worker messages.
const (
	TriggerChars    = 600
	AssignmentChars = 1500
)

// Link is a trigger's source links.
type Link struct {
	SourceURL     string
	SourceURLRead string
}

// TriggerMessage is a Trigger as a Worker reads it: author, thread, time, text up to TriggerChars, its links, the
// routing note and a preview of the message it replies to when the worker has not seen that one.
func TriggerMessage(t core.Trigger, link Link, note string, repliesTo *core.Trigger) map[string]any {
	m := map[string]any{"trigger_id": t.ID, "from": t.Actor.ID, "thread": core.ThreadKey(t.Threads[0]), "at": t.At}
	for k, v := range Capped(readable(t.Text), TriggerChars, "trigger/"+t.ID) {
		m[k] = v
	}
	if link.SourceURL != "" {
		m["source_url"] = link.SourceURL
	}
	if link.SourceURLRead != "" {
		m["source_url_read"] = link.SourceURLRead
	}
	if note != "" {
		m["routing_note"] = note
	}
	if repliesTo != nil {
		m["replies_to"] = map[string]any{"trigger_id": repliesTo.ID, "preview": Preview(repliesTo.Text)}
	}
	return m
}

// TriggerCard is a Trigger in a list.
func TriggerCard(t core.Trigger, d *core.TriggerDisposition) map[string]any {
	m := map[string]any{"trigger_id": t.ID, "source": t.Source, "actor": t.Actor.ID, "thread": core.ThreadKey(t.Threads[0]), "human": t.Human, "at": t.At,
		"preview": Preview(t.Text), "chars": length(t.Text), "read": "trigger/" + t.ID}
	if t.URL != "" {
		m["source_url"] = t.URL
	}
	if d != nil {
		disp := map[string]any{"delivered_to": d.Fanout}
		if d.DecisionID != "" {
			disp["decision_id"] = d.DecisionID
		}
		m["disposition"] = disp
	}
	return m
}

// BatchLine is a peer Trigger of a ticket's acceptance batch.
func BatchLine(t core.Trigger) map[string]any {
	return map[string]any{"trigger_id": t.ID, "from": t.Actor.ID, "at": t.At, "preview": Preview(t.Text), "read": "trigger/" + t.ID}
}

// CardState is the state a ticket card shows: an open ticket's stage, an ended ticket's outcome.
func CardState(t core.Ticket) string {
	if t.Ended != nil {
		return string(t.Ended.Outcome)
	}
	stage := core.StageOf(&t)
	switch {
	case stage.Stage == "person":
		return "person/" + stage.Action
	case stage.Stage == "idle":
		return "idle"
	case stage.Stage == "working" && stage.Action != "":
		return "working/queued"
	}
	return "working"
}

// TicketCard is a ticket in a list.
func TicketCard(t core.Ticket) map[string]any {
	m := map[string]any{"ticket_id": t.ID, "title": t.Title, "state": CardState(t), "strength": t.Mandate.Strength, "read": "ticket/" + t.ID}
	if t.Ended != nil {
		reason := t.Ended.Reason
		if reason == "" {
			reason = string(t.Ended.Outcome)
		}
		m["ended"] = map[string]any{"at": t.Ended.At, "by": t.Ended.By, "reason": reason}
	}
	if t.Attention != nil {
		a := map[string]any{"question": t.Attention.Question}
		if t.Attention.TriggerID != "" {
			a["trigger_id"] = t.Attention.TriggerID
		}
		m["attention"] = a
	}
	summary := ""
	if r := core.LatestReport(&t); r != nil {
		summary = r.Summary
	}
	m["preview"] = Preview(summary)
	return m
}

// Brief is a ticket card without the report preview.
func Brief(t core.Ticket) map[string]any {
	m := TicketCard(t)
	delete(m, "preview")
	return m
}

// RelatedTickets lists up to 8 of a Thread's tickets, open ones first, newest first.
func RelatedTickets(tickets []core.Ticket) map[string]any {
	order := append([]core.Ticket(nil), tickets...)
	sort.SliceStable(order, func(i, j int) bool {
		a, b := order[i], order[j]
		if (a.Ended == nil) == (b.Ended == nil) {
			return b.CreatedAt < a.CreatedAt
		}
		return a.Ended == nil
	})
	if len(order) > 8 {
		order = order[:8]
	}
	cards := make([]map[string]any, 0, len(order))
	for _, t := range order {
		cards = append(cards, TicketCard(t))
	}
	return map[string]any{"related_tickets": cards, "tickets_total": len(tickets)}
}

// TextPage slices text by characters so concatenated pages rebuild it.
func TextPage(resource, text string, offset, limit int) map[string]any {
	r := []rune(text)
	if offset > len(r) {
		offset = len(r)
	}
	end := offset + limit
	if end > len(r) {
		end = len(r)
	}
	var next any
	if end < len(r) {
		next = end
	}
	return map[string]any{"resource": resource, "offset": offset, "total_chars": len(r), "next_offset": next, "text": string(r[offset:end])}
}

// InputResource is where a Worker reads an input's whole text.
func InputResource(ticketID, inputID string) string {
	return "ticket/" + ticketID + "/input/" + core.Key(inputID)
}

const resumed = "This ticket ended earlier and the message that follows, on its thread, resumed it under the same mandate. If it asks for " +
	"more within the original scope, inspect the worktree, git history and PR state and continue; if it asks for work beyond that scope or " +
	"for another ticket, hand it back with multica ticket handback and report dropped; if it needs nothing, such as thanks, " +
	"report dropped without a message. Report ready when the follow-up is done. Do not repeat completed work."

func policyMessage(effect *core.PolicyEffect) string {
	if effect == nil {
		return "Policy change: the access or admission rules changed. Check that the remaining work stays within the assignment."
	}
	lines := []string{"Policy change:"}
	for _, rule := range effect.Rules {
		if rule.Change == "disabled" {
			lines = append(lines, "Rule "+rule.Name+" ("+rule.ID+"), which this ticket was accepted under, no longer applies: finish only a change within it, or report dropped.")
		} else {
			lines = append(lines, "Rule "+rule.Name+" ("+rule.ID+"), which this ticket was accepted under, changed. Keep the remaining work within it as it reads now: "+rule.Instruction)
		}
	}
	if effect.Proactive != nil && !*effect.Proactive {
		lines = append(lines, "Work inferred from a rule is no longer accepted, as the accept mode is no longer auto: finish only "+
			"what is in progress within the assignment, or report dropped, and take on nothing new.")
	}
	if effect.RequesterAllowed != nil && !*effect.RequesterAllowed {
		who := "who asked for it"
		if effect.Requester != nil {
			who = effect.Requester.ID + " on " + effect.Requester.Source
		}
		lines = append(lines, "The sender this ticket is for, "+who+", may no longer start work here: finish the assignment within its scope, and take no new requests from them on it.")
	}
	return strings.Join(lines, "\n")
}

// InputMessage is an Input as its Worker reads it; ok is false for a legacy reopen Input, which is not delivered.
func InputMessage(input core.TicketInput, trigger map[string]any) (string, bool) {
	switch input.Kind {
	case "trigger":
		m := map[string]any{}
		if trigger != nil {
			for k, v := range trigger {
				m[k] = v
			}
		} else {
			m["trigger_id"] = input.TriggerID
			m["read"] = "trigger/" + input.TriggerID
		}
		if input.Context {
			m["context"] = true
		}
		data, _ := json.Marshal(m)
		return string(data), true
	case "policy-changed":
		return policyMessage(input.Effect), true
	case "resumed":
		return resumed, true
	case "note":
		return "Note from the orchestrator, for context only: " + input.Text, true
	case "text":
		return input.Text, true
	}
	return "", false
}

// Relay is a message for the Router: ID makes its delivery idempotent.
type Relay struct {
	ID   string
	Text string
}

func encode(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

// ResultRelay is a worker's result, blocker, end or answer for the Router; a summary over 600 characters is cut. The
// same report on a ticket is one relay, so a Worker repeating itself does not flood the Router.
func ResultRelay(t core.Ticket, state, text, replyTo string) Relay {
	id := core.Key("worker-result", t.ID, state, text, replyTo)
	report := map[string]any{"kind": "worker_report", "ticket": Brief(t), "trigger_id": t.Origin.TriggerID, "state": state, "read": "ticket/" + t.ID + "/result"}
	if replyTo != "" {
		report["answer_to"] = replyTo
	}
	if state == "ready" {
		// the host does not deliver a result; the Router does, in the conversation it answers.
		to := replyTo
		if to == "" {
			to = t.Origin.TriggerID
		}
		report["next_action"] = "The host has not delivered this result. Reply to the person with multica ticket reply purpose=result ticket_id=" +
			t.ID + " trigger_id=" + to + " and a brief text of the result."
	}
	if length(text) > 600 {
		report["summary"] = cut(text, 600)
		report["chars"] = length(text)
	} else {
		report["summary"] = text
	}
	return Relay{ID: "report:" + id, Text: encode(report)}
}

// ReportRelay is a recorded report as the Router hears of it: a question is a worker_attention relay, a result,
// blocker, end or answer a worker_report relay; routine progress without an answer stays internal (ok false).
func ReportRelay(t core.Ticket, r core.WorkerReport) (Relay, bool) {
	if r.Question != nil && *r.Question != "" && t.Attention != nil {
		return Relay{ID: "attention:" + t.ID + ":" + t.Attention.ID,
			Text: encode(map[string]any{"kind": "worker_attention", "ticket": Brief(t), "state": r.State, "read": "ticket/" + t.ID + "/result"})}, true
	}
	if r.State == "progress" && r.Answers == "" {
		return Relay{}, false
	}
	return ResultRelay(t, r.State, r.Summary, r.Answers), true
}

const followupNote = "Its ticket's worker has it and decides; act only if the person asks to stop the work " +
	"(multica ticket route kind stop with this trigger_id)."

// FollowupRelay is a person's message that reached tickets, as the Router hears of it.
func FollowupRelay(message map[string]any, tickets []map[string]any) Relay {
	m := map[string]any{"kind": "follow_up", "delivered_to": tickets, "note": followupNote}
	for k, v := range message {
		m[k] = v
	}
	return Relay{ID: "followup:" + message["trigger_id"].(string), Text: encode(m)}
}
