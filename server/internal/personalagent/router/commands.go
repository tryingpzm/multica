package router

import (
	"errors"
	"fmt"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/cards"
	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
)

// RouteItem is one decision of multica ticket route (SA orchestrator_route).
type RouteItem struct {
	Kind            string `json:"kind"`
	TriggerID       string `json:"trigger_id"`
	TicketID        string `json:"ticket_id,omitempty"`
	Title           string `json:"title,omitempty"`
	WorktreeName    string `json:"worktree_name,omitempty"`
	Acknowledgement string `json:"acknowledgement,omitempty"`
	Note            string `json:"note,omitempty"`
	Reason          string `json:"reason,omitempty"`
	// Rules are the ids of the Policy rules an accept falls under; the first one with an agent decides who works it.
	Rules []string `json:"rules,omitempty"`
}

type field struct {
	name  string
	reads []string
	needs []string
	value func(RouteItem) string
	limit int
}

var fields = []field{
	{"ticket_id", []string{"attach", "stop"}, []string{"attach", "stop"}, func(i RouteItem) string { return i.TicketID }, 0},
	{"title", []string{"accept"}, []string{"accept"}, func(i RouteItem) string { return i.Title }, 200},
	{"worktree_name", []string{"accept"}, []string{"accept"}, func(i RouteItem) string { return i.WorktreeName }, 32},
	{"acknowledgement", []string{"accept"}, nil, func(i RouteItem) string { return i.Acknowledgement }, 0},
	{"note", []string{"accept", "attach"}, nil, func(i RouteItem) string { return i.Note }, 0},
	{"reason", []string{"ignore", "stop"}, []string{"ignore"}, func(i RouteItem) string { return i.Reason }, 500},
}

func has(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// invalid says why an item cannot run: a field its kind needs is missing, or one it reads breaks its limit.
func invalid(item RouteItem) string {
	if !has([]string{"accept", "attach", "ignore", "stop"}, item.Kind) {
		return "kind is one of accept, attach, ignore or stop"
	}
	for _, f := range fields {
		if !has(f.reads, item.Kind) {
			continue
		}
		v := strings.TrimSpace(f.value(item))
		if v == "" {
			if has(f.needs, item.Kind) {
				return item.Kind + " needs " + f.name
			}
			continue
		}
		if f.limit > 0 && len([]rune(v)) > f.limit {
			return fmt.Sprintf("%s: at most %d characters", f.name, f.limit)
		}
	}
	return ""
}

func fromDecision(d core.Decision) map[string]any {
	m := map[string]any{"decision": d.Kind, "reason": core.ReasonText(d.Reason...)}
	if d.TicketID != "" {
		m["ticket_id"] = d.TicketID
	}
	return m
}

// Route is multica ticket route: each item runs as its own Engine command, in order; a refused one leaves the others.
func Route(a *host.Agent, items []RouteItem) []map[string]any {
	decided := map[string]string{}
	results := make([]map[string]any, 0, len(items))
	for _, item := range items {
		head := map[string]any{"trigger_id": item.TriggerID, "kind": item.Kind}
		if problem := invalid(item); problem != "" {
			head["refused"] = problem
			results = append(results, head)
			continue
		}
		result, triggers, err := route(a, item, decided)
		if err != nil {
			var user *core.UserError
			if errors.As(err, &user) {
				head["refused"] = user.Error()
			} else {
				head["refused"] = "Not recorded: " + err.Error() + "; retry this item"
			}
			results = append(results, head)
			continue
		}
		for _, id := range triggers {
			if _, ok := decided[id]; !ok {
				decided[id] = item.TriggerID
			}
		}
		for k, v := range result {
			head[k] = v
		}
		if id, _ := result["ticket_id"].(string); id != "" {
			if t := a.Store.Tickets().Get(id); t != nil {
				head["ticket"] = cards.TicketCard(*t)
			}
			delete(head, "ticket_id")
		}
		results = append(results, head)
	}
	return results
}

func route(a *host.Agent, item RouteItem, decided map[string]string) (map[string]any, []string, error) {
	if earlier, ok := decided[item.TriggerID]; ok && item.Kind == "ignore" {
		return map[string]any{"refused": "Decided in this call with " + earlier + ", whose Decision took its batch"}, nil, nil
	}
	switch item.Kind {
	case "accept":
		before := ""
		if d := a.Store.Triggers().Disposition(item.TriggerID); d != nil {
			before = d.DecisionID
		}
		code := core.Reason{Code: "explicitRouted"}
		d, err := a.Engine.Decide(item.TriggerID, core.Outcome{Kind: "accept", Title: item.Title, Scope: item.Title, ReasonCode: &code,
			Acknowledgement: item.Acknowledgement, Name: item.WorktreeName, Note: item.Note, Rules: item.Rules})
		if err != nil {
			return nil, nil, err
		}
		if d.Kind != "accept" || d.TicketID == "" {
			return map[string]any{"refused": "Ticket was not created: " + core.ReasonText(d.Reason...)}, nil, nil
		}
		if d.ID == before {
			return map[string]any{"decision": "accept", "reason": "Already accepted as " + d.TicketID + "; title and acknowledgement unchanged", "ticket_id": d.TicketID}, nil, nil
		}
		return fromDecision(d), d.TriggerIDs, nil
	case "attach":
		t, err := a.Engine.Attach(item.TicketID, item.TriggerID, item.Note)
		if err != nil {
			return nil, nil, err
		}
		return map[string]any{"reason": "Attached: the ticket follows the trigger's thread and receives the trigger", "ticket_id": t.ID}, nil, nil
	case "stop":
		outcome := core.Outcome{Kind: "stop", TicketID: item.TicketID, Said: item.Reason}
		if item.Reason == "" {
			outcome.ReasonCode = &core.Reason{Code: "humanStop"}
		}
		d, err := a.Engine.Decide(item.TriggerID, outcome)
		if err != nil {
			return nil, nil, err
		}
		return fromDecision(d), d.TriggerIDs, nil
	}
	d, err := a.Engine.Decide(item.TriggerID, core.Outcome{Kind: "ignore", Said: item.Reason})
	if err != nil {
		return nil, nil, err
	}
	return fromDecision(d), d.TriggerIDs, nil
}

// ReplyArgs is multica ticket reply (SA orchestrator_reply).
type ReplyArgs struct {
	TriggerID   string `json:"trigger_id,omitempty"`
	TicketID    string `json:"ticket_id,omitempty"`
	Purpose     string `json:"purpose"`
	HumanAction string `json:"human_action,omitempty"`
	Text        string `json:"text"`
}

// Reply queues a reply through the Engine's gate; the Outbox sends it, or holds it for confirmation.
func Reply(a *host.Agent, args ReplyArgs) (map[string]any, error) {
	if !has([]string{"answer", "result", "attention"}, args.Purpose) {
		return nil, errors.New("purpose is one of answer, result or attention")
	}
	if strings.TrimSpace(args.Text) == "" {
		return nil, errors.New("text is required")
	}
	reply, err := a.Engine.Reply(core.ReplyRequest{Purpose: args.Purpose, TicketID: args.TicketID, TriggerID: args.TriggerID,
		Question: strings.TrimSpace(args.HumanAction), Text: args.Text})
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": outboxStatus(a, reply.ID, reply.Status)}, nil
}

func outboxStatus(a *host.Agent, id, fallback string) string {
	if a.Outbox != nil {
		if s := a.Outbox.Receipt(id); s != "" {
			return s
		}
	}
	return fallback
}
