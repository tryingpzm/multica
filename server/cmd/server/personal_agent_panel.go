package main

// personal-agent panel API: the routes of the personal agent panel (SA src/app/http.ts) under
// /api/personal-agent/panel/, for the signed-in member's own personal agent in the request's workspace. The first read
// creates the agent, off and in observe mode.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/multica-ai/multica/server/internal/handler"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/plans"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/panel"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/personalagent/views"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type panelError struct {
	status int
	body   map[string]any
}

func (e *panelError) Error() string { return fmt.Sprint(e.body["error"]) }

func refuse(status int, message string) error {
	return &panelError{status: status, body: map[string]any{"error": message}}
}

// panelCall runs work, turning a panic of the store or a refused Engine command into its HTTP refusal.
func panelCall(work func() (any, error)) (out any, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("%v", r)
			}
		}
	}()
	return work()
}

func writePanel(w http.ResponseWriter, out any, err error) {
	var pe *panelError
	var ue *core.UserError
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, out)
	case errors.As(err, &pe):
		writeJSON(w, pe.status, pe.body)
	case errors.As(err, &ue):
		status := http.StatusBadRequest
		if ue.Missing {
			status = http.StatusNotFound
		}
		values := ue.Values
		if values == nil {
			values = map[string]string{}
		}
		writeJSON(w, status, map[string]any{"error": ue.Error(), "code": ue.Code, "values": values})
	case errors.Is(err, store.ErrSettingsChanged):
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}
}

// panelParam is a route parameter as the client wrote it before encodeURIComponent: chi matches on the escaped
// path when the request has one, so a Thread key's "/" and "+" (base64 ids) arrive as "%2F" and "%2B".
func panelParam(r *http.Request, name string) string {
	v := chi.URLParam(r, name)
	if r.URL.RawPath == "" {
		return v
	}
	if u, err := url.PathUnescape(v); err == nil {
		return u
	}
	return v
}

// personalAgentPanel serves the panel's routes; rt is the personal agent runtime.
func personalAgentPanel(h *handler.Handler) http.Handler {
	r := chi.NewRouter()
	type route func(ctx context.Context, a *host.Agent, r *http.Request) (any, error)
	serve := func(fn route) http.HandlerFunc {
		return func(w http.ResponseWriter, req *http.Request) {
			rt := personalAgentRuntime
			userID, workspaceID := req.Header.Get("X-User-ID"), middleware.WorkspaceIDFromContext(req.Context())
			if rt == nil || userID == "" || workspaceID == "" || req.Header.Get("X-Actor-Source") == "task_token" {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "The personal agent panel is for its signed-in owner"})
				return
			}
			ctx, cancel := context.WithTimeout(req.Context(), 30*time.Second)
			defer cancel()
			out, err := panelCall(func() (any, error) {
				row := store.CreateAgent(store.New(ctx, rt.Host.Pool(), ""), workspaceID, userID)
				a, err := rt.Host.Open(ctx, row.ID)
				if err != nil {
					return nil, err
				}
				return fn(ctx, a, req)
			})
			writePanel(w, out, err)
		}
	}
	view := func(a *host.Agent) *views.View {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return views.New(a, sources.ChannelLinks{AppURL: strings.TrimRight(os.Getenv("MULTICA_APP_URL"), "/")}, time.Now()).
			WithDefaultWorker(personalAgentDefaultWorker(h)(ctx, a.Agent) != ""). // personal-agent.
			WithNames(personalAgentNames(h, a.WorkspaceID))                       // personal-agent.
	}
	request := func(a *host.Agent) { personalAgentRuntime.Request(a.ID) }
	number := func(r *http.Request, name string, fallback, max int) int {
		n, err := strconv.Atoi(r.URL.Query().Get(name))
		if err != nil || n < 0 || (n == 0 && name == "limit") {
			return fallback
		}
		if max > 0 && n > max {
			return max
		}
		return n
	}
	body := func(r *http.Request, v any) error {
		if err := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 256*1024)).Decode(v); err != nil {
			return refuse(http.StatusBadRequest, "The request body is not JSON")
		}
		return nil
	}

	r.Get("/state", serve(func(_ context.Context, a *host.Agent, _ *http.Request) (any, error) { return view(a).State(), nil }))
	r.Get("/overview", serve(func(_ context.Context, a *host.Agent, _ *http.Request) (any, error) { return view(a).Overview(), nil }))
	r.Get("/search", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		return view(a).Search(r.URL.Query().Get("q")), nil
	}))
	r.Get("/sources", serve(func(_ context.Context, a *host.Agent, _ *http.Request) (any, error) { return view(a).Sources(), nil }))
	r.Get("/settings", serve(func(ctx context.Context, a *host.Agent, _ *http.Request) (any, error) {
		// Reading the settings re-reads the owner's binding, name and channels, so a binding made after the agent
		// was turned on fills itself in.
		if err := personalAgentRuntime.Host.Provision(ctx, a); err != nil {
			return nil, err
		}
		a, err := personalAgentRuntime.Host.Open(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		return view(a).Settings(a.Store.Revision()), nil
	}))
	r.Get("/agents", serve(func(ctx context.Context, a *host.Agent, _ *http.Request) (any, error) {
		agents, err := h.Queries.ListAgents(ctx, parseUUID(a.WorkspaceID))
		if err != nil {
			return nil, err
		}
		// personal-agent each agent with the name of its runtime, "" for none.
		runtimes, err := h.Queries.ListAgentRuntimes(ctx, parseUUID(a.WorkspaceID))
		if err != nil {
			return nil, err
		}
		names := map[string]string{}
		for _, rt := range runtimes {
			names[uuid.UUID(rt.ID.Bytes).String()] = rt.Name
		}
		ws, err := h.Queries.GetWorkspace(ctx, parseUUID(a.WorkspaceID))
		if err != nil {
			return nil, err
		}
		executor := personalAgentDefaultWorker(h)(ctx, a.Agent) // the owner's executor.
		out := []map[string]any{}
		for _, ag := range agents {
			runtime := ""
			if ag.RuntimeID.Valid {
				runtime = names[uuid.UUID(ag.RuntimeID.Bytes).String()]
			}
			id := uuid.UUID(ag.ID.Bytes).String()
			// the owner's own template copy is personal: its page edits their own instructions.
			personal := false
			out = append(out, map[string]any{"type": "agent", "id": id, "name": ag.Name, "runtime": runtime,
				"personal": personal, "executor": id == executor, "url": "/" + ws.Slug + "/agents/" + id})
		}
		// the workspace's squads, which a rule may target too.
		squads, err := h.Queries.ListSquads(ctx, parseUUID(a.WorkspaceID))
		if err != nil {
			return nil, err
		}
		for _, sq := range squads {
			out = append(out, map[string]any{"type": "squad", "id": uuid.UUID(sq.ID.Bytes).String(), "name": sq.Name})
		}
		return out, nil
	}))
	r.Get("/threads", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		filter := r.URL.Query().Get("filter")
		switch filter {
		case "subscribed", "evaluating", "recent", "all":
		default:
			filter = "subscribed"
		}
		return view(a).Threads(filter, number(r, "offset", 0, 0), number(r, "limit", 20, 1000)), nil
	}))
	r.Get("/threads/{key}", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		q := r.URL.Query()
		out := view(a).Thread(panelParam(r, "key"), q.Get("before"), number(r, "limit", 50, 100), q.Get("around"))
		if out == nil {
			return nil, refuse(http.StatusNotFound, "Thread not found")
		}
		return out, nil
	}))
	r.Post("/threads/{key}/unsubscribe", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		key := panelParam(r, "key")
		removed, err := a.Engine.Unsubscribe(key)
		request(a)
		return map[string]any{"removed": removed, "thread": key, "context_delivery": "pending"}, err
	}))
	annotate := func(a *host.Agent, ticketID string, thread core.ThreadRef, text string) (any, error) {
		text = strings.TrimSpace(text)
		if text == "" || len([]rune(text)) > 4000 {
			return nil, refuse(http.StatusBadRequest, "An annotation needs between 1 and 4000 characters")
		}
		id := a.Store.AddAnnotation(ticketID, core.ThreadKey(thread), a.UserID, text)
		if _, err := host.Annotate(a, panel.Annotation{ID: id, Thread: thread, Text: text, At: time.Now()}); err != nil {
			return nil, err
		}
		a.Store.SetAnnotationTrigger(id, panel.TriggerID(id))
		request(a)
		return map[string]string{"triggerId": panel.TriggerID(id)}, nil
	}
	type textBody struct {
		Text string `json:"text"`
	}
	r.Post("/threads/{key}/annotate", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		thread := a.Store.Threads().Get(panelParam(r, "key"))
		if thread == nil {
			return nil, refuse(http.StatusNotFound, "Thread not found")
		}
		var in textBody
		if err := body(r, &in); err != nil {
			return nil, err
		}
		return annotate(a, "", thread.Ref, in.Text)
	}))
	r.Post("/tickets/{id}/annotate", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		t := a.Store.Tickets().Get(panelParam(r, "id"))
		if t == nil {
			return nil, refuse(http.StatusNotFound, "Ticket not found")
		}
		var in textBody
		if err := body(r, &in); err != nil {
			return nil, err
		}
		// An annotation on a ticket is written on the Thread it was accepted from.
		var thread *core.ThreadRef
		if origin := a.Store.Triggers().Get(t.Origin.TriggerID); origin != nil && len(origin.Threads) > 0 {
			thread = &origin.Threads[0]
		}
		if thread == nil {
			return nil, refuse(http.StatusBadRequest, "This ticket has no Thread to annotate")
		}
		return annotate(a, t.ID, *thread, in.Text)
	}))
	r.Post("/triggers/{id}/requeue", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		queued, err := a.Engine.Reevaluate(panelParam(r, "id"))
		request(a)
		return map[string]bool{"queued": queued}, err
	}))
	r.Get("/decisions", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		kinds := []string{}
		for _, k := range strings.Split(r.URL.Query().Get("kind"), ",") {
			if k != "" {
				kinds = append(kinds, k)
			}
		}
		return view(a).Decisions(kinds, number(r, "offset", 0, 0), number(r, "limit", 30, 200), r.URL.Query().Get("window") == "24h"), nil
	}))
	r.Post("/decisions/{id}/dismiss", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		_, err := a.Engine.Dismiss(panelParam(r, "id"))
		return map[string]bool{"dismissed": true}, err
	}))
	// personal-agent 「待我决策」: the owner's pending plans, executed as them or rejected.
	planView := func(ctx context.Context, a *host.Agent, p plans.Plan) map[string]any {
		slug := ""
		if ws, err := h.Queries.GetWorkspace(ctx, parseUUID(a.WorkspaceID)); err == nil {
			slug = ws.Slug
		}
		out := map[string]any{"id": p.ID, "issue_id": p.IssueID, "issue_key": p.IssueID, "issue_status": p.IssueStatus,
			"issue_url": "/" + slug + "/issues/" + p.IssueID, "conclusion": p.Conclusion, "reason": p.Reason, "actions": p.Actions, "state": p.State}
		// the plan is named by its issue number and shown on the tickets about that issue.
		key := p.IssueID
		if link := personalAgentNames(h, a.WorkspaceID).Issue(p.IssueID); link != nil {
			out["issue_key"], out["issue_url"], key = link.Identifier, link.URL, link.Identifier
		}
		out["ticket_ids"] = planTickets(a, p.IssueID, key)
		return out
	}
	ownPlan := func(ctx context.Context, a *host.Agent, r *http.Request) (plans.Plan, error) {
		p := personalAgentRuntime.Plans.Get(ctx, panelParam(r, "id"))
		if p.ID == "" || p.AgentID != a.ID {
			return p, refuse(http.StatusNotFound, "Plan not found")
		}
		return p, nil
	}
	r.Get("/plans", serve(func(ctx context.Context, a *host.Agent, _ *http.Request) (any, error) {
		out := []map[string]any{}
		for _, p := range personalAgentRuntime.Plans.Pending(ctx, a.ID) {
			out = append(out, planView(ctx, a, p))
		}
		return out, nil
	}))
	r.Post("/plans/{id}/execute", serve(func(ctx context.Context, a *host.Agent, r *http.Request) (any, error) {
		p, err := ownPlan(ctx, a, r)
		if err != nil {
			return nil, err
		}
		var in struct {
			Actions []plans.Action `json:"actions"`
			Confirm bool           `json:"confirm"`
		}
		if err := body(r, &in); err != nil {
			return nil, err
		}
		done, err := personalAgentRuntime.Plans.Execute(ctx, p.ID, in.Actions, in.Confirm)
		if errors.Is(err, plans.ErrConfirm) {
			return nil, refuse(http.StatusConflict, err.Error())
		}
		if err != nil {
			return nil, err
		}
		return planView(ctx, a, done), nil
	}))
	r.Post("/plans/{id}/reject", serve(func(ctx context.Context, a *host.Agent, r *http.Request) (any, error) {
		p, err := ownPlan(ctx, a, r)
		if err != nil {
			return nil, err
		}
		var in struct {
			Reason string `json:"reason"`
		}
		if err := body(r, &in); err != nil {
			return nil, err
		}
		if strings.TrimSpace(in.Reason) == "" {
			return nil, refuse(http.StatusBadRequest, "reason is required")
		}
		return map[string]bool{"rejected": true}, personalAgentRuntime.Plans.Reject(ctx, p.ID, strings.TrimSpace(in.Reason))
	}))
	r.Get("/tickets", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		state := r.URL.Query().Get("state")
		switch state {
		case "open", "person", "working", "idle", "ended":
		default:
			state = "open"
		}
		return view(a).Tickets(state, number(r, "offset", 0, 0), number(r, "limit", 20, 1000)), nil
	}))
	r.Get("/tickets/{id}", serve(func(ctx context.Context, a *host.Agent, r *http.Request) (any, error) {
		out := view(a).Ticket(panelParam(r, "id"), personalAgentNames(h, a.WorkspaceID).Issue)
		if out == nil {
			return nil, refuse(http.StatusNotFound, "Ticket not found")
		}
		return out, nil
	}))
	ticket := func(a *host.Agent, id string) error {
		if a.Store.Tickets().Get(id) == nil {
			return refuse(http.StatusNotFound, "Ticket not found")
		}
		return nil
	}
	r.Post("/tickets/{id}/retry", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		id := panelParam(r, "id")
		if err := ticket(a, id); err != nil {
			return nil, err
		}
		_, err := a.Engine.RetryTicket(id)
		request(a)
		return map[string]bool{"retried": true}, err
	}))
	r.Post("/tickets/{id}/stop", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		id := panelParam(r, "id")
		if err := ticket(a, id); err != nil {
			return nil, err
		}
		_, err := a.Engine.StopTicket(id, "")
		request(a)
		return map[string]bool{"stopped": true}, err
	}))
	r.Post("/outbox/{id}/confirm", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		// a reply edited before sending is sent as edited.
		var in struct {
			Text *string `json:"text"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Text != nil {
			if strings.TrimSpace(*in.Text) == "" {
				return nil, refuse(http.StatusBadRequest, "text is empty")
			}
			if !a.Outbox.Revise(panelParam(r, "id"), *in.Text) {
				return nil, refuse(http.StatusNotFound, "No reply waits for confirmation under this id")
			}
		}
		confirmed := a.Outbox.Confirm(panelParam(r, "id"))
		if !confirmed {
			return nil, refuse(http.StatusNotFound, "No reply waits for confirmation under this id")
		}
		request(a)
		return map[string]bool{"confirmed": true}, nil
	}))
	r.Post("/outbox/{id}/withdraw", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		a.Outbox.Withdraw(panelParam(r, "id"))
		return map[string]bool{"withdrawn": true}, nil
	}))
	r.Post("/executor/pause", serve(func(_ context.Context, a *host.Agent, r *http.Request) (any, error) {
		var in struct {
			Paused *bool `json:"paused"`
		}
		if err := body(r, &in); err != nil || in.Paused == nil {
			return nil, refuse(http.StatusBadRequest, "Invalid settings: paused")
		}
		a.Store.SetPaused(*in.Paused)
		request(a)
		return map[string]bool{"paused": *in.Paused}, nil
	}))
	r.Post("/sources/{id}/labels", serve(func(ctx context.Context, a *host.Agent, r *http.Request) (any, error) {
		added, err := personalAgentRuntime.Host.RefreshLabels(ctx, a, panelParam(r, "id"))
		if errors.Is(err, host.ErrNoLabels) {
			return nil, refuse(http.StatusBadRequest, err.Error())
		}
		return map[string]any{"added": added}, err
	}))
	r.Post("/enable", serve(func(ctx context.Context, a *host.Agent, r *http.Request) (any, error) {
		var in struct {
			Enabled *bool `json:"enabled"`
		}
		if err := body(r, &in); err != nil || in.Enabled == nil {
			return nil, refuse(http.StatusBadRequest, "Invalid settings: enabled")
		}
		// Turning it on provisions the owner's defaults.
		if *in.Enabled {
			if err := personalAgentRuntime.Host.Provision(ctx, a); err != nil {
				return nil, err
			}
		}
		a.Store.SetEnabled(*in.Enabled)
		return map[string]bool{"enabled": *in.Enabled}, nil
	}))
	r.Put("/settings/{block}", serve(func(ctx context.Context, a *host.Agent, r *http.Request) (any, error) {
		var in struct {
			Value    json.RawMessage `json:"value"`
			Revision *int            `json:"revision"`
		}
		if err := body(r, &in); err != nil || in.Revision == nil {
			return nil, refuse(http.StatusBadRequest, "Invalid settings: revision")
		}
		next, err := saveBlock(ctx, h, a, panelParam(r, "block"), in.Value, *in.Revision)
		if err != nil {
			return nil, err
		}
		return map[string]any{"revision": next, "restarting": false}, nil
	}))
	return r
}

// saveBlock validates and stores one settings block; a Policy change is told to the open tickets it bears on.
func saveBlock(ctx context.Context, h *handler.Handler, a *host.Agent, block string, raw json.RawMessage, revision int) (int, error) {
	invalid := func(field string, err error) error {
		message := "Invalid settings: " + field
		if err != nil {
			message += ": " + err.Error()
		}
		return &panelError{status: http.StatusBadRequest, body: map[string]any{"error": message,
			"issues": []map[string]string{{"path": field, "message": message}}}}
	}
	var next int
	switch {
	case block == "policy":
		var in struct {
			Rules   []policy.Rule             `json:"rules"`
			Sources map[string]map[string]any `json:"sources"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return 0, invalid("policy", err)
		}
		seen := map[string]bool{}
		for _, rule := range in.Rules {
			if rule.ID == "" || seen[rule.ID] || strings.TrimSpace(rule.Name) == "" || len([]rune(rule.Name)) > 100 ||
				strings.TrimSpace(rule.Instruction) == "" || len([]rune(rule.Instruction)) > 2000 {
				return 0, invalid("rules", nil)
			}
			seen[rule.ID] = true
			if rule.Target.ID != "" && !workspaceTarget(ctx, h, a.WorkspaceID, rule.Target) { // personal-agent.
				return 0, invalid("rules", errors.New("no agent or squad of this workspace"))
			}
		}
		delete(in.Sources, panel.Source)
		parts, err := a.Registry.ValidatePolicy(in.Sources)
		if err != nil {
			return 0, invalid("sources", err)
		}
		a.Store.Atomic(func() { next = a.Store.SavePolicy(revision, in.Rules, parts) })
		// The owner stays on every allowed list whatever the saved lists hold.
		if err := personalAgentRuntime.Host.Provision(ctx, a); err != nil {
			return next, err
		}
		next = a.Store.Revision()
	case block == "eval":
		var in struct {
			Mode           string `json:"mode"`
			TimeoutSeconds *int   `json:"timeoutSeconds"`
			Language       string `json:"language"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return 0, invalid("eval", err)
		}
		if in.Mode != "off" && in.Mode != "observe" && in.Mode != "auto" {
			return 0, invalid("mode", nil)
		}
		if in.TimeoutSeconds == nil || *in.TimeoutSeconds < 30 || *in.TimeoutSeconds > 600 {
			return 0, invalid("timeoutSeconds", nil)
		}
		if in.Language == "" {
			in.Language = a.Settings.Eval.Language
		}
		if in.Language != "zh" && in.Language != "en" {
			return 0, invalid("language", nil)
		}
		a.Store.Atomic(func() {
			next = a.Store.SaveEval(revision, in.Mode, *in.TimeoutSeconds)
			a.Store.SetLanguage(in.Language)
		})
	case block == "executor":
		var in struct {
			RouterAgentID string `json:"routerAgentId"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return 0, invalid("executor", err)
		}
		if in.RouterAgentID != "" && !workspaceAgent(ctx, h, a.WorkspaceID, in.RouterAgentID) {
			return 0, invalid("routerAgentId", errors.New("no agent of this workspace"))
		}
		a.Store.Atomic(func() { next = a.Store.SaveExecutor(revision, in.RouterAgentID) })
	case strings.HasPrefix(block, "source:"):
		id := strings.TrimPrefix(block, "source:")
		if a.Registry.Kind(id) == nil || id == panel.Source {
			return 0, refuse(http.StatusNotFound, "Source not found")
		}
		var in struct {
			Enabled bool           `json:"enabled"`
			Config  map[string]any `json:"config"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return 0, invalid(block, err)
		}
		enabled, config, err := a.Registry.Validate(id, in.Enabled, in.Config)
		if err != nil {
			return 0, invalid(block, err)
		}
		a.Store.Atomic(func() { next = a.Store.SaveSource(revision, id, enabled, config) })
	default:
		return 0, refuse(http.StatusBadRequest, "Invalid settings: block")
	}
	// Tell the open tickets how a changed Policy bears on them (SA Settings.save).
	if after, err := personalAgentRuntime.Host.Open(ctx, a.ID); err == nil {
		from, to := policy.PolicyVersion(a.Settings), policy.PolicyVersion(after.Settings)
		if from != to {
			effects := policy.PolicyEffects(a.Store.Tickets().Open(), a.Settings, after.Settings, func(s policy.Settings) core.SourceScope {
				if policy.PolicyVersion(s) == to {
					return after.Registry
				}
				return a.Registry
			})
			if err := after.Engine.PolicyChanged(from, to, effects); err != nil {
				return next, err
			}
		}
		personalAgentRuntime.Request(a.ID)
	}
	return next, nil
}

// workspaceAgent reports whether id is an agent of the workspace.
// workspaceTarget reports whether a rule's target is an agent or squad of the workspace.
func workspaceTarget(ctx context.Context, h *handler.Handler, workspaceID string, t policy.RuleTarget) bool {
	switch t.Type {
	case "agent":
		return workspaceAgent(ctx, h, workspaceID, t.ID)
	case "squad":
		_, err := h.Queries.GetSquadInWorkspace(ctx, db.GetSquadInWorkspaceParams{ID: parseUUID(t.ID), WorkspaceID: parseUUID(workspaceID)})
		return err == nil
	}
	return false
}

func workspaceAgent(ctx context.Context, h *handler.Handler, workspaceID, id string) bool {
	ag, err := h.Queries.GetAgent(ctx, parseUUID(id))
	return err == nil && uuid.UUID(ag.WorkspaceID.Bytes).String() == workspaceID
}

// personalAgentNames names the issues and executors the panel shows.
func personalAgentNames(h *handler.Handler, workspaceID string) views.Names {
	return views.Names{
		Issue: func(issueID string) *views.IssueLink {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := uuid.Parse(issueID); err != nil {
				return nil
			}
			issue, err := h.Queries.GetIssue(ctx, parseUUID(issueID))
			if err != nil {
				return nil
			}
			ws, err := h.Queries.GetWorkspace(ctx, issue.WorkspaceID)
			if err != nil {
				return nil
			}
			identifier := service.IssueIdentifier(ws.IssuePrefix, issue.Number)
			return &views.IssueLink{ID: issueID, Identifier: identifier, Title: issue.Title, URL: "/" + ws.Slug + "/issues/" + identifier}
		},
		Target: func(target core.Target) string {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := uuid.Parse(target.ID); err != nil {
				return ""
			}
			if target.Type == "squad" {
				if sq, err := h.Queries.GetSquad(ctx, parseUUID(target.ID)); err == nil {
					return sq.Name
				}
				return ""
			}
			if ag, err := h.Queries.GetAgent(ctx, parseUUID(target.ID)); err == nil {
				return ag.Name
			}
			return ""
		},
	}
}

// planTickets are the open tickets about a plan's issue: run on it, following its thread, or named after it.
func planTickets(a *host.Agent, issueID, key string) []string {
	ids := []string{}
	following := map[string]bool{}
	if th := a.Store.Threads().Get("multica:" + issueID); th != nil {
		for _, id := range th.Subscriptions {
			following[id] = true
		}
	}
	for _, t := range a.Store.Tickets().Open() {
		if following[t.ID] || a.Store.TicketIssue(t.ID) == issueID || (key != "" && strings.Contains(t.Title, key)) {
			ids = append(ids, t.ID)
		}
	}
	return ids
}
