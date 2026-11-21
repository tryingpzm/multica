package executor

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/cards"
	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/plans"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
)

// ReportArgs is multica ticket report: Kind is progress, ready, waiting, blocked, failed or dropped. HumanAction nil keeps
// the pending question, "" clears it; QuestionFor names the person's message that raised it; ReplyTo the person's
// question the report answers; MR the merge request the work is in.
type ReportArgs struct {
	Kind        string  `json:"kind"`
	Summary     string  `json:"summary"`
	HumanAction *string `json:"human_action,omitempty"`
	QuestionFor string  `json:"question_for_trigger_id,omitempty"`
	ReplyTo     string  `json:"reply_to,omitempty"`
	MR          string  `json:"mr,omitempty"`
	// Plan is the executor's plan for an issue that needs the owner.
	Plan *PlanArgs `json:"plan,omitempty"`
	// Follow is an issue the Worker handed the work to; the ticket follows it (path 1).
	Follow string `json:"follow,omitempty"`
}

// PlanArgs is a report's plan.
type PlanArgs struct {
	IssueID     string         `json:"issue_id"`
	IssueStatus string         `json:"issue_status"`
	Conclusion  string         `json:"conclusion"`
	Reason      string         `json:"reason"`
	Actions     []plans.Action `json:"actions"`
}

// ReportKinds are the states a worker reports.
var ReportKinds = []string{"progress", "ready", "waiting", "blocked", "failed", "dropped"}

// Report records a worker's report on its ticket and relays what the Router must hear.
func (x *Executor) Report(a *host.Agent, ticketID string, args ReportArgs) (map[string]any, error) {
	known := false
	for _, k := range ReportKinds {
		known = known || k == args.Kind
	}
	if !known {
		return nil, errors.New("kind is one of " + strings.Join(ReportKinds, ", "))
	}
	summary := strings.TrimSpace(args.Summary)
	if args.MR != "" {
		summary = strings.TrimSpace(summary + "\nMR: " + args.MR)
	}
	if summary == "" {
		return nil, errors.New("summary is required")
	}
	current := a.Store.Tickets().Get(ticketID)
	if current == nil || !core.LiveTicket(*current) {
		return nil, errors.New("Ticket ended (its work finished or a person stopped it); do not resume it")
	}
	report := core.WorkerReport{State: args.Kind, Summary: summary, Question: args.HumanAction, AskedBy: args.QuestionFor, Answers: args.ReplyTo}
	ticket, err := x.Reported(a, ticketID, report)
	if err != nil {
		return nil, err
	}
	// a blocked ticket's issue is blocked too, so 需要我处理 picks it up (SA tickets.ts holds automated inputs
	// back on a blocked report until a person writes; Multica's own surface for "a person must act" is the status).
	if args.Kind == "blocked" {
		if issueID := a.Store.TicketIssue(ticketID); issueID != "" && x.o.Runs != nil {
			if err := x.o.Runs.SetStatus(context.Background(), a.Agent, issueID, "blocked"); err != nil {
				return nil, err
			}
		}
	}
	_, relayed := cards.ReportRelay(ticket, report)
	notification := "internal_only"
	if relayed {
		notification = "orchestrator"
	}
	out := map[string]any{"reported": true, "notification": notification}
	if f := strings.TrimSpace(args.Follow); f != "" && x.o.Pool != nil {
		if _, err := x.o.Pool.Exec(context.Background(), "INSERT INTO pa_follow (agent_id, ticket_id, issue_ref) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING",
			a.ID, ticketID, f); err != nil {
			return nil, err
		}
		out["following"] = f
	}
	if args.Plan != nil && x.o.Plans != nil {
		p, created, err := x.o.Plans.Propose(context.Background(), plans.Proposal{AgentID: a.ID, OwnerID: a.UserID, IssueID: args.Plan.IssueID,
			IssueStatus: args.Plan.IssueStatus, Conclusion: args.Plan.Conclusion, Reason: args.Plan.Reason, Actions: args.Plan.Actions}, a.Settings.Eval.Mode)
		if err != nil {
			return nil, err
		}
		out["plan"] = map[string]any{"id": p.ID, "created": created, "state": p.State}
	}
	if ticket.Attention != nil {
		out["attention"] = ticket.Attention
	}
	return out, nil
}

// Ack posts a worker's acknowledgement of a person's request as a reply to it.
func Ack(a *host.Agent, ticketID, triggerID, text string) (map[string]any, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("text is required")
	}
	reply, err := a.Engine.Reply(core.ReplyRequest{Purpose: "acknowledgement", TicketID: ticketID, TriggerID: triggerID, Text: text})
	if err != nil {
		return nil, err
	}
	status := reply.Status
	if s := a.Outbox.Receipt(reply.ID); s != "" {
		status = s
	}
	return map[string]any{"trigger_id": triggerID, "acknowledgement": status, "reused": reply.Reused}, nil
}

// Handback hands a message beyond the ticket back for a fresh assessment.
func Handback(a *host.Agent, ticketID, triggerID, reason string) (map[string]any, error) {
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("reason is required")
	}
	queued, err := a.Engine.Handback(ticketID, triggerID, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"trigger_id": triggerID, "queued": queued}, nil
}

// Watchdog limits (design §9 row 6, rule version).
const (
	RunOverdue = 2 * time.Hour
	NoRun      = 15 * time.Minute
)

// Watchdog checks an agent's open tickets: a run that has not ended RunOverdue after it started, or an Assignment that
// has had no run NoRun after it was created, is stuck. A new finding is recorded for the panel and relayed to the
// Router once; a ticket no longer stuck drops its finding.
func (x *Executor) Watchdog(a *host.Agent) []store.Stuck {
	now := x.o.Now()
	before := func(at string, limit time.Duration) bool {
		t, err := time.Parse(time.RFC3339Nano, at)
		return err == nil && now.Sub(t) >= limit
	}
	found := []store.Stuck{}
	for _, t := range a.Store.Tickets().Open() {
		latest := core.LatestAssignment(&t)
		var f *store.Stuck
		if latest != nil {
			run := a.Store.AssignmentRun(latest.ID)
			switch {
			case latest.State == "pending" && run.TaskID == "" && before(latest.CreatedAt, NoRun):
				f = &store.Stuck{TicketID: t.ID, AssignmentID: latest.ID, Kind: "no_run", Since: latest.CreatedAt}
			case latest.State == "running" && Busy(t) && run.StartedAt != "" && before(run.StartedAt, RunOverdue):
				f = &store.Stuck{TicketID: t.ID, AssignmentID: latest.ID, Kind: "run_overdue", Since: run.StartedAt}
			}
		}
		if f == nil {
			a.Store.ClearStuck(t.ID)
			continue
		}
		found = append(found, *f)
		ticket, finding := t, *f
		a.Store.Atomic(func() {
			if a.Store.MarkStuck(finding) && x.o.Relay != nil {
				x.o.Relay.Relay(a.Store, cards.Relay{ID: core.Key("watchdog", finding.TicketID, finding.AssignmentID, finding.Kind),
					Text: encode(map[string]any{"kind": "watchdog", "ticket": cards.Brief(ticket), "stuck": finding.Kind, "since": finding.Since,
						"note": "The Watchdog found this ticket stuck; the panel shows it. Ask its worker with multica ticket request-status, or tell the person, as the replies section says."})})
			}
		})
	}
	return found
}

// IssueChanged asks the Worker of each ticket following the issue for a plan when the issue comes to need the owner
// (blocked / in_review), once per status (path 1); ids are the issue's id and identifier.
func (x *Executor) IssueChanged(ctx context.Context, status string, ids ...string) {
	if (status != "blocked" && status != "in_review") || x.o.Pool == nil {
		return
	}
	rows, err := x.o.Pool.Query(ctx, `UPDATE pa_follow SET asked_status = $2 WHERE issue_ref = ANY($1) AND asked_status <> $2
		RETURNING agent_id, ticket_id, issue_ref`, ids, status)
	if err != nil {
		return
	}
	type follow struct{ agent, ticket, issue string }
	due := []follow{}
	for rows.Next() {
		var f follow
		if rows.Scan(&f.agent, &f.ticket, &f.issue) == nil {
			due = append(due, f)
		}
	}
	rows.Close()
	for _, f := range due {
		a, err := x.o.Open(ctx, f.agent)
		if err != nil {
			continue
		}
		t := a.Store.Tickets().Get(f.ticket)
		own := a.Store.TicketIssue(f.ticket)
		if t == nil || !core.LiveTicket(*t) || own == "" {
			continue
		}
		text := "你转派出去的 " + f.issue + " 进入 " + status + "，需要本人处理。读它的上下游、关联 MR、历史评论和 clarify 块，出方案：\n\n" +
			"`\"$MULTICA_CLI_PATH\" ticket report --kind blocked --summary \"<结论>\" --human-action \"<要本人决定的一句话>\" --plan '{\"issue_id\":\"" + f.issue +
			"\",\"issue_status\":\"" + status + "\",\"conclusion\":\"...\",\"reason\":\"...\",\"actions\":[...]}'`\n\n只出方案，不执行任何动作。"
		_, _ = x.o.Runs.Run(ctx, a.Agent, own, text)
	}
}
