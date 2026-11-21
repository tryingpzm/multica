package views

import (
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// basisFacts is a Mandate's admission facts in reading order (SA basisFacts).
func (v *View) basisFacts(basis core.MandateBasis) map[string]any {
	facts := []any{}
	add := func(name string, value any) {
		f := map[string]any{"name": name}
		if value != nil {
			f["value"] = value
		}
		facts = append(facts, f)
	}
	if basis.Sender != "" {
		add("sender", basis.Sender)
	}
	if basis.Via == "bot" {
		add("bot", nil)
	}
	if basis.Via != "followup" && basis.Addressed != "" {
		add("addressed", basis.Addressed)
	}
	if basis.Via == "followup" && basis.Authority == "" {
		add("followup", nil)
	}
	if basis.Via == "admission" {
		add("admission", basis.Cell)
	}
	var observation *core.Observation
	if basis.Via == "admission" || basis.Via == "listed" || basis.Via == "bot" {
		observation = basis.Observation
	}
	if observation != nil && basis.Via != "bot" {
		add("observation", nil)
	}
	if observation != nil && observation.Mode != "" {
		add("mode", observation.Mode)
	}
	if len(basis.Rules) > 0 {
		rules := []ruleRef{}
		for _, id := range basis.Rules {
			rules = append(rules, v.rule(id))
		}
		add("rules", rules)
	}
	authority := ""
	if basis.Via == "followup" || basis.Via == "listed" {
		authority = basis.Authority
	}
	if authority != "" {
		add("legacyAuthority", authority)
	}
	return map[string]any{"facts": facts, "legacy": authority != ""}
}

// Annotation is one of a person's annotations as the panel lists it.
type Annotation struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	At        string `json:"at"`
	TriggerID string `json:"triggerId"`
}

func (v *View) annotations(ticketID string) []Annotation {
	rows, err := v.s.Q().Query(v.s.Context(), `SELECT id::text, text, created_at, COALESCE(trigger_id, '') FROM pa_annotation
		WHERE agent_id = $1 AND ticket_id = $2 ORDER BY created_at DESC LIMIT 50`, v.id, ticketID)
	check(err)
	defer rows.Close()
	out := []Annotation{}
	for rows.Next() {
		var a Annotation
		var at time.Time
		check(rows.Scan(&a.ID, &a.Text, &at, &a.TriggerID))
		a.At = iso(at)
		out = append(out, a)
	}
	return out
}

// IssueLink is a ticket's Multica issue.
type IssueLink struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title,omitempty"`
	URL        string `json:"url"`
}

// Ticket is a ticket's page (SA ticketDetail); nil when there is none. issue resolves the ticket's Multica issue.
func (v *View) Ticket(id string, issue func(issueID string) *IssueLink) map[string]any {
	t := v.ticket(id)
	if t == nil {
		return nil
	}
	stage := core.StageOf(t).Stage
	out := v.ticketRow(t)
	human := t.Mandate.Basis.Via != "bot"
	out["mandate"] = map[string]any{"strength": t.Mandate.Strength, "policyVersion": t.Mandate.PolicyVersion, "basis": t.Mandate.Basis,
		"actor": v.actor(t.Mandate.Actor, human), "scope": t.Mandate.Scope}
	out["basis"] = v.basisFacts(t.Mandate.Basis)
	out["paused"] = v.a.Settings.Paused
	channels := []any{}
	for _, c := range t.Channels {
		channels = append(channels, v.channelLink(c))
	}
	out["channels"] = channels
	if d := v.s.Decisions().Get(t.DecisionID); d != nil {
		out["decision"] = v.decisionView(*d)
	}
	issueID := v.s.TicketIssue(t.ID)
	assignments := []any{}
	for _, a := range t.Assignments {
		item := map[string]any{"id": a.ID, "state": a.State, "createdAt": a.CreatedAt}
		for k, val := range map[string]string{"startedAt": a.StartedAt, "endedAt": a.EndedAt, "endedReason": a.EndedReason} {
			if val != "" {
				item[k] = val
			}
		}
		if a.Report != nil {
			item["report"] = a.Report
		}
		if a.StartedAt != "" && issueID != "" {
			item["ref"] = issueID
		}
		if a.State == "running" && a.Report != nil && a.Report.State != "progress" && stage != "working" {
			item["turnEndedAt"] = a.Report.At
		}
		assignments = append(assignments, item)
	}
	out["assignments"] = assignments
	inputs := []any{}
	for i := len(t.Inputs) - 1; i >= 0; i-- {
		in := t.Inputs[i]
		item := map[string]any{"id": in.ID, "kind": in.Kind, "at": in.At, "delivered": !contains(t.Undelivered, in.ID)}
		for k, val := range map[string]string{"triggerId": in.TriggerID, "note": in.Note, "from": in.From, "to": in.To, "decisionId": in.DecisionID} {
			if val != "" {
				item[k] = val
			}
		}
		if (in.Kind == "text" || in.Kind == "note") && in.Text != "" {
			item["text"] = markdown(in.Text)
		}
		if in.Kind == "trigger" {
			if tr := v.s.Triggers().Get(in.TriggerID); tr != nil {
				shown := map[string]any{"id": tr.ID, "actor": v.actor(tr.Actor, tr.Human), "human": tr.Human, "preview": preview(tr.Text, 160),
					"thread": core.ThreadKey(tr.Threads[0])}
				if d := v.s.Triggers().Disposition(tr.ID); d != nil {
					for _, h := range d.Handbacks {
						if h.TicketID == id {
							shown["handback"] = map[string]string{"reason": h.Reason, "at": h.At}
						}
					}
				}
				item["trigger"] = shown
			}
		}
		inputs = append(inputs, item)
	}
	out["inputs"] = inputs
	threads := []any{}
	for _, th := range v.threadsOf(id) {
		item := v.threadSummary(th)
		if contains(th.Context, id) {
			item["context"] = true
		}
		threads = append(threads, item)
	}
	out["threads"] = threads
	out["annotations"] = v.annotations(id)
	withdrawn := []any{}
	for _, m := range v.outbox("discarded", "1970-01-01T00:00:00Z", 200) {
		if m.TicketID == id {
			withdrawn = append(withdrawn, map[string]any{"id": m.ID, "kind": m.Kind, "text": m.Text, "at": m.At, "status": "discarded"})
		}
	}
	out["withdrawn"] = withdrawn
	if issueID != "" && issue != nil {
		if link := issue(issueID); link != nil {
			out["issue"] = link
		}
	}
	if t.Execution != nil && t.Execution.Assignment != "" {
		out["plan"] = t.Execution.Assignment
	}
	out["timeline"] = v.timeline(t, out["executor"].(map[string]string)["name"])
	return out
}
