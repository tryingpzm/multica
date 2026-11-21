// Package runtime wires one server's personal agents together (SA src/app/runtime.ts and passes.ts): the Host that
// hands Source events to the agents, the durable effect queue, the Router and the Executor, and the passes that start
// due Evals, send the Outbox and run the Watchdog.
package runtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/effects"
	"github.com/multica-ai/multica/server/internal/personalagent/executor"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/plans"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/prompts"
	"github.com/multica-ai/multica/server/internal/personalagent/router"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
)

// Options are the systems the personal agents use.
type Options struct {
	Pool      *pgxpool.Pool
	Ports     host.Ports
	Runs      executor.Runs
	Session   router.Session
	OwnerName func(ctx context.Context, userID string) string
	// PersonalRouter is the owner's copy of the Router template (host.Options.PersonalRouter).
	PersonalRouter func(ctx context.Context, workspaceID, userID string) string
	// TemplateRules is the Router template's rule set (host.Options.TemplateRules).
	TemplateRules func(ctx context.Context) (version int, rules []policy.Rule, ok bool)
	// DefaultWorker is the agent tickets no rule names an agent for go to (executor.Options.DefaultWorker).
	DefaultWorker func(ctx context.Context, agent store.Agent) string
	// Actor carries out plan actions as the owner; nil keeps plans without executing them.
	Actor plans.Actor
	// CanInvoke is executor.Options.CanInvoke.
	CanInvoke func(ctx context.Context, agent store.Agent, target string) bool
	// AgentName names a Multica agent in the Router's Policy section.
	AgentName func(ctx context.Context, id string) string
	Now       func() time.Time
}

// Runtime is the personal agents of one server.
type Runtime struct {
	Host     *host.Host
	Effects  *effects.Effects
	Router   *router.Router
	Executor *executor.Executor
	Plans    *plans.Plans
	pool     *pgxpool.Pool
	mu       sync.Mutex
	passing  map[string]bool
}

// New wires a Runtime; Start runs it.
func New(o Options) *Runtime {
	rt := &Runtime{pool: o.Pool, passing: map[string]bool{}}
	var nowMs func() int64
	if o.Now != nil {
		nowMs = func() int64 { return o.Now().UnixMilli() }
	}
	rt.Effects = effects.New(o.Pool, nowMs)
	open := func(ctx context.Context, id string) (*host.Agent, error) { return rt.Host.Open(ctx, id) }
	paused := func(id string) bool { return store.AgentPaused(context.Background(), o.Pool, id) }
	rt.Plans = plans.New(o.Pool, o.Actor)
	rt.Router = router.New(router.Options{Effects: rt.Effects, Session: o.Session, Open: open, Paused: paused})
	rt.Executor = executor.New(executor.Options{Pool: o.Pool, Effects: rt.Effects, Runs: o.Runs, Open: open, Relay: rt.Router, Paused: paused, Now: o.Now, DefaultWorker: o.DefaultWorker, CanInvoke: o.CanInvoke, Plans: rt.Plans})
	rt.Host = host.New(o.Pool, o.Ports, host.Options{Assessor: rt.Router.For, Executor: rt.Executor.For, PrepareTicket: executor.Prepare,
		OwnerName: o.OwnerName, Received: rt.Request,
		PersonalRouter: o.PersonalRouter, TemplateRules: o.TemplateRules})
	return rt
}

// RouterPrompt is the Router's system prompt for an agent.
func RouterPrompt(a *host.Agent, agentName func(id string) string) string {
	return prompts.Compose(prompts.Defaults(), prompts.Orchestrator, prompts.Environment, prompts.PolicySection(a.Settings, agentName))
}

// Start runs the effects and the passes until ctx ends.
func (rt *Runtime) Start(ctx context.Context) {
	rt.Effects.Start()
	go rt.Effects.Run(ctx, time.Second)
	go func() {
		pass, watch := time.NewTicker(2*time.Second), time.NewTicker(time.Minute)
		defer pass.Stop()
		defer watch.Stop()
		for {
			select {
			case <-ctx.Done():
				rt.Effects.Drain()
				return
			case <-pass.C:
				rt.Pass(ctx, false)
			case <-watch.C:
				rt.Pass(ctx, true)
			}
		}
	}()
}

// Request runs one agent's pass now, unless one runs.
func (rt *Runtime) Request(agentID string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		a, err := rt.Host.Open(ctx, agentID)
		if err == nil {
			rt.pass(ctx, a, false)
		}
	}()
}

// Pass runs every enabled agent's pass; watchdog also runs the Watchdog.
func (rt *Runtime) Pass(ctx context.Context, watchdog bool) {
	agents, err := rt.Host.Agents(ctx, "")
	if err != nil {
		slog.Warn("personal agent: pass skipped", "error", err)
		return
	}
	for _, a := range agents {
		rt.pass(ctx, a, watchdog)
	}
}

// pass starts an agent's due Evals, sends its Outbox and, with watchdog, checks for stuck tickets.
func (rt *Runtime) pass(ctx context.Context, a *host.Agent, watchdog bool) {
	rt.mu.Lock()
	if rt.passing[a.ID] {
		rt.mu.Unlock()
		return
	}
	rt.passing[a.ID] = true
	rt.mu.Unlock()
	defer func() {
		rt.mu.Lock()
		delete(rt.passing, a.ID)
		rt.mu.Unlock()
		if r := recover(); r != nil {
			slog.Warn("personal agent: pass failed", "agent", a.ID, "error", r)
		}
	}()
	if err := rt.Router.Received(ctx, a); err != nil {
		slog.Warn("personal agent: candidate receipt not read", "agent", a.ID, "error", err)
	}
	if err := a.Engine.EvaluateDue(); err != nil {
		slog.Warn("personal agent: evaluation pass failed", "agent", a.ID, "error", err)
	}
	if err := a.Outbox.Deliver(a.Settings.Paused, func(m core.OutboundMessage) (string, error) {
		return a.Registry.Send(sources.WithSender(ctx, a.RouterAgentID), m)
	}); err != nil {
		slog.Warn("personal agent: outbox not sent", "agent", a.ID, "error", err)
	}
	if watchdog {
		rt.Executor.Watchdog(a)
	}
}
