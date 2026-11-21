package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNoAgent is returned when no personal agent matches.
var ErrNoAgent = errors.New("personal agent not found")

// AgentByID returns a personal agent row, enabled or not.
func AgentByID(ctx context.Context, pool *pgxpool.Pool, id string) (Agent, error) {
	return agentWhere(ctx, pool, "id::text = $1", id)
}

// AgentByRouterSession returns the personal agent whose Router is the chat session.
func AgentByRouterSession(ctx context.Context, pool *pgxpool.Pool, sessionID string) (Agent, error) {
	return agentWhere(ctx, pool, "router_session_id::text = $1", sessionID)
}

func agentWhere(ctx context.Context, pool *pgxpool.Pool, where string, arg string) (Agent, error) {
	var a Agent
	err := pool.QueryRow(ctx, "SELECT "+agentColumns+" FROM personal_agent WHERE "+where, arg).Scan(a.fields()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNoAgent
	}
	return a, err
}

// SetRouterSession records the Router's chat session.
func (s *Store) SetRouterSession(sessionID string) {
	_, err := s.q().Exec(s.ctx, "UPDATE personal_agent SET router_session_id = NULLIF($2, '')::uuid, updated_at = now() WHERE id = $1", s.agentID, sessionID)
	check(err)
}

// SetTicketIssue records the Multica issue a ticket runs on.
func (s *Store) SetTicketIssue(ticketID, issueID string) {
	_, err := s.q().Exec(s.ctx, "UPDATE pa_ticket SET issue_id = $3::uuid WHERE agent_id = $1 AND id = $2", s.agentID, ticketID, issueID)
	check(err)
}

// TicketIssue is the Multica issue a ticket runs on, "" before its first launch.
func (s *Store) TicketIssue(ticketID string) string {
	var id string
	err := s.q().QueryRow(s.ctx, "SELECT COALESCE(issue_id::text, '') FROM pa_ticket WHERE agent_id = $1 AND id = $2", s.agentID, ticketID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	check(err)
	return id
}

// TicketOfIssue is the ticket that runs on a Multica issue, "" for none.
func (s *Store) TicketOfIssue(issueID string) string {
	var id string
	err := s.q().QueryRow(s.ctx, "SELECT id FROM pa_ticket WHERE agent_id = $1 AND issue_id::text = $2", s.agentID, issueID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	check(err)
	return id
}

// Run is the Multica run an Assignment last started.
type Run struct {
	AgentID, TicketID, AssignmentID, TaskID, StartedAt string
	// Squad marks a squad member's run on the issue of a ticket dispatched to a squad.
	Squad bool
}

// TicketOfIssueAny finds the ticket, of any personal agent, that runs on a Multica issue.
func TicketOfIssueAny(ctx context.Context, pool *pgxpool.Pool, issueID string) (agentID, ticketID string, ok bool, err error) {
	err = pool.QueryRow(ctx, "SELECT agent_id::text, id FROM pa_ticket WHERE issue_id::text = $1 LIMIT 1", issueID).Scan(&agentID, &ticketID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	return agentID, ticketID, err == nil, err
}

// SetAssignmentRun records the run an Assignment started at startedAt.
func (s *Store) SetAssignmentRun(assignmentID, taskID, startedAt string) {
	_, err := s.q().Exec(s.ctx, "UPDATE pa_assignment SET task_id = $3::uuid, task_started_at = $4 WHERE agent_id = $1 AND id = $2", s.agentID, assignmentID, taskID, startedAt)
	check(err)
}

// AssignmentRun is the run an Assignment last started; TaskID is "" when it started none.
func (s *Store) AssignmentRun(assignmentID string) Run {
	r := Run{AgentID: s.agentID, AssignmentID: assignmentID}
	err := s.q().QueryRow(s.ctx, "SELECT ticket_id, COALESCE(task_id::text, ''), COALESCE(task_started_at, '') FROM pa_assignment WHERE agent_id = $1 AND id = $2",
		s.agentID, assignmentID).Scan(&r.TicketID, &r.TaskID, &r.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r
	}
	check(err)
	return r
}

// RunOfTask finds the Assignment whose latest run is a Multica task, of any personal agent.
func RunOfTask(ctx context.Context, pool *pgxpool.Pool, taskID string) (Run, bool, error) {
	r := Run{TaskID: taskID}
	err := pool.QueryRow(ctx, "SELECT agent_id::text, ticket_id, id, COALESCE(task_started_at, '') FROM pa_assignment WHERE task_id::text = $1", taskID).
		Scan(&r.AgentID, &r.TicketID, &r.AssignmentID, &r.StartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	return r, err == nil, err
}

// Stuck is a Watchdog finding.
type Stuck struct {
	TicketID     string `json:"ticketId"`
	AssignmentID string `json:"assignmentId"`
	Kind         string `json:"kind"`
	Since        string `json:"since"`
}

// MarkStuck records a finding; it reports false when the same finding was recorded before.
func (s *Store) MarkStuck(f Stuck) bool {
	tag, err := s.q().Exec(s.ctx, `INSERT INTO pa_watchdog (agent_id, ticket_id, assignment_id, kind, since) VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (agent_id, ticket_id) DO UPDATE SET assignment_id = EXCLUDED.assignment_id, kind = EXCLUDED.kind, since = EXCLUDED.since
		WHERE pa_watchdog.assignment_id <> EXCLUDED.assignment_id OR pa_watchdog.kind <> EXCLUDED.kind`, s.agentID, f.TicketID, f.AssignmentID, f.Kind, f.Since)
	check(err)
	return tag.RowsAffected() == 1
}

// ClearStuck drops a ticket's finding.
func (s *Store) ClearStuck(ticketID string) {
	_, err := s.q().Exec(s.ctx, "DELETE FROM pa_watchdog WHERE agent_id = $1 AND ticket_id = $2", s.agentID, ticketID)
	check(err)
}

// StuckTickets lists the findings, oldest first.
func (s *Store) StuckTickets() []Stuck {
	rows, err := s.q().Query(s.ctx, "SELECT ticket_id, assignment_id, kind, since FROM pa_watchdog WHERE agent_id = $1 ORDER BY since, ticket_id", s.agentID)
	check(err)
	defer rows.Close()
	out := []Stuck{}
	for rows.Next() {
		var f Stuck
		check(rows.Scan(&f.TicketID, &f.AssignmentID, &f.Kind, &f.Since))
		out = append(out, f)
	}
	check(rows.Err())
	return out
}

// AgentPaused reports whether a personal agent's processing is paused; an unknown agent is not.
func AgentPaused(ctx context.Context, pool *pgxpool.Pool, id string) bool {
	var paused bool
	_ = pool.QueryRow(ctx, "SELECT paused FROM personal_agent WHERE id::text = $1", id).Scan(&paused)
	return paused
}
