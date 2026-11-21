// Package views builds the personal agent panel's views (SA src/app/views/*), field for field the JSON the panel in
// apps/web/features/personal-agent reads (its app/api.ts contract).
package views

import (
	"sort"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
)

// View is one request's reads of one personal agent (SA Snapshot): each Thread's subscribers, rule and sender names.
type View struct {
	a     *host.Agent
	s     *store.Store
	id    string
	links sources.ChannelLinks
	now   time.Time

	subscribed map[string][]core.Thread
	tickets    map[string]*core.Ticket
	threads    map[string]*core.Thread
	rules      map[string]string
	// defaultWorker is whether the deployment has a default worker agent for rules with no executor.
	defaultWorker bool
	names         Names
}

// Names resolves Multica records the panel shows by name; a nil func leaves the record unnamed.
type Names struct {
	Issue  func(issueID string) *IssueLink
	Target func(target core.Target) string
	// Person is the Multica user name a source's account is bound to now, or "".
	Person func(source, id string) string
	// Place is the name of the channel or group a reply goes to, or "".
	Place func(channel core.ChannelRef) string
}

// WithNames sets how issues and executors are named.
func (v *View) WithNames(n Names) *View {
	v.names = n
	return v
}

func (v *View) issue(issueID string) *IssueLink {
	if issueID == "" || v.names.Issue == nil {
		return nil
	}
	return v.names.Issue(issueID)
}

// WithDefaultWorker sets whether the deployment has a default worker agent.
func (v *View) WithDefaultWorker(ok bool) *View {
	v.defaultWorker = ok
	return v
}

// New opens a View of an agent; links are the deployment facts channel links are built from.
func New(a *host.Agent, links sources.ChannelLinks, now time.Time) *View {
	v := &View{a: a, s: a.Store, id: a.ID, links: links, now: now, tickets: map[string]*core.Ticket{}, threads: map[string]*core.Thread{},
		rules: map[string]string{}}
	for _, rule := range a.Settings.Policy.Rules {
		v.rules[rule.ID] = rule.Name
	}
	return v
}

func (v *View) dayAgo() string { return iso(v.now.Add(-24 * time.Hour)) }

func (v *View) ticket(id string) *core.Ticket {
	if t, ok := v.tickets[id]; ok {
		return t
	}
	t := v.s.Tickets().Get(id)
	v.tickets[id] = t
	return t
}

func (v *View) thread(key string) *core.Thread {
	if t, ok := v.threads[key]; ok {
		return t
	}
	t := v.s.Threads().Get(key)
	v.threads[key] = t
	return t
}

func (v *View) threadsOf(ticketID string) []core.Thread {
	if v.subscribed == nil {
		v.subscribed = map[string][]core.Thread{}
		for _, thread := range v.subscribedThreads() {
			for _, id := range thread.Subscriptions {
				v.subscribed[id] = append(v.subscribed[id], thread)
			}
		}
	}
	return v.subscribed[ticketID]
}

type ruleRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func (v *View) rule(id string) ruleRef {
	if name, ok := v.rules[id]; ok {
		return ruleRef{ID: id, Name: name}
	}
	return ruleRef{ID: id, Name: id}
}

// ActorView is a sender as the panel names it; an automated notice has an empty id and no name.
type ActorView struct {
	Source string             `json:"source"`
	ID     string             `json:"id"`
	Name   *sources.Localized `json:"name,omitempty"`
}

func named(name string) *sources.Localized { return &sources.Localized{Zh: name, En: name} }

func (v *View) actor(ref core.ActorRef, human bool) ActorView {
	if ref.ID == "" {
		return ActorView{Source: ref.Source}
	}
	// the current binding names a person before any name copied onto the lists earlier.
	if human && v.names.Person != nil {
		if name := v.names.Person(ref.Source, ref.ID); name != "" {
			return ActorView{Source: ref.Source, ID: ref.ID, Name: named(name)}
		}
	}
	entry := policy.AllowedSender(v.a.Settings, ref)
	if entry == nil {
		entry = policy.ListedBot(v.a.Settings, ref)
	}
	if entry != nil {
		name := entry.Name
		if name == "" {
			name = entry.ID
		}
		return ActorView{Source: ref.Source, ID: ref.ID, Name: named(name)}
	}
	if k := v.a.Registry.Kind(ref.Source); k != nil && k.Policy != nil && k.Policy.Unnamed != nil {
		unnamed := k.Policy.Unnamed.Person
		if !human {
			unnamed = k.Policy.Unnamed.Bot
		}
		return ActorView{Source: ref.Source, ID: ref.ID, Name: &unnamed}
	}
	return ActorView{Source: ref.Source, ID: ref.ID, Name: named(ref.ID)}
}

func (v *View) records() core.FollowupRecords { return viewRecords{v} }

type viewRecords struct{ v *View }

func (r viewRecords) TicketByID(id string) *core.Ticket   { return r.v.ticket(id) }
func (r viewRecords) ThreadByKey(key string) *core.Thread { return r.v.thread(key) }
func (r viewRecords) DispositionOf(id string) *core.TriggerDisposition {
	return r.v.s.Triggers().Disposition(id)
}

func (v *View) owners(t core.Trigger) []string { return core.FollowupOwners(t, v.records()) }

// awaitingEval is the Triggers of a Thread's queue that wait for an Eval.
func (v *View) awaitingEval(thread core.Thread) []string {
	out := []string{}
	for _, id := range thread.Pending {
		t := v.s.Triggers().Get(id)
		if t == nil || len(v.owners(*t)) == 0 {
			out = append(out, id)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

// TriggerView is a Trigger with where it went.
type TriggerView struct {
	ID          string          `json:"id"`
	Source      string          `json:"source"`
	Actor       ActorView       `json:"actor"`
	Human       bool            `json:"human"`
	At          string          `json:"at"`
	Text        string          `json:"text,omitempty"`
	Preview     string          `json:"preview"`
	URL         string          `json:"url,omitempty"`
	Threads     []string        `json:"threads"`
	Actions     map[string]bool `json:"actions"`
	Disposition DispositionView `json:"disposition"`
	ThreadTitle *string         `json:"threadTitle,omitempty"`
	Filtered    any             `json:"filtered,omitempty"`
}

type handbackView struct {
	core.Handback
	Title string `json:"title"`
}

// DispositionView is where a Trigger went.
type DispositionView struct {
	Queued     bool             `json:"queued"`
	Evaluating bool             `json:"evaluating"`
	Delivered  bool             `json:"delivered"`
	Filtered   *core.Reason     `json:"filtered,omitempty"`
	Fanout     []string         `json:"fanout"`
	Handbacks  []handbackView   `json:"handbacks"`
	Decision   *triggerDecision `json:"decision,omitempty"`
}

type triggerDecision struct {
	ID       string        `json:"id"`
	Outcome  string        `json:"outcome"`
	Reason   []core.Reason `json:"reason"`
	TicketID string        `json:"ticketId,omitempty"`
}

func (v *View) triggerView(t core.Trigger, threadDecisions []core.Decision) TriggerView {
	disposition := v.s.Triggers().Disposition(t.ID)
	if disposition == nil {
		disposition = &core.TriggerDisposition{}
	}
	home := v.thread(core.ThreadKey(t.Threads[0]))
	var decision *core.Decision
	if disposition.DecisionID != "" {
		decision = v.s.Decisions().Get(disposition.DecisionID)
	} else {
		list := threadDecisions
		if list == nil && home != nil {
			list = v.s.Decisions().ByThread(home.Key)
		}
		for i := range list {
			if contains(list[i].TriggerIDs, t.ID) {
				d := list[i]
				decision = &d
			}
		}
	}
	delivered := len(v.owners(t)) > 0
	queued := home != nil && contains(home.Pending, t.ID) && !delivered
	evaluating := home != nil && home.Evaluating != nil && contains(home.Evaluating.TriggerIDs, t.ID)
	decided := len(disposition.Acked) > 0 || (decision != nil && core.Settled(*decision))
	unjudged := disposition.Filtered != nil || (decision != nil && decision.Kind == "unavailable") || delivered ||
		(len(disposition.Fanout) > 0 && !decided) || (decision != nil && !decided)
	threads := make([]string, 0, len(t.Threads))
	for _, ref := range t.Threads {
		threads = append(threads, core.ThreadKey(ref))
	}
	handbacks := []handbackView{}
	for _, h := range disposition.Handbacks {
		title := h.TicketID
		if ticket := v.ticket(h.TicketID); ticket != nil {
			title = ticket.Title
		}
		handbacks = append(handbacks, handbackView{Handback: h, Title: title})
	}
	fanout := disposition.Fanout
	if fanout == nil {
		fanout = []string{}
	}
	out := TriggerView{ID: t.ID, Source: t.Source, Actor: v.actor(t.Actor, t.Human), Human: t.Human, At: t.At,
		Text: plain(readable(t.Text), true), Preview: preview(t.Text, 160), URL: t.URL, Threads: threads,
		Actions: map[string]bool{"requeue": t.Human && !queued && !evaluating && unjudged},
		Disposition: DispositionView{Queued: queued, Evaluating: evaluating, Delivered: delivered, Filtered: disposition.Filtered,
			Fanout: fanout, Handbacks: handbacks}}
	if decision != nil {
		out.Disposition.Decision = &triggerDecision{ID: decision.ID, Outcome: outcomeOf(*decision), Reason: decision.Reason, TicketID: decision.TicketID}
	}
	return out
}

// ticketNote is why a ticket waits for a person, in one line.
func ticketNote(t *core.Ticket, stage core.TicketStage) map[string]any {
	if stage.Stage != "person" {
		return nil
	}
	if stage.Action == "answer" && t.Attention != nil {
		return map[string]any{"text": preview(t.Attention.Question, 160), "error": false}
	}
	if stage.Action == "delivery" && t.Delivery != nil {
		return map[string]any{"text": preview(t.Delivery.Error, 160), "error": true}
	}
	report := core.LatestReport(t)
	if (stage.Action != "blocked" && stage.Action != "failed") || report == nil || report.Summary == "" {
		return nil
	}
	return map[string]any{"text": preview(strings.SplitN(report.Summary, "\n", 2)[0], 160), "error": stage.Action == "failed"}
}

// ticketSummary is SA's ticketSummary as a JSON object.
func (v *View) ticketSummary(t *core.Ticket) map[string]any {
	stage := core.StageOf(t)
	state := "open"
	if !core.LiveTicket(*t) {
		state = "ended"
	}
	out := map[string]any{"id": t.ID, "title": t.Title, "state": state, "stage": stage.Stage,
		"actions": map[string]bool{"retry": core.CanRetry(t), "stop": core.LiveTicket(*t)}, "strength": t.Mandate.Strength,
		"assignments": len(t.Assignments), "inputs": len(t.Inputs), "undelivered": len(t.Undelivered), "summary": "",
		"createdAt": t.CreatedAt, "updatedAt": t.UpdatedAt, "sessionId": ""}
	if stage.Action != "" {
		out["action"] = stage.Action
	}
	if note := ticketNote(t, stage); note != nil {
		note["text"] = v.readableIDs(note["text"].(string))
		out["note"] = note
	}
	if t.Ended != nil {
		out["ended"] = t.Ended
	}
	if t.Attention != nil {
		a := *t.Attention
		a.Question = markdown(a.Question)
		out["attention"] = a
	}
	if t.Delivery != nil && t.Delivery.Error != "" {
		out["deliveryError"] = t.Delivery.Error
	}
	if report := core.LatestReport(t); report != nil {
		out["summary"] = v.readableIDs(report.Summary)
	}
	// A started ticket's worker runs on its Multica issue, which the panel opens.
	for _, a := range t.Assignments {
		if a.StartedAt != "" {
			out["sessionId"] = v.s.TicketIssue(t.ID)
			break
		}
	}
	return out
}

func (v *View) threadURL(thread core.Thread) string {
	if url := attribute(thread.Meta["url"]); url != "" {
		return url
	}
	if first := v.firstTrigger(thread.Key, false); first != nil {
		return first.URL
	}
	return ""
}

func facts(thread core.Thread) any {
	if f, ok := thread.Meta["facts"]; ok && f != nil {
		return f
	}
	return map[string]any{}
}

func (v *View) threadFacts(thread core.Thread) map[string]any {
	out := map[string]any{"key": thread.Key, "source": thread.Ref.Source, "title": v.threadTitle(thread), "facts": facts(thread)}
	if url := v.threadURL(thread); url != "" {
		out["url"] = url
	}
	return out
}

func (v *View) ticketLine(t *core.Ticket) map[string]any {
	out := v.ticketSummary(t)
	threads := []any{}
	for _, thread := range v.threadsOf(t.ID) {
		threads = append(threads, v.threadFacts(thread))
	}
	out["threads"] = threads
	return out
}

func (v *View) threadTitle(thread core.Thread) string {
	if label := attribute(thread.Meta["label"]); label != "" {
		return label
	}
	// a Multica thread is named by its issue, never its internal key.
	if thread.Ref.Source == "multica" {
		if link := v.issue(thread.Ref.ID); link != nil {
			return strings.TrimSpace(link.Identifier + " " + link.Title)
		}
	}
	first := v.firstTrigger(thread.Key, true)
	if first == nil {
		first = v.firstTrigger(thread.Key, false)
	}
	if first != nil {
		return preview(first.Text, 80)
	}
	return thread.Key
}

func threadStage(thread core.Thread, pending int, tickets []map[string]string) string {
	if attribute(thread.Meta["pollError"]) != "" {
		return "error"
	}
	if thread.Evaluating != nil {
		return "evaluating"
	}
	if pending > 0 {
		return "queued"
	}
	for _, t := range tickets {
		if t["state"] == "open" {
			return "subscribed"
		}
	}
	if len(tickets) > 0 {
		return "dormant"
	}
	return "unsubscribed"
}

func (v *View) threadSummary(thread core.Thread) map[string]any {
	tickets := []map[string]string{}
	for _, id := range thread.Subscriptions {
		t := v.ticket(id)
		entry := map[string]string{"id": id, "title": id, "state": "unknown"}
		if t != nil {
			entry["title"] = t.Title
			entry["state"] = "ended"
			if core.LiveTicket(*t) {
				entry["state"] = "open"
			}
		}
		tickets = append(tickets, entry)
	}
	pending := len(v.awaitingEval(thread))
	lastAt := thread.LastAt
	if lastAt == "" {
		lastAt = thread.CreatedAt
	}
	out := map[string]any{"key": thread.Key, "source": thread.Ref.Source, "title": v.threadTitle(thread),
		"stage": threadStage(thread, pending, tickets), "lastAt": lastAt, "createdAt": thread.CreatedAt,
		"subscriptions": len(thread.Subscriptions), "tickets": tickets, "pending": pending, "facts": facts(thread)}
	if latest := v.threadTriggers(thread.Key, 1, "", "", false); len(latest) > 0 {
		t := latest[0]
		out["latest"] = map[string]any{"actor": v.actor(t.Actor, t.Human), "human": t.Human, "at": t.At, "preview": preview(t.Text, 120)}
	}
	if e := thread.Evaluating; e != nil {
		out["evaluating"] = map[string]any{"id": e.ID, "strength": e.Strength, "triggers": len(e.TriggerIDs), "startedAt": e.StartedAt,
			"deadline": e.Deadline}
	}
	if poll := attribute(thread.Meta["pollError"]); poll != "" {
		out["pollError"] = poll
	}
	if url := v.threadURL(thread); url != "" {
		out["url"] = url
	}
	return out
}

// ChannelLinkView is a reply channel's link.
type ChannelLinkView struct {
	Source string            `json:"source"`
	URL    string            `json:"url"`
	Label  sources.Localized `json:"label"`
}

func (v *View) channelLink(channel core.ChannelRef) ChannelLinkView {
	link := v.a.Registry.Link(channel, v.links)
	url := link.URL
	if link.TriggerID != "" {
		if t := v.s.Triggers().Get(link.TriggerID); t != nil && t.URL != "" {
			url = t.URL
		}
	}
	return ChannelLinkView{Source: channel.Source, URL: url, Label: link.Label}
}

func audit(d core.Decision) map[string]any {
	if d.Audit == nil {
		return map[string]any{}
	}
	return d.Audit
}

func outcomeOf(d core.Decision) string {
	if would, _ := audit(d)["would_accept"].(bool); d.Kind == "ignore" && would {
		return "wouldAccept"
	}
	return d.Kind
}

func (v *View) decisionView(d core.Decision) map[string]any {
	texts := []string{}
	for _, id := range d.TriggerIDs {
		if t := v.s.Triggers().Get(id); t != nil {
			texts = append(texts, preview(t.Text, 160))
		}
	}
	rules := []any{}
	a := audit(d)
	judged, _ := a["rule_assessments"].([]any)
	matches := judged == nil
	if matches {
		judged, _ = a["matches"].([]any)
	}
	for _, item := range judged {
		m, _ := item.(map[string]any)
		id, _ := m["rule_id"].(string)
		if id == "" {
			continue
		}
		matched, _ := m["matched"].(bool)
		reason, _ := m["reason"].(string)
		r := v.rule(id)
		rules = append(rules, map[string]any{"id": r.ID, "name": r.Name, "matched": matched || matches, "reason": reason})
	}
	out := map[string]any{"id": d.ID, "kind": d.Kind, "outcome": outcomeOf(d), "reason": reasons(d.Reason), "at": d.At, "strength": d.Strength,
		"triggerIds": d.TriggerIDs, "thread": d.ThreadKey, "rules": rules, "preview": strings.Join(texts, " / ")}
	if d.TicketID != "" {
		out["ticketId"] = d.TicketID
		if t := v.ticket(d.TicketID); t != nil {
			out["ticketTitle"] = t.Title
		}
	}
	if direction, ok := a["direction"].(map[string]any); ok {
		out["direction"] = direction
	}
	if thread := v.thread(d.ThreadKey); thread != nil {
		out["threadTitle"] = v.threadTitle(*thread)
	}
	return out
}

func reasons(list []core.Reason) []core.Reason {
	if list == nil {
		return []core.Reason{}
	}
	return list
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func newestFirst(list []core.Ticket) {
	sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt > list[j].UpdatedAt })
}

// Page is a page of a list.
type Page struct {
	Items      []any `json:"items"`
	Total      int   `json:"total"`
	NextOffset *int  `json:"next_offset"`
}

func page[T any](items []T, offset, limit int, view func(T) any) Page {
	out := Page{Items: []any{}, Total: len(items)}
	for i := offset; i < len(items) && i < offset+limit; i++ {
		out.Items = append(out.Items, view(items[i]))
	}
	if offset+limit < len(items) {
		next := offset + limit
		out.NextOffset = &next
	}
	return out
}
