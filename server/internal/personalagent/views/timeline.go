package views

import (
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// timeline is a ticket's story in time order: the Triggers it got, the Router's decision, each dispatch to
// the worker, the worker's reports, and the replies sent. Each item is one line; detail is its full text.
func (v *View) timeline(t *core.Ticket, executor string) []any {
	type item struct {
		at  string
		row map[string]any
	}
	items := []item{}
	add := func(kind, at string, fields map[string]any) {
		row := map[string]any{"kind": kind, "at": at}
		for k, val := range fields {
			row[k] = val
		}
		items = append(items, item{at, row})
	}
	seen := map[string]bool{}
	trigger := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		if tr := v.s.Triggers().Get(id); tr != nil {
			add("trigger", tr.At, map[string]any{"actor": v.actor(tr.Actor, tr.Human), "text": preview(tr.Text, 120), "detail": plain(tr.Text, true)})
		}
	}
	for _, id := range t.Origin.TriggerIDs {
		trigger(id)
	}
	trigger(t.Origin.TriggerID)
	if d := v.s.Decisions().Get(t.DecisionID); d != nil {
		for _, id := range d.TriggerIDs {
			trigger(id)
		}
		add("decision", d.At, map[string]any{"outcome": outcomeOf(*d), "reason": reasons(d.Reason)})
	}
	for _, in := range t.Inputs {
		if in.Kind == "trigger" {
			trigger(in.TriggerID)
		}
	}
	for _, a := range t.Assignments {
		add("assign", a.CreatedAt, map[string]any{"executor": executor})
		if r := a.Report; r != nil {
			summary := v.readableIDs(r.Summary)
			add("report", r.At, map[string]any{"state": r.State, "text": preview(strings.SplitN(summary, "\n", 2)[0], 160), "detail": summary})
		}
	}
	for _, m := range v.outbox("sent", "1970-01-01T00:00:00Z", 200) {
		if m.TicketID == t.ID {
			add("reply", m.At, map[string]any{"source": m.Channel.Source, "text": preview(m.Text, 120), "detail": m.Text})
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].at < items[j].at })
	out := []any{}
	for _, it := range items {
		out = append(out, it.row)
	}
	return out
}
