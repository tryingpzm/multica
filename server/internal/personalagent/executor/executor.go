package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/personalagent/cards"
	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/effects"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/plans"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/personalagent/ticketskill"
)

// Runs is the Multica execution plane a ticket runs on: the system boundary the Executor drives.
type Runs interface {
	// CreateIssue creates a ticket's issue in the agent's workspace, assigned to the worker agent, which starts the
	// worker's run on it; it returns the issue and that run's task.
	// CreateIssue creates the ticket's issue assigned to target, an agent or a squad, and starts its run.
	CreateIssue(ctx context.Context, agent store.Agent, target core.Target, title, description string) (issueID, taskID string, err error)
	// SetStatus sets the ticket's issue status.
	SetStatus(ctx context.Context, agent store.Agent, issueID, status string) error
	// Run posts text on the issue for the Worker agent and starts its run; it returns the run's task.
	Run(ctx context.Context, agent store.Agent, issueID, text string) (taskID string, err error)
	// Cancel cancels a run; a run that already ended is no error.
	Cancel(ctx context.Context, taskID string) error
}

// Relayer queues a message for an agent's Router in s's transaction.
type Relayer interface {
	Relay(s *store.Store, relay cards.Relay)
}

// Opener opens one personal agent for a unit of work.
type Opener func(ctx context.Context, agentID string) (*host.Agent, error)

// Options configure the Executor.
type Options struct {
	Pool    *pgxpool.Pool
	Effects *effects.Effects
	Runs    Runs
	Open    Opener
	Relay   Relayer
	// DefaultWorker is the agent a ticket no rule names an agent for is assigned to, "" when none.
	DefaultWorker func(ctx context.Context, agent store.Agent) string
	// CanInvoke reports whether the owner may invoke a rule's executor; one they may not falls back to DefaultWorker
	//. nil allows every executor.
	// Plans keeps the plans reports carry; nil drops them.
	Plans     *plans.Plans
	CanInvoke func(ctx context.Context, agent store.Agent, target string) bool
	Now       func() time.Time
	Paused    func(agentID string) bool
}

// Executor runs the Assignments of every personal agent.
type Executor struct {
	o         Options
	mu        sync.Mutex
	slots     map[string]*Slots
	transient map[string]int
}

const noReport = "The run ended without a report."

// New returns an Executor and registers its launch and delivery effects.
// SetCanInvoke sets Options.CanInvoke.
func (x *Executor) SetCanInvoke(f func(ctx context.Context, agent store.Agent, target string) bool) {
	x.o.CanInvoke = f
}

func New(o Options) *Executor {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Paused == nil {
		o.Paused = func(string) bool { return false }
	}
	x := &Executor{o: o, slots: map[string]*Slots{}, transient: map[string]int{}}
	o.Effects.Handle("launch", x.launchQueued, effects.Options{Limit: func() int { return 4 }, Retry: [2]int64{5000, 300_000}})
	o.Effects.Handle("delivery", x.deliverQueued, effects.Options{Limit: func() int { return 4 }, Quiet: true})
	return x
}

func (x *Executor) now() string { return core.FormatTime(x.o.Now().UnixMilli()) }

// Busy reports whether a ticket's worker runs: its running Assignment's last run has not ended, or Inputs reached it
// since that run ended.
func Busy(t core.Ticket) bool {
	a := core.RunningAssignment(&t)
	return core.LiveTicket(t) && a != nil && (a.IdleAt == "" || a.DeliveredAt > a.IdleAt)
}

func (x *Executor) slotsOf(a *host.Agent) *Slots {
	x.mu.Lock()
	defer x.mu.Unlock()
	s := x.slots[a.ID]
	if s == nil {
		id, pool := a.ID, a.Store.Pool()
		s = NewSlots(func() []string {
			running := []string{}
			for _, t := range store.New(context.Background(), pool, id).Tickets().Open() {
				if Busy(t) {
					running = append(running, t.ID)
				}
			}
			return running
		}, func() int { return MaxWorkers })
		x.slots[a.ID] = s
	}
	return s
}

// Prepare gives a ticket an accept creates its Execution; a name it refuses refuses the accept.
func Prepare(ticket core.Ticket, outcome core.Outcome, settings policy.Settings) core.Ticket {
	execution, err := TicketExecution(ticket, outcome, settings.Policy.Rules)
	if err != nil {
		panic(err)
	}
	ticket.Execution = &execution
	return ticket
}

// For returns the core.Executor port of one agent's Engine, which queues its work on s.
func (x *Executor) For(agent store.Agent, s *store.Store) core.Executor { return port{x, s} }

type port struct {
	x *Executor
	s *store.Store
}

type launch struct {
	TicketID     string `json:"ticketId"`
	AssignmentID string `json:"assignmentId"`
}

func (p port) Start(ticket core.Ticket, a core.Assignment) {
	p.x.o.Effects.Enqueue(p.s, "launch", a.ID, launch{ticket.ID, a.ID})
}

func (p port) Deliver(ticket core.Ticket, _ core.Assignment) {
	p.x.o.Effects.Enqueue(p.s, "delivery", ticket.ID, map[string]string{"ticketId": ticket.ID})
}

// Stop cancels the run of an Assignment that ends, once the ending commits, unless the worker's last word was a ready or
// dropped report with nothing delivered since.
func (p port) Stop(ticket core.Ticket, a core.Assignment, reason string) {
	x := p.x
	x.mu.Lock()
	delete(x.transient, a.ID)
	delete(x.transient, "launch:"+a.ID)
	x.mu.Unlock()
	busy := a.DeliveredAt != "" && a.Report != nil && a.DeliveredAt >= a.Report.At
	if a.Report != nil && (a.Report.State == "ready" || a.Report.State == "dropped") && !busy {
		return
	}
	run := p.s.AssignmentRun(a.ID)
	if run.TaskID == "" {
		return
	}
	p.s.AfterCommit(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := x.o.Runs.Cancel(ctx, run.TaskID); err != nil {
				slog.Warn("personal agent: run not cancelled", "task", run.TaskID, "reason", reason, "error", err)
			}
		}()
	})
}

func (x *Executor) retries(key string, err error) bool {
	if !Transient(err) {
		return false
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	x.transient[key]++
	return x.transient[key] < TransientTries
}

func (x *Executor) launchQueued(ctx context.Context, agentID, _ string, payload json.RawMessage) error {
	var l launch
	if err := json.Unmarshal(payload, &l); err != nil {
		return err
	}
	if x.o.Paused(agentID) {
		return effects.NewLater("Processing is paused")
	}
	a, err := x.o.Open(ctx, agentID)
	if err != nil {
		return err
	}
	ticket := a.Store.Tickets().Get(l.TicketID)
	if ticket == nil || !core.LiveTicket(*ticket) {
		return nil
	}
	latest := core.LatestAssignment(ticket)
	if latest == nil || latest.ID != l.AssignmentID || latest.State != "pending" {
		return nil
	}
	slots := x.slotsOf(a)
	if err := slots.Reserve(ticket.ID, "launch"); err != nil {
		return err
	}
	defer slots.Free(ticket.ID)
	taskID, err := x.launch(ctx, a, *ticket)
	if errors.Is(err, errNoTarget) {
		// no rule of the ticket names an executor and the deployment has no default; the owner picks one.
		_, rerr := x.Reported(a, ticket.ID, core.WorkerReport{State: "blocked", Summary: NoTargetSummary})
		return rerr
	}
	if err != nil {
		if x.retries("launch:"+l.AssignmentID, err) {
			return err
		}
		x.mu.Lock()
		delete(x.transient, "launch:"+l.AssignmentID)
		x.mu.Unlock()
		_, rerr := x.Reported(a, ticket.ID, core.WorkerReport{State: "failed", Summary: "The launch failed: " + err.Error()})
		return rerr
	}
	x.mu.Lock()
	delete(x.transient, "launch:"+l.AssignmentID)
	x.mu.Unlock()
	a.Store.SetAssignmentRun(l.AssignmentID, taskID, x.now())
	return a.Engine.AssignmentStarted(ticket.ID, l.AssignmentID)
}

// NoTargetSummary is the blocked report of a ticket that has no executor.
const NoTargetSummary = "这条规则还没有指定执行方，请选择智能体或小队"

var errNoTarget = errors.New(NoTargetSummary)

func (x *Executor) launch(ctx context.Context, a *host.Agent, ticket core.Ticket) (string, error) {
	assignment := x.assignment(a, ticket)
	issueID := a.Store.TicketIssue(ticket.ID)
	if issueID != "" {
		return x.o.Runs.Run(ctx, a.Agent, issueID, assignment)
	}
	var worker core.Target
	if ticket.Execution != nil {
		worker = ticket.Execution.Target
	}
	if worker.Type == "agent" && x.o.CanInvoke != nil && !x.o.CanInvoke(ctx, a.Agent, worker.ID) {
		worker = core.Target{}
	}
	if worker.ID == "" && x.o.DefaultWorker != nil {
		if id := x.o.DefaultWorker(ctx, a.Agent); id != "" {
			worker = core.Target{Type: "agent", ID: id}
		}
	}
	if worker.ID == "" {
		return "", errNoTarget
	}
	// The run the creation starts reads the card and then the full assignment with multica ticket read.
	issueID, taskID, err := x.o.Runs.CreateIssue(ctx, a.Agent, worker, ticket.Title, x.description(a, ticket))
	if err != nil {
		return "", err
	}
	a.Store.SetTicketIssue(ticket.ID, issueID)
	// The ticket follows its own issue, so a comment there reaches its Worker instead of a new Eval (sources/multica/kind.go).
	if err := a.Engine.Subscribe(ticket.ID, multica.ThreadRef(issueID)); err != nil {
		return "", err
	}
	if taskID == "" {
		return x.o.Runs.Run(ctx, a.Agent, issueID, assignment)
	}
	return taskID, nil
}

// SourceChars bounds the source message on a ticket's issue card.
const SourceChars = 400

// description is the ticket's issue description, a short Chinese card: the scope, the source message, the
// matched rules and how to deliver. The Worker's instructions are the personal-agent-ticket skill, and the full
// assignment (rule procedures, the batch, ids) is `multica ticket read`, so no instruction text or id is put here.
func (x *Executor) description(a *host.Agent, ticket core.Ticket) string {
	cli := "`\"$MULTICA_CLI_PATH\" ticket "
	parts := []string{"个人 Agent 工单。开工前先加载 skill **" + ticketskill.Name + "**（工单规范），再运行 " + cli + "read` 读取完整派单（来源消息全文、规则步骤）。"}
	scope := strings.TrimSpace(ticket.Mandate.Scope)
	if scope == "" {
		scope = ticket.Title
	}
	parts = append(parts, "**要做什么**："+scope)
	if t := a.Store.Triggers().Get(ticket.Origin.TriggerID); t != nil {
		text := strings.TrimSpace(t.Text)
		if r := []rune(text); len(r) > SourceChars {
			text = string(r[:SourceChars]) + "…（全文见 ticket read）"
		}
		parts = append(parts, "**来源消息**：\n\n> "+strings.ReplaceAll(text, "\n", "\n> "))
	}
	if names := ruleNames(a, ticket.Mandate.Basis.Rules); names != "" {
		parts = append(parts, "**命中规则**："+names)
	}
	parts = append(parts, "**交付**：结论不要写成 issue 评论（发起人看不到 issue），用 "+cli+"report --kind ready --summary \"<中文结论与依据>\"` 交回；"+
		"受阻用 `--kind blocked --human-action \"<一个具体问题>\"`，没有可交付的用 `--kind dropped`。")
	if ticket.Mandate.Strength == "inferred" {
		parts = append(parts, "**授权有限**：这是主动接的单，只做规则范围内的小改动；涉及接口、行为、交互或产品决策就停下，报 dropped。")
	}
	return strings.Join(parts, "\n\n")
}

// ruleNames is the names of a ticket's matched rules.
func ruleNames(a *host.Agent, ids []string) string {
	names := []string{}
	for _, id := range ids {
		name := id
		for _, r := range a.Settings.Policy.Rules {
			if r.ID == id && r.Name != "" {
				name = r.Name
			}
		}
		names = append(names, name)
	}
	return strings.Join(names, "、")
}

// Read is multica ticket read: the ticket's full assignment, as its first run would read it.
func (x *Executor) Read(a *host.Agent, ticketID string) (map[string]any, error) {
	ticket := a.Store.Tickets().Get(ticketID)
	if ticket == nil {
		return nil, errors.New("unknown ticket")
	}
	var out map[string]any
	_ = json.Unmarshal([]byte(x.assignment(a, *ticket)), &out)
	if ticket.Execution != nil && ticket.Execution.Assignment != "" {
		out["assignment"] = ticket.Execution.Assignment
	}
	if t := a.Store.Triggers().Get(ticket.Origin.TriggerID); t != nil {
		if m, ok := out["trigger"].(map[string]any); ok {
			m["text"] = t.Text
			delete(m, "chars")
			delete(m, "read")
		}
	}
	return out, nil
}

// assignment is the first message of an Assignment: its assignment text, the source Trigger and its batch.
func (x *Executor) assignment(a *host.Agent, ticket core.Ticket) string {
	m := map[string]any{"kind": "assignment", "ticket_id": ticket.ID, "title": ticket.Title}
	if ticket.Execution != nil && ticket.Execution.Assignment != "" {
		m["assignment"] = cards.Capped(ticket.Execution.Assignment, cards.AssignmentChars, "ticket/"+ticket.ID+"/assignment")
	}
	if t := a.Store.Triggers().Get(ticket.Origin.TriggerID); t != nil {
		m["trigger"] = cards.TriggerMessage(*t, cards.Link{SourceURL: t.URL}, "", nil)
	}
	batch := []map[string]any{}
	for _, id := range ticket.Origin.TriggerIDs {
		if id == ticket.Origin.TriggerID {
			continue
		}
		if t := a.Store.Triggers().Get(id); t != nil {
			batch = append(batch, cards.BatchLine(*t))
		}
	}
	if len(batch) > 0 {
		m["batch"] = batch
	}
	data, _ := json.Marshal(m)
	return string(data)
}

// wakes reports whether an Input may start a Worker run: a bot's, CI's or another agent's message, or one the ticket
// only follows for context, does not (SA: bot/CI messages do not wake the Executor); it rides along with the next
// Input that does. Posting it would start the Worker on the ticket's issue, whose reply loops back.
func wakes(in *core.TicketInput) bool {
	return in != nil && (in.Kind != "trigger" || (in.Human && !in.Context))
}

func fromPerson(in *core.TicketInput) bool {
	if in == nil {
		return false
	}
	if in.Kind == "trigger" {
		return in.Human && !in.Context
	}
	return in.Kind == "text"
}

func findInput(t core.Ticket, id string) *core.TicketInput {
	for _, in := range t.Inputs {
		if in.ID == id {
			return in
		}
	}
	return nil
}

// deliverQueued hands a ticket's undelivered Inputs to its running Assignment as one comment, which starts a run.
func (x *Executor) deliverQueued(ctx context.Context, agentID, ticketID string, _ json.RawMessage) error {
	if x.o.Paused(agentID) {
		return effects.NewLater("Processing is paused")
	}
	a, err := x.o.Open(ctx, agentID)
	if err != nil {
		return err
	}
	ticket := a.Store.Tickets().Get(ticketID)
	if ticket == nil || !core.LiveTicket(*ticket) {
		return nil
	}
	assignment := core.RunningAssignment(ticket)
	if assignment == nil || len(ticket.Undelivered) == 0 {
		return nil
	}
	woken := false
	for _, id := range ticket.Undelivered {
		woken = woken || wakes(findInput(*ticket, id))
	}
	if !woken {
		return nil
	}
	if !Busy(*ticket) {
		by := "wake"
		for _, id := range ticket.Undelivered {
			if fromPerson(findInput(*ticket, id)) {
				by = "person"
			}
		}
		slots := x.slotsOf(a)
		if err := slots.Reserve(ticketID, by); err != nil {
			return err
		}
		defer slots.Free(ticketID)
	}
	ids := append([]string(nil), ticket.Undelivered...)
	var texts []string
	for _, id := range ids {
		input := findInput(*ticket, id)
		if input == nil {
			continue
		}
		var card map[string]any
		if input.Kind == "trigger" {
			if t := a.Store.Triggers().Get(input.TriggerID); t != nil {
				card = x.triggerCard(a, *ticket, *t, input.Note)
			}
		}
		text, ok := cards.InputMessage(*input, card)
		if !ok {
			continue
		}
		if len([]rune(text)) > cards.AssignmentChars {
			data, _ := json.Marshal(cards.Capped(text, cards.AssignmentChars, cards.InputResource(ticketID, input.ID)))
			text = string(data)
		}
		texts = append(texts, text)
	}
	if len(texts) > 0 {
		issueID := a.Store.TicketIssue(ticketID)
		taskID, err := x.o.Runs.Run(ctx, a.Agent, issueID, strings.Join(texts, "\n\n"))
		if err != nil {
			current := a.Store.Tickets().Get(ticketID)
			if current != nil && core.LiveTicket(*current) && core.RunningAssignment(current) != nil && core.RunningAssignment(current).ID == assignment.ID {
				if derr := a.Engine.DeliveryFailed(ticketID, assignment.ID, err.Error()); derr != nil {
					return derr
				}
			}
			return err
		}
		a.Store.SetAssignmentRun(assignment.ID, taskID, x.now())
	}
	return a.Engine.InputsDelivered(ticketID, assignment.ID, ids)
}

// triggerCard is a Trigger on a ticket for its worker, with the message it replies to when the worker has not seen it.
func (x *Executor) triggerCard(a *host.Agent, ticket core.Ticket, t core.Trigger, note string) map[string]any {
	var parent *core.Trigger
	if id := a.Registry.RepliesTo(t); id != "" {
		parent = a.Store.Triggers().Get(id)
	}
	if parent != nil {
		seen := parent.ID == ticket.Origin.TriggerID
		for _, in := range ticket.Inputs {
			seen = seen || (in.Kind == "trigger" && in.TriggerID == parent.ID)
		}
		if seen {
			parent = nil
		}
	}
	return cards.TriggerMessage(t, cards.Link{SourceURL: t.URL}, note, parent)
}

// Reported records a worker's report and queues its relay for the Router in the same transaction.
func (x *Executor) Reported(a *host.Agent, ticketID string, report core.WorkerReport) (ticket core.Ticket, err error) {
	defer func() {
		if r := recover(); r != nil {
			if e, ok := r.(error); ok {
				err = e
			} else {
				err = fmt.Errorf("%v", r)
			}
		}
	}()
	a.Store.Atomic(func() {
		var rerr error
		ticket, rerr = a.Engine.Report(ticketID, report)
		if rerr != nil {
			panic(rerr)
		}
		a.Store.ClearStuck(ticketID)
		if relay, ok := cards.ReportRelay(ticket, report); ok && x.o.Relay != nil {
			x.o.Relay.Relay(a.Store, relay)
		}
	})
	return ticket, nil
}

// Run statuses of a run's end.
const (
	Completed = "completed"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// RunEnded is a Multica run's end. A run of a ticket's running Assignment ends the worker's turn; a turn that reported
// nothing since its run started is reported waiting with the worker's last words, or failed when the run failed for
// good. A run that failed for a passing reason goes on, up to TransientTries times.
//
// on a ticket dispatched to a squad, the run of any squad member on the ticket's issue counts as the turn's
// run; issueID is the ended run's issue ("" when none).
func (x *Executor) RunEnded(ctx context.Context, taskID, issueID, status, output string) error {
	run, ok, err := x.RunOf(ctx, taskID, issueID)
	if err != nil || !ok {
		return err
	}
	a, err := x.o.Open(ctx, run.AgentID)
	if err != nil {
		return err
	}
	if current := a.Store.AssignmentRun(run.AssignmentID); current.TaskID != taskID && !run.Squad {
		return nil
	}
	ticket := a.Store.Tickets().Get(run.TicketID)
	if ticket == nil || !core.LiveTicket(*ticket) {
		return nil
	}
	assignment := core.RunningAssignment(ticket)
	if assignment == nil || assignment.ID != run.AssignmentID {
		return nil
	}
	if status == Failed && x.retries(run.AssignmentID, errors.New(output)) {
		next, err := x.o.Runs.Run(ctx, a.Agent, a.Store.TicketIssue(ticket.ID), "The previous run failed with a passing error ("+output+"). Continue the assignment.")
		if err == nil {
			a.Store.SetAssignmentRun(run.AssignmentID, next, x.now())
			return nil
		}
	}
	if err := a.Engine.TurnEnded(ticket.ID, run.AssignmentID); err != nil {
		return err
	}
	ticket = a.Store.Tickets().Get(run.TicketID)
	assignment = core.RunningAssignment(ticket)
	if assignment == nil || (assignment.Report != nil && assignment.Report.At >= run.StartedAt) || assignment.DeliveredAt > assignment.IdleAt {
		return nil
	}
	x.mu.Lock()
	delete(x.transient, run.AssignmentID)
	x.mu.Unlock()
	state, summary := "waiting", strings.TrimSpace(output)
	if status == Failed {
		state = "failed"
	}
	if summary == "" {
		summary = noReport
	}
	_, err = x.Reported(a, ticket.ID, core.WorkerReport{State: state, Summary: summary, Unreported: true})
	return err
}

// RunOf is the Assignment a Multica run works for: the one that started it, or, for a run on the issue of a ticket
// dispatched to a squad, that ticket's running Assignment.
func (x *Executor) RunOf(ctx context.Context, taskID, issueID string) (store.Run, bool, error) {
	run, ok, err := store.RunOfTask(ctx, x.storePool(), taskID)
	if err != nil || ok || issueID == "" {
		return run, ok, err
	}
	agentID, ticketID, found, err := store.TicketOfIssueAny(ctx, x.storePool(), issueID)
	if err != nil || !found {
		return run, false, err
	}
	a, err := x.o.Open(ctx, agentID)
	if err != nil {
		return run, false, err
	}
	ticket := a.Store.Tickets().Get(ticketID)
	if ticket == nil || ticket.Execution == nil || ticket.Execution.Target.Type != "squad" {
		return run, false, nil
	}
	assignment := core.RunningAssignment(ticket)
	if assignment == nil {
		return run, false, nil
	}
	current := a.Store.AssignmentRun(assignment.ID)
	return store.Run{AgentID: agentID, TicketID: ticketID, AssignmentID: assignment.ID, TaskID: taskID, StartedAt: current.StartedAt, Squad: true}, true, nil
}

func (x *Executor) storePool() *pgxpool.Pool { return x.o.Pool }
func encode(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}
