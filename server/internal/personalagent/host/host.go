// Package host connects the Source kinds to each person's personal agent (SA src/app/source-context.ts): it builds
// every enabled agent's Registry and Engine and hands each Source event to the agents it concerns. A Source
// receives a message only when its scope listens to it and it is addressed to the owner, observed under an "all"
// row, or on a Thread a Ticket follows (design §2); anything else is never stored.
package host

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multicainbox"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/panel"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
)

// Ports are the systems the Source kinds read and write.
type Ports struct {
	Multica multica.Ports
}

// Options are the parts later stages supply; nil ones are no-ops.
type Options struct {
	// Assessor and Executor return the ports of one agent's Engine, on the Store its commands write with.
	Assessor func(agent store.Agent, s *store.Store) core.Assessor
	Executor func(agent store.Agent, s *store.Store) core.Executor
	// PrepareTicket adjusts a ticket an accept creates (its Execution).
	PrepareTicket func(ticket core.Ticket, outcome core.Outcome, settings policy.Settings) core.Ticket
	// Received is told of each agent that stored a Trigger, so its pass runs now.
	Received func(agentID string)
	// OwnerName is the owner's display name.
	OwnerName func(ctx context.Context, userID string) string
	// PersonalRouter returns the owner's copy of the platform's Router template, making it when they have none, or ""
	// when there is no template; it fills an agent's unset Router.
	PersonalRouter func(ctx context.Context, workspaceID, userID string) string
	// TemplateRules returns the Router template's rule set and its version, ok false when it has none; an agent's
	// rules follow it.
	TemplateRules func(ctx context.Context) (version int, rules []policy.Rule, ok bool)
}

// Host hands Source events to the enabled personal agents.
type Host struct {
	pool    *pgxpool.Pool
	ports   Ports
	options Options
}

// New returns a Host on the personal agent tables in pool.
func New(pool *pgxpool.Pool, ports Ports, options Options) *Host {
	return &Host{pool: pool, ports: ports, options: options}
}

// Agent is one enabled personal agent, opened for one event.
type Agent struct {
	store.Agent
	Store    *store.Store
	Settings policy.Settings
	Registry *sources.Registry
	Engine   *core.Engine
	Outbox   *store.Outbox
	received func(agentID string)
}

// Kinds returns the Source kinds an agent of owner has.
func (h *Host) Kinds(ownerName string) []sources.Kind {
	return []sources.Kind{multica.Kind(h.ports.Multica), multicainbox.Kind(h.ports.Multica), panel.Kind()}
}

// Pool is the pool the personal agent tables are in.
func (h *Host) Pool() *pgxpool.Pool { return h.pool }

// Open opens one personal agent, enabled or not.
func (h *Host) Open(ctx context.Context, agentID string) (*Agent, error) {
	row, err := store.AgentByID(ctx, h.pool, agentID)
	if err != nil {
		return nil, err
	}
	return h.open(ctx, row)
}

// Agents opens the enabled personal agents, those of one workspace when workspaceID is set.
func (h *Host) Agents(ctx context.Context, workspaceID string) ([]*Agent, error) {
	rows, err := store.EnabledAgents(ctx, h.pool, workspaceID)
	if err != nil {
		return nil, err
	}
	out := make([]*Agent, 0, len(rows))
	for _, row := range rows {
		a, err := h.open(ctx, row)
		if err != nil {
			return nil, fmt.Errorf("personal agent %s: %w", row.ID, err)
		}
		out = append(out, a)
	}
	return out, nil
}

func (h *Host) open(ctx context.Context, row store.Agent) (*Agent, error) {
	s := store.New(ctx, h.pool, row.ID)
	settings, err := s.Settings()
	if err != nil {
		return nil, err
	}
	// Only the owner writes annotations, so the owner is always on the panel's list.
	part := settings.Policy.Sources[panel.Source]
	part.Allowed = []policy.Allowed{{ID: row.UserID}}
	settings.Policy.Sources[panel.Source] = part
	name := ""
	if h.options.OwnerName != nil {
		name = h.options.OwnerName(ctx, row.UserID)
	}
	registry := sources.NewRegistry(func() policy.Settings { return settings }, nil)
	for _, kind := range h.Kinds(name) {
		if _, err := registry.Register(kind); err != nil {
			return nil, err
		}
	}
	options := core.EngineOptions{
		Repo:     s,
		Policy:   func() core.Policy { return policy.SettingsPolicy(settings, registry) },
		Scope:    registry,
		Assessor: noop{},
		Executor: noop{},
	}
	outbox := store.NewOutbox(s, func() bool { return settings.Eval.Mode == "auto" })
	options.Outbox = outbox
	if h.options.Assessor != nil {
		options.Assessor = h.options.Assessor(row, s)
	}
	if h.options.Executor != nil {
		options.Executor = h.options.Executor(row, s)
	}
	if prepare := h.options.PrepareTicket; prepare != nil {
		options.PrepareTicket = func(t core.Ticket, o core.Outcome) core.Ticket { return prepare(t, o, settings) }
	}
	return &Agent{Agent: row, Store: s, Settings: settings, Registry: registry, Engine: core.NewEngine(options), Outbox: outbox, received: h.options.Received}, nil
}

// Receive hands a Trigger to an agent when its Source should keep it; it reports whether the agent took it (stored
// now or before).
func Receive(a *Agent, t *core.Trigger) (bool, error) {
	if t == nil || !Admits(a, *t) {
		return false, nil
	}
	result, err := a.Engine.Receive(*t)
	if err != nil {
		return false, err
	}
	if result == "stored" && a.received != nil {
		a.received(a.ID)
	}
	return result == "stored" || result == "duplicate", nil
}

// Admits reports whether an agent's Source keeps a Trigger: its instance is enabled and listens, and the Trigger
// is addressed, observed, or on a followed Thread.
func Admits(a *Agent, t core.Trigger) bool {
	inst := a.Registry.Instance(t.Source)
	if inst == nil || !inst.Enabled {
		return false
	}
	scope := a.Registry.Scope(t.Source, t.Meta)
	if !scope.Listened {
		return false
	}
	if t.Addressed != "" || scope.Observed || a.Store.Triggers().Get(t.ID) != nil {
		return true
	}
	for _, ref := range t.Threads {
		if thread := a.Store.Threads().Get(core.ThreadKey(ref)); thread != nil && len(thread.Subscriptions) > 0 {
			return true
		}
	}
	return false
}

// Comment hands a Multica comment to the workspace's agents whose multica Source keeps it.
func (h *Host) Comment(ctx context.Context, c multica.Comment) (int, error) {
	agents, err := h.Agents(ctx, c.WorkspaceID)
	if err != nil {
		return 0, err
	}
	taken := 0
	for _, a := range agents {
		// The Router's comments and a worker's on its ticket's issue are their work, not messages to the agent (no loop).
		if c.AuthorType == "agent" && c.AuthorID != "" && (c.AuthorID == a.RouterAgentID || a.Store.TicketOfIssue(c.IssueID) != "") {
			continue
		}
		ok, err := Receive(a, multica.Trigger(c, a.UserID))
		if err != nil {
			return taken, err
		}
		if ok {
			taken++
		}
	}
	return taken, nil
}

// Inbox hands a Multica inbox item to its recipient's agent when its multica-inbox Source keeps it. An item raised for
// a comment the agent's multica Source keeps is left to that Source, so the comment is one Trigger.
func (h *Host) Inbox(ctx context.Context, i multicainbox.Item) (int, error) {
	agents, err := h.Agents(ctx, i.WorkspaceID)
	if err != nil {
		return 0, err
	}
	taken := 0
	for _, a := range agents {
		if a.UserID != i.RecipientID {
			continue
		}
		// The Router's and a worker's actions are the agent's own work (no loop); so is a system notification on a
		// ticket's own issue, the platform relaying a Worker's @ of the owner.
		onTicket := i.IssueID != "" && a.Store.TicketOfIssue(i.IssueID) != ""
		if (i.ActorType == "agent" && i.ActorID != "" && (i.ActorID == a.RouterAgentID || onTicket)) || (i.ActorType == "system" && onTicket) {
			continue
		}
		if c, ok := i.Comment(); ok {
			if t := multica.Trigger(c, a.UserID); t != nil && Admits(a, *t) {
				continue
			}
		}
		ok, err := Receive(a, multicainbox.Trigger(i, a.UserID))
		if err != nil {
			return taken, err
		}
		if ok {
			taken++
		}
	}
	return taken, nil
}

// Annotate hands an annotation its owner wrote to the owner's agent.
func Annotate(a *Agent, an panel.Annotation) (bool, error) {
	an.Owner = a.UserID
	t := panel.Trigger(an)
	return Receive(a, &t)
}

type noop struct{}

func (noop) Request(core.Candidate)                    {}
func (noop) Release(string)                            {}
func (noop) Start(core.Ticket, core.Assignment)        {}
func (noop) Deliver(core.Ticket, core.Assignment)      {}
func (noop) Stop(core.Ticket, core.Assignment, string) {}
