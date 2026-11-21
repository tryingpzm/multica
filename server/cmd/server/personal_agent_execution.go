package main

// personal-agent execution: each personal agent's Router is a Multica chat session with its Router
// agent, a ticket is an issue assigned to its Worker agent, and the multica ticket commands of both reach the agent
// through POST /api/personal-agent/ticket/{action}. A run's end is the Worker's turn's end.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/executor"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/prompts"
	"github.com/multica-ai/multica/server/internal/personalagent/router"
	paruntime "github.com/multica-ai/multica/server/internal/personalagent/runtime"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

var personalAgentRuntime *paruntime.Runtime

func newPersonalAgentRuntime(pool *pgxpool.Pool, h *handler.Handler, bus *events.Bus) *paruntime.Runtime {
	if personalAgentRuntime != nil {
		return personalAgentRuntime
	}
	runs := &personalAgentRuns{h: h}
	sess := &personalAgentSession{h: h, pool: pool}
	ports := host.Ports{Multica: multica.Ports{Commenter: personalAgentCommenter{h: h}}}
	rt := paruntime.New(paruntime.Options{Pool: pool, Ports: ports, Runs: runs, Session: sess,
		PersonalRouter: personalAgentRouter(h), DefaultWorker: personalAgentDefaultWorker(h), CanInvoke: personalAgentCanInvoke(h), // personal-agent.
		Actor: personalAgentActor{h: h, workspace: func(ctx context.Context, agentID string) string { // .
			var ws string
			_ = pool.QueryRow(ctx, "SELECT workspace_id FROM personal_agent WHERE id = $1", agentID).Scan(&ws)
			return ws
		}}})
	sess.rt = rt
	rt.Start(context.Background())
	registerPersonalAgentRunEnds(bus, h, rt)
	personalAgentRuntime = rt
	return rt
}

// personalAgentRuns is the executor.Runs of Multica issues and runs.
type personalAgentRuns struct{ h *handler.Handler }

// CreateIssue assigns the ticket's issue to an agent or, through Multica's squad assignment, a squad.
func (r *personalAgentRuns) CreateIssue(ctx context.Context, agent store.Agent, target core.Target, title, description string) (string, string, error) {
	if target.Type == "squad" {
		if _, err := r.h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: parseUUID(target.ID), WorkspaceID: parseUUID(agent.WorkspaceID)}); err != nil {
			return "", "", fmt.Errorf("squad %s: %w", target.ID, err)
		}
	}
	owner := parseUUID(agent.UserID)
	res, err := r.h.IssueService.Create(ctx, service.IssueCreateParams{
		WorkspaceID: parseUUID(agent.WorkspaceID), Title: title, Description: pgtype.Text{String: description, Valid: true},
		Status: "todo", Priority: "none", AssigneeType: pgtype.Text{String: target.Type, Valid: true}, AssigneeID: parseUUID(target.ID),
		CreatorType: "member", CreatorID: owner,
	}, service.IssueCreateOpts{ActorID: agent.UserID})
	if err != nil {
		return "", "", err
	}
	task := ""
	if res.AssignedTaskID.Valid {
		task = uuid.UUID(res.AssignedTaskID.Bytes).String()
	} else if target.Type == "squad" {
		// The squad leader's run the creation queued.
		var id pgtype.UUID
		if err := r.h.DB.QueryRow(ctx, "SELECT id FROM agent_task_queue WHERE issue_id = $1 ORDER BY created_at DESC LIMIT 1", res.Issue.ID).Scan(&id); err == nil {
			task = uuid.UUID(id.Bytes).String()
		}
	}
	return uuid.UUID(res.Issue.ID.Bytes).String(), task, nil
}

func (r *personalAgentRuns) Run(ctx context.Context, agent store.Agent, issueID, text string) (string, error) {
	issue, err := r.h.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		return "", err
	}
	var squad *db.Squad
	if issue.AssigneeType.String == "squad" && issue.AssigneeID.Valid { // A squad's turn starts with its leader.
		sq, err := r.h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: issue.AssigneeID, WorkspaceID: issue.WorkspaceID})
		if err != nil {
			return "", err
		}
		squad = &sq
	}
	if issue.Status == "blocked" { // The issue a blocked report blocked is worked on again.
		if issue, err = r.setStatus(ctx, issue, "in_progress"); err != nil {
			return "", err
		}
	}
	comment, err := r.h.Queries.CreateComment(ctx, db.CreateCommentParams{IssueID: issue.ID, WorkspaceID: issue.WorkspaceID, AuthorType: "member",
		AuthorID: parseUUID(agent.UserID), Content: text, Type: "comment"})
	if err != nil {
		return "", err
	}
	var task db.AgentTaskQueue
	if squad != nil {
		task, err = r.h.TaskService.EnqueueTaskForSquadLeader(ctx, issue, squad.LeaderID, squad.ID, comment.ID, service.OriginDerived)
	} else {
		task, err = r.h.TaskService.EnqueueTaskForIssue(ctx, issue, comment.ID)
	}
	if err != nil {
		return "", err
	}
	return uuid.UUID(task.ID.Bytes).String(), nil
}

func (r *personalAgentRuns) setStatus(ctx context.Context, issue db.Issue, status string) (db.Issue, error) {
	return r.h.Queries.UpdateIssueStatus(ctx, db.UpdateIssueStatusParams{ID: issue.ID, Status: status, WorkspaceID: issue.WorkspaceID})
}

// SetStatus sets a ticket's issue status.
func (r *personalAgentRuns) SetStatus(ctx context.Context, _ store.Agent, issueID, status string) error {
	issue, err := r.h.Queries.GetIssue(ctx, parseUUID(issueID))
	if err != nil {
		return err
	}
	_, err = r.setStatus(ctx, issue, status)
	return err
}

func (r *personalAgentRuns) Cancel(ctx context.Context, taskID string) error {
	_, err := r.h.TaskService.CancelTask(ctx, parseUUID(taskID))
	return err
}

// personalAgentSession is the Router's chat session with the agent's Router agent; the first message it gets is the
// Router's prompt. A request ID is the chat submission, so a retried delivery posts nothing twice.
type personalAgentSession struct {
	h    *handler.Handler
	pool *pgxpool.Pool
	rt   *paruntime.Runtime
	// settingsOf reads the agent's current settings; nil reads them from the Runtime's Host.
	settingsOf func(ctx context.Context, agentID string) (policy.Settings, error)
}

func (s *personalAgentSession) settings(ctx context.Context, agentID string) (policy.Settings, error) {
	if s.settingsOf != nil {
		return s.settingsOf(ctx, agentID)
	}
	a, err := s.rt.Host.Open(ctx, agentID)
	if err != nil {
		return policy.Settings{}, err
	}
	return a.Settings, nil
}

var personalAgentNamespace = uuid.MustParse("5f0c1f0e-6f43-4b1f-9a59-7d3c2a1b0e11")

func (s *personalAgentSession) Deliver(ctx context.Context, agent store.Agent, requestID, text string) error {
	if agent.RouterAgentID == "" {
		return errors.New("This personal agent has no Router agent")
	}
	routerAgent, err := s.h.Queries.GetAgent(ctx, parseUUID(agent.RouterAgentID))
	if err != nil {
		return err
	}
	var session db.ChatSession
	if agent.RouterSessionID != "" {
		session, err = s.h.Queries.GetChatSession(ctx, parseUUID(agent.RouterSessionID))
	}
	if agent.RouterSessionID == "" || err != nil || session.Status != "active" {
		session, err = s.h.Queries.CreateChatSession(ctx, db.CreateChatSessionParams{WorkspaceID: parseUUID(agent.WorkspaceID), AgentID: routerAgent.ID,
			CreatorID: parseUUID(agent.UserID), Title: "个人 Agent · Router"})
		if err != nil {
			return err
		}
		store.New(ctx, s.pool, agent.ID).SetRouterSession(uuid.UUID(session.ID.Bytes).String())
		a, err := s.rt.Host.Open(ctx, agent.ID)
		if err != nil {
			return err
		}
		if err := s.send(ctx, session, routerAgent, agent, "prompt:"+uuid.UUID(session.ID.Bytes).String(), paruntime.RouterPrompt(a, func(id string) string { return personalAgentName(s.h)(ctx, id) })); err != nil {
			return err
		}
	} else if err := s.updatePolicy(ctx, session, routerAgent, agent); err != nil {
		return err
	}
	return s.send(ctx, session, routerAgent, agent, requestID, text)
}

// updatePolicy sends the Router's Session the current Policy when the latest one it was sent differs. The
// request id is the update's text after the latest Policy message, so a retry posts nothing twice while a Policy changed
// back to one sent before is sent again.
func (s *personalAgentSession) updatePolicy(ctx context.Context, session db.ChatSession, routerAgent db.Agent, agent store.Agent) error {
	var lastID, last string
	err := s.pool.QueryRow(ctx, `SELECT id::text, content FROM chat_message WHERE chat_session_id = $1 AND role = 'user' AND content LIKE '%## Policy%'
		ORDER BY created_at DESC LIMIT 1`, session.ID).Scan(&lastID, &last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	settings, err := s.settings(ctx, agent.ID)
	if err != nil {
		return err
	}
	update, ok := prompts.PolicyUpdate([]string{last}, settings, func(id string) string { return personalAgentName(s.h)(ctx, id) })
	if !ok {
		return nil
	}
	return s.send(ctx, session, routerAgent, agent, "policy:"+uuid.UUID(session.ID.Bytes).String()+":"+uuid.NewSHA1(personalAgentNamespace, []byte(lastID+"\n"+update)).String(), update)
}

func (s *personalAgentSession) send(ctx context.Context, session db.ChatSession, routerAgent db.Agent, agent store.Agent, requestID, text string) error {
	submission := uuid.NewSHA1(personalAgentNamespace, []byte(agent.ID+":"+requestID))
	owner := parseUUID(agent.UserID)
	_, err := s.h.TaskService.SendDirectChatMessageWithSubmission(ctx, session, routerAgent, owner, text, nil, "member", owner,
		pgtype.UUID{Bytes: submission, Valid: true})
	return err
}

// Received reports whether a Router run has started on the message requestID posted: a message queued behind another
// run is not yet received. A message with no run of its own, or whose run ended unstarted, counts as received.
func (s *personalAgentSession) Received(ctx context.Context, agent store.Agent, requestID string) (bool, error) {
	submission := uuid.NewSHA1(personalAgentNamespace, []byte(agent.ID+":"+requestID))
	var received bool
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(t.started_at IS NOT NULL OR t.status NOT IN ('queued', 'dispatched'), true)
		FROM chat_submission_receipt r LEFT JOIN agent_task_queue t ON t.id::text = r.response->>'task_id'
		WHERE r.submission_id = $1 LIMIT 1`, submission).Scan(&received)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return received, err
}

// registerPersonalAgentRunEnds tells the Executor of each run's end.
func registerPersonalAgentRunEnds(bus *events.Bus, h *handler.Handler, rt *paruntime.Runtime) {
	ended := func(status string) func(events.Event) {
		return func(e events.Event) {
			payload, ok := e.Payload.(map[string]any)
			if !ok {
				return
			}
			taskID, _ := payload["task_id"].(string)
			if taskID == "" {
				return
			}
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), personalAgentTimeout)
				defer cancel()
				issueID := ""
				if task, err := h.Queries.GetAgentTask(ctx, parseUUID(taskID)); err == nil && task.IssueID.Valid {
					issueID = uuid.UUID(task.IssueID.Bytes).String()
				}
				if _, ok, err := rt.Executor.RunOf(ctx, taskID, issueID); err != nil || !ok {
					return
				}
				output := ""
				if task, err := h.Queries.GetAgentTask(ctx, parseUUID(taskID)); err == nil {
					var result struct {
						Output string `json:"output"`
					}
					_ = json.Unmarshal(task.Result, &result)
					output = result.Output
					if status == executor.Failed && task.Error.Valid {
						output = task.Error.String
					}
				}
				if err := rt.Executor.RunEnded(ctx, taskID, issueID, status, output); err != nil {
					slog.Warn("personal agent: run end not recorded", "task", taskID, "error", err)
				}
			}()
		}
	}
	bus.Subscribe(protocol.EventTaskCompleted, ended(executor.Completed))
	bus.Subscribe(protocol.EventTaskFailed, ended(executor.Failed))
	bus.Subscribe(protocol.EventTaskCancelled, ended(executor.Cancelled))
	// personal-agent an issue that moves on makes the plans proposed for its earlier status stale.
	bus.Subscribe(protocol.EventIssueUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		raw, _ := json.Marshal(payload["issue"])
		var issue struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
			Status     string `json:"status"`
		}
		if json.Unmarshal(raw, &issue) != nil || issue.ID == "" || issue.Status == "" {
			return
		}
		go func() {
			rt.Plans.IssueChanged(context.Background(), issue.Status, issue.ID, issue.Identifier)
			rt.Executor.IssueChanged(context.Background(), issue.Status, issue.ID, issue.Identifier) // path 1
		}()
	})
}

// personalAgentTicket serves POST /api/personal-agent/ticket/{action}, the multica ticket commands: a Router run's
// (its task is a chat task of a Router session) or a Worker run's (its task runs a ticket's Assignment).
func personalAgentTicket(h *handler.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rt := personalAgentRuntime
		if rt == nil || r.Header.Get("X-Actor-Source") != "task_token" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "ticket commands are only available from within a personal agent's run"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		task, err := h.Queries.GetAgentTask(ctx, parseUUID(r.Header.Get("X-Task-ID")))
		if err != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "active authenticated run required"})
			return
		}
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			body = json.RawMessage("{}")
		}
		action := chi.URLParam(r, "action")
		var out any
		if task.ChatSessionID.Valid {
			agent, err := store.AgentByRouterSession(ctx, rt.Host.Pool(), uuid.UUID(task.ChatSessionID.Bytes).String())
			if err != nil {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "this chat is no personal agent's Router"})
				return
			}
			a, err := rt.Host.Open(ctx, agent.ID)
			if err == nil {
				out, err = routerCommand(ctx, rt, a, action, body)
			}
			respondTicket(w, out, err)
			return
		}
		issueID := ""
		if task.IssueID.Valid {
			issueID = uuid.UUID(task.IssueID.Bytes).String()
		}
		run, ok, err := rt.Executor.RunOf(ctx, uuid.UUID(task.ID.Bytes).String(), issueID) // any squad member's run.
		if err != nil || !ok {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "this run is no personal agent ticket's"})
			return
		}
		a, err := rt.Host.Open(ctx, run.AgentID)
		if err == nil {
			out, err = workerCommand(rt, a, run.TicketID, action, body)
		}
		respondTicket(w, out, err)
	}
}

func respondTicket(w http.ResponseWriter, out any, err error) {
	if err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type ticketArgs struct {
	TriggerID string `json:"trigger_id"`
	TicketID  string `json:"ticket_id"`
	Thread    string `json:"thread"`
	Text      string `json:"text"`
	Reason    string `json:"reason"`
}

func routerCommand(ctx context.Context, rt *paruntime.Runtime, a *host.Agent, action string, body json.RawMessage) (any, error) {
	var args ticketArgs
	_ = json.Unmarshal(body, &args)
	defer rt.Request(a.ID)
	switch action {
	case "route":
		var in struct {
			Decisions []router.RouteItem `json:"decisions"`
		}
		if err := json.Unmarshal(body, &in); err != nil || len(in.Decisions) == 0 {
			return nil, errors.New("route takes {\"decisions\": [...]}, one item per candidate")
		}
		return map[string]any{"results": router.Route(a, in.Decisions)}, nil
	case "eval":
		return rt.Router.Context(ctx, a, args.TriggerID)
	case "eval-read":
		var read router.Read
		if err := json.Unmarshal(body, &read); err != nil {
			return nil, err
		}
		return rt.Router.ReadContext(ctx, a, read)
	case "decide":
		var in struct {
			TriggerID string `json:"trigger_id"`
			policy.Assessment
		}
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, err
		}
		return rt.Router.Decide(a, in.TriggerID, in.Assessment)
	case "reply":
		var in router.ReplyArgs
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, err
		}
		return router.Reply(a, in)
	case "retry":
		t, err := a.Engine.RetryTicket(args.TicketID)
		return map[string]any{"reason": "Started a new assignment", "ticket_id": t.ID}, err
	case "request-status":
		asked, err := a.Engine.RequestStatus(args.TicketID)
		return map[string]any{"asked": asked}, err
	case "note":
		_, err := a.Engine.Note(args.TicketID, args.Text)
		return map[string]any{"reason": "Noted for the worker"}, err
	case "requeue":
		queued, err := a.Engine.Reevaluate(args.TriggerID)
		return map[string]any{"queued": queued}, err
	case "unsubscribe":
		removed, err := a.Engine.Unsubscribe(args.Thread)
		return map[string]any{"removed": removed, "thread": args.Thread}, err
	}
	return nil, errors.New("unknown Router command " + action)
}

func workerCommand(rt *paruntime.Runtime, a *host.Agent, ticketID, action string, body json.RawMessage) (any, error) {
	var args ticketArgs
	_ = json.Unmarshal(body, &args)
	defer rt.Request(a.ID)
	switch action {
	case "report":
		var in executor.ReportArgs
		if err := json.Unmarshal(body, &in); err != nil {
			return nil, err
		}
		return rt.Executor.Report(a, ticketID, in)
	case "read": // the full assignment the issue card points to.
		return rt.Executor.Read(a, ticketID)
	case "ack":
		return executor.Ack(a, ticketID, args.TriggerID, args.Text)
	case "handback":
		return executor.Handback(a, ticketID, args.TriggerID, args.Reason)
	case "unrelated":
		return map[string]any{"removed": true, "thread": args.Thread}, a.Engine.MarkUnrelated(ticketID, args.Thread, args.Reason)
	}
	return nil, errors.New("unknown Worker command " + action)
}
