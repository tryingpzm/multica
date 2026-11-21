// Package plans is 「需要我处理」: for an issue that needs its owner, the owner's executor proposes a plan — a
// conclusion, the reason and actions — and the owner executes it action by action as themselves, edits it first, or
// rejects it with a reason. One plan per issue and issue status; it goes stale when the issue moves on. Under auto the
// safe actions run on their own; merging an MR, releasing / approving / tagging and answering a clarify for the owner
// always wait for the owner, and merging also needs their confirmation.
package plans

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Action is one step of a plan: comment | clarify_answer | channel_reply | status | merge_mr | release.
type Action struct {
	Type   string `json:"type"`
	Target string `json:"target"` // issue id, clarify comment id, channel thread, MR ref
	Body   string `json:"body,omitempty"`
}

// Dangerous actions never run on their own.
func Dangerous(t string) bool {
	return t == "merge_mr" || t == "clarify_answer" || t == "release" || t == "approve" || t == "tag"
}

// OnBehalf ends every text the platform posts for the owner.
const OnBehalf = "\n\n（由个人助理代执行）"

// ErrConfirm is returned for a merge the owner has not confirmed.
var ErrConfirm = errors.New("merging an MR needs the owner's confirmation")

// Actor carries out an action as the owner (the system boundary: Multica, channels).
type Actor interface {
	Do(ctx context.Context, p Plan, a Action) (string, error)
}

type Proposal struct {
	AgentID, OwnerID, IssueID, IssueStatus string
	Conclusion, Reason                     string
	Actions                                []Action
}

type Plan struct {
	Proposal
	ID           string   `json:"id"`
	State        string   `json:"state"` // pending | executed | rejected | stale
	RejectReason string   `json:"reject_reason"`
	Results      []string `json:"results"`
}

type Plans struct {
	pool  *pgxpool.Pool
	actor Actor
}

func New(pool *pgxpool.Pool, actor Actor) *Plans { return &Plans{pool: pool, actor: actor} }

const cols = "id, agent_id, owner_id, issue_id, issue_status, conclusion, reason, actions, state, reject_reason, results"

func scan(row pgx.Row) (Plan, error) {
	var p Plan
	var actions, results []byte
	err := row.Scan(&p.ID, &p.AgentID, &p.OwnerID, &p.IssueID, &p.IssueStatus, &p.Conclusion, &p.Reason, &actions, &p.State, &p.RejectReason, &results)
	_ = json.Unmarshal(actions, &p.Actions)
	_ = json.Unmarshal(results, &p.Results)
	return p, err
}

// Propose records the plan unless the issue already has one for its status; under auto its safe actions run.
func (s *Plans) Propose(ctx context.Context, in Proposal, mode string) (Plan, bool, error) {
	actions, _ := json.Marshal(in.Actions)
	p, err := scan(s.pool.QueryRow(ctx, `INSERT INTO pa_plan (id, agent_id, owner_id, issue_id, issue_status, conclusion, reason, actions)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (agent_id, issue_id, issue_status) DO NOTHING RETURNING `+cols,
		uuid.NewString(), in.AgentID, in.OwnerID, in.IssueID, in.IssueStatus, in.Conclusion, in.Reason, actions))
	if errors.Is(err, pgx.ErrNoRows) {
		p, err = scan(s.pool.QueryRow(ctx, "SELECT "+cols+" FROM pa_plan WHERE agent_id=$1 AND issue_id=$2 AND issue_status=$3", in.AgentID, in.IssueID, in.IssueStatus))
		return p, false, err
	}
	if err != nil {
		return p, false, err
	}
	if mode == "auto" && s.actor != nil {
		safe, rest := []Action{}, []Action{}
		for _, a := range p.Actions {
			if Dangerous(a.Type) {
				rest = append(rest, a)
			} else {
				safe = append(safe, a)
			}
		}
		results, err := s.run(ctx, p, safe)
		if err != nil {
			return p, true, err
		}
		state := "pending"
		if len(rest) == 0 {
			state = "executed"
		}
		p, err = s.save(ctx, p.ID, state, rest, results)
		return p, true, err
	}
	return p, true, nil
}

func (s *Plans) run(ctx context.Context, p Plan, actions []Action) ([]string, error) {
	out := []string{}
	for _, a := range actions {
		if a.Type == "comment" || a.Type == "channel_reply" || a.Type == "clarify_answer" {
			a.Body += OnBehalf
		}
		r, err := s.actor.Do(ctx, p, a)
		if err != nil {
			return out, fmt.Errorf("%s %s: %w", a.Type, a.Target, err)
		}
		out = append(out, a.Type+": "+r)
	}
	return out, nil
}

func (s *Plans) save(ctx context.Context, id, state string, actions []Action, results []string) (Plan, error) {
	a, _ := json.Marshal(actions)
	r, _ := json.Marshal(results)
	return scan(s.pool.QueryRow(ctx, "UPDATE pa_plan SET state=$2, actions=$3, results=results || $4::jsonb, updated_at=now() WHERE id=$1 RETURNING "+cols, id, state, a, r))
}

func (s *Plans) Get(ctx context.Context, id string) Plan {
	p, _ := scan(s.pool.QueryRow(ctx, "SELECT "+cols+" FROM pa_plan WHERE id=$1", id))
	return p
}

// Pending is the agent's plans awaiting the owner, newest first (the panel's 「待我决策」).
func (s *Plans) Pending(ctx context.Context, agentID string) []Plan {
	rows, err := s.pool.Query(ctx, "SELECT "+cols+" FROM pa_plan WHERE agent_id=$1 AND state='pending' ORDER BY created_at DESC", agentID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		if p, err := scan(rows); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// IssueChanged makes the issue's pending plans for any other status stale; ids are the issue's id and identifier.
func (s *Plans) IssueChanged(ctx context.Context, status string, ids ...string) {
	_, _ = s.pool.Exec(ctx, "UPDATE pa_plan SET state='stale', updated_at=now() WHERE issue_id = ANY($1) AND issue_status<>$2 AND state='pending'", ids, status)
}

// Execute runs the plan's actions, or the owner's edited ones, as the owner; a merge needs confirmed.
func (s *Plans) Execute(ctx context.Context, id string, edited []Action, confirmed bool) (Plan, error) {
	p := s.Get(ctx, id)
	if p.ID == "" || p.State != "pending" {
		return p, fmt.Errorf("plan %s is not pending", id)
	}
	actions := p.Actions
	if edited != nil {
		actions = edited
	}
	for _, a := range actions {
		if a.Type == "merge_mr" && !confirmed {
			return p, ErrConfirm
		}
	}
	if s.actor == nil {
		return p, errors.New("plans cannot be executed here")
	}
	results, err := s.run(ctx, p, actions)
	if err != nil {
		_, _ = s.save(ctx, id, "pending", actions[len(results):], results)
		return s.Get(ctx, id), err
	}
	return s.save(ctx, id, "executed", actions, results)
}

// Reject records the owner's reason and closes the plan.
func (s *Plans) Reject(ctx context.Context, id, reason string) error {
	tag, err := s.pool.Exec(ctx, "UPDATE pa_plan SET state='rejected', reject_reason=$2, updated_at=now() WHERE id=$1 AND state='pending'", id, reason)
	if err == nil && tag.RowsAffected() == 0 {
		err = fmt.Errorf("plan %s is not pending", id)
	}
	return err
}
