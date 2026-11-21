package core

import (
	"encoding/json"
	"sort"
	"time"
)

// ActorRef is an actor identity in the source's own vocabulary.
type ActorRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// ThreadRef is a thread identity; the rule that derives ID from a native event belongs to the source.
type ThreadRef struct {
	Source string `json:"source"`
	ID     string `json:"id"`
}

// ChannelRef is a write-only address on a thread; the address format belongs to the source.
type ChannelRef struct {
	Source  string            `json:"source"`
	Thread  string            `json:"thread"`
	Address map[string]string `json:"address"`
}

// ThreadKey is the stable key of a thread across the whole domain.
func ThreadKey(ref ThreadRef) string { return ref.Source + ":" + ref.ID }

// Strength, AcceptMode and the other string unions of SA's model.
type (
	Strength       = string
	AdmissionCell  = string
	AcceptMode     = string
	DecisionKind   = string
	ReportState    = string
	TicketEndBy    = string
	TicketOutcome  = string
	AssignmentSate = string
)

const (
	Explicit = "explicit"
	Inferred = "inferred"
)

// Trigger is one immutable event produced by a source. Text and Meta are untrusted external data.
type Trigger struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	// Threads holds every thread the trigger belongs to; Threads[0] is the primary thread that hosts its evaluation.
	Threads []ThreadRef `json:"threads"`
	Actor   ActorRef    `json:"actor"`
	// Human is source-declared: a person wrote it rather than a bot, CI, or this agent.
	Human bool `json:"human"`
	// Notice is source-declared: a state change such as a status or assignment notice rather than a message. It
	// reaches a subscribed ticket only as context, so it neither resumes an ended ticket nor starts a Worker run (D-15).
	Notice bool `json:"notice,omitempty"`
	// Addressed is "mention" or "direct" when the sender addressed the agent.
	Addressed string         `json:"addressed,omitempty"`
	At        string         `json:"at"`
	Text      string         `json:"text"`
	Channel   *ChannelRef    `json:"channel,omitempty"`
	URL       string         `json:"url,omitempty"`
	Meta      map[string]any `json:"meta"`
}

// Handback records that a ticket's worker handed a Trigger back as beyond the ticket.
type Handback struct {
	TicketID string `json:"ticketId"`
	Reason   string `json:"reason"`
	At       string `json:"at"`
}

// TriggerDisposition is where a trigger went after intake (see SA model.ts).
type TriggerDisposition struct {
	Filtered   *Reason    `json:"filtered,omitempty"`
	EvalID     string     `json:"evalId,omitempty"`
	DecisionID string     `json:"decisionId,omitempty"`
	Fanout     []string   `json:"fanout"`
	Context    []string   `json:"context,omitempty"`
	Requeued   bool       `json:"requeued,omitempty"`
	Handbacks  []Handback `json:"handbacks,omitempty"`
	Acked      []string   `json:"acked,omitempty"`
	At         string     `json:"at"`
}

// FollowupRecords is what deciding a follow-up's owners reads.
type FollowupRecords interface {
	TicketByID(id string) *Ticket
	ThreadByKey(key string) *Thread
	DispositionOf(id string) *TriggerDisposition
}

// FollowupOwners returns the tickets whose worker decides a person's follow-up (SA followupOwners).
func FollowupOwners(trigger Trigger, records FollowupRecords) []string {
	if !trigger.Human {
		return []string{}
	}
	disposition := records.DispositionOf(trigger.ID)
	if disposition == nil || disposition.Requeued {
		return []string{}
	}
	returned := map[string]bool{}
	for _, item := range disposition.Handbacks {
		returned[item.TicketID] = true
	}
	reached := []string{}
	for _, id := range disposition.Fanout {
		if !returned[id] && !contains(disposition.Context, id) {
			reached = append(reached, id)
		}
	}
	if len(reached) == 0 {
		return []string{}
	}
	if th := records.ThreadByKey(ThreadKey(trigger.Threads[0])); th != nil && th.Evaluating != nil && contains(th.Evaluating.TriggerIDs, trigger.ID) {
		return []string{}
	}
	out := []string{}
	for _, id := range reached {
		ticket := records.TicketByID(id)
		if ticket == nil {
			continue
		}
		if LiveTicket(*ticket) {
			out = append(out, id)
			continue
		}
		if ticket.Ended.By == "human" {
			continue
		}
		for _, input := range ticket.Inputs {
			if input.Kind == "trigger" && input.TriggerID == trigger.ID && contains(ticket.Undelivered, input.ID) {
				out = append(out, id)
				break
			}
		}
	}
	return out
}

// Settled reports whether a Decision settled what a person's message asks for.
func Settled(decision Decision) bool {
	if decision.Kind == "stop" || decision.Kind == "unavailable" {
		return false
	}
	for _, r := range decision.Reason {
		if r.Code == "stopNotExplicit" || r.Code == "stopUnknown" || r.Code == "routed" {
			return false
		}
	}
	return true
}

// Observation records the accept mode under which an inferred candidate was made.
type Observation struct {
	Mode AcceptMode `json:"mode,omitempty"`
}

// MandateBasis is how the gate admitted the Trigger a Mandate came from (SA MandateBasis union, flattened by Via).
type MandateBasis struct {
	Source      string         `json:"source,omitempty"`
	Scope       map[string]any `json:"scope,omitempty"`
	Row         *string        `json:"row,omitempty"`
	Sender      string         `json:"sender,omitempty"`
	Rules       []string       `json:"rules,omitempty"`
	Via         string         `json:"via"`
	Addressed   string         `json:"addressed,omitempty"`
	Cell        AdmissionCell  `json:"cell,omitempty"`
	Observation *Observation   `json:"observation,omitempty"`
	Authority   string         `json:"authority,omitempty"`
}

// Mandate is the immutable authorization an accept decision produces.
type Mandate struct {
	Strength      Strength     `json:"strength"`
	PolicyVersion string       `json:"policyVersion"`
	Basis         MandateBasis `json:"basis"`
	Actor         ActorRef     `json:"actor"`
	Scope         string       `json:"scope"`
}

// Evaluation is the active trigger evaluation on a thread; at most one exists per thread.
type Evaluation struct {
	ID            string       `json:"id"`
	ThreadKey     string       `json:"threadKey"`
	TriggerIDs    []string     `json:"triggerIds"`
	Primary       string       `json:"primary,omitempty"`
	Strength      Strength     `json:"strength"`
	Actor         ActorRef     `json:"actor"`
	Basis         MandateBasis `json:"basis"`
	PolicyVersion string       `json:"policyVersion"`
	StartedAt     string       `json:"startedAt"`
	Delivered     string       `json:"delivered,omitempty"`
	Deadline      string       `json:"deadline"`
	PausedAt      string       `json:"pausedAt,omitempty"`
}

// Thread is the consistency boundary of eval, holder of subscriptions and decision history.
type Thread struct {
	Key           string         `json:"key"`
	Ref           ThreadRef      `json:"ref"`
	CreatedAt     string         `json:"createdAt"`
	LastAt        string         `json:"lastAt,omitempty"`
	Subscriptions []string       `json:"subscriptions"`
	Context       []string       `json:"context,omitempty"`
	Correlated    []string       `json:"correlated"`
	Pending       []string       `json:"pending"`
	Evaluating    *Evaluation    `json:"evaluating,omitempty"`
	Expired       int            `json:"expired,omitempty"`
	Meta          map[string]any `json:"meta"`
}

// Decision is the outcome of one eval, recorded on the thread.
type Decision struct {
	ID            string         `json:"id"`
	EvalID        string         `json:"evalId"`
	ThreadKey     string         `json:"threadKey"`
	TriggerIDs    []string       `json:"triggerIds"`
	PolicyVersion string         `json:"policyVersion"`
	At            string         `json:"at"`
	Kind          DecisionKind   `json:"kind"`
	Reason        []Reason       `json:"reason"`
	Strength      Strength       `json:"strength"`
	TicketID      string         `json:"ticketId,omitempty"`
	Mandate       *Mandate       `json:"mandate,omitempty"`
	Audit         map[string]any `json:"audit,omitempty"`
	Dismissed     bool           `json:"dismissed,omitempty"`
}

// Report is what an Assignment last reported.
type Report struct {
	State   ReportState `json:"state"`
	Summary string      `json:"summary"`
	At      string      `json:"at"`
	// Unreported: the worker's run ended without a report and the host filed this one.
	Unreported bool `json:"unreported,omitempty"`
}

// Assignment binds a ticket to one executor instance.
type Assignment struct {
	ID          string  `json:"id"`
	State       string  `json:"state"`
	CreatedAt   string  `json:"createdAt"`
	StartedAt   string  `json:"startedAt,omitempty"`
	EndedAt     string  `json:"endedAt,omitempty"`
	EndedReason string  `json:"endedReason,omitempty"`
	Report      *Report `json:"report,omitempty"`
	DeliveredAt string  `json:"deliveredAt,omitempty"`
	IdleAt      string  `json:"idleAt,omitempty"`
}

// TicketInput is SA's TicketInput union, flattened by Kind: trigger, policy-changed, reopen, resumed, text, note.
type TicketInput struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	TriggerID string `json:"triggerId,omitempty"`
	At        string `json:"at"`
	Note      string `json:"note,omitempty"`
	Human     bool   `json:"human,omitempty"`
	Context   bool   `json:"context,omitempty"`
	// ReadAt is nil while undefined, a pointer to "" for SA's null, else the time.
	ReadAt      *string       `json:"readAt,omitempty"`
	DeliveredTo string        `json:"deliveredTo,omitempty"`
	DeliveredAt string        `json:"deliveredAt,omitempty"`
	From        string        `json:"from,omitempty"`
	To          string        `json:"to,omitempty"`
	Effect      *PolicyEffect `json:"effect,omitempty"`
	DecisionID  string        `json:"decisionId,omitempty"`
	Text        string        `json:"text,omitempty"`
}

// MarshalJSON writes SA's null for a delivered but unread Input.
func (i TicketInput) MarshalJSON() ([]byte, error) {
	type plain TicketInput
	data, err := json.Marshal(plain(i))
	if err != nil || i.ReadAt == nil || *i.ReadAt != "" {
		return data, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	m["readAt"] = json.RawMessage("null")
	return json.Marshal(m)
}

// UnmarshalJSON reads SA's null readAt as a pointer to "".
func (i *TicketInput) UnmarshalJSON(data []byte) error {
	type plain TicketInput
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if raw, ok := m["readAt"]; ok && string(raw) == "null" {
		empty := ""
		p.ReadAt = &empty
	}
	*i = TicketInput(p)
	return nil
}

// RuleEffect is how a Policy change bears on one rule a ticket was accepted under.
type RuleEffect struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Change      string `json:"change"`
	Instruction string `json:"instruction,omitempty"`
}

// PolicyEffect is how a Policy change bears on a ticket.
type PolicyEffect struct {
	Rules            []RuleEffect `json:"rules,omitempty"`
	Proactive        *bool        `json:"proactive,omitempty"`
	RequesterAllowed *bool        `json:"requesterAllowed,omitempty"`
	Requester        *ActorRef    `json:"requester,omitempty"`
}

// Empty reports whether the effect says nothing.
func (e PolicyEffect) Empty() bool {
	return len(e.Rules) == 0 && e.Proactive == nil && e.RequesterAllowed == nil && e.Requester == nil
}

// Attention is a question for a person.
type Attention struct {
	ID        string `json:"id"`
	Question  string `json:"question"`
	TriggerID string `json:"triggerId,omitempty"`
}

// Ended is when, by whom and how a ticket ended.
type Ended struct {
	At      string        `json:"at"`
	By      TicketEndBy   `json:"by"`
	Outcome TicketOutcome `json:"outcome"`
	Reason  string        `json:"reason,omitempty"`
}

// Closed is when and how a followed Thread closed.
type Closed struct {
	At      string `json:"at"`
	Outcome string `json:"outcome"`
}

// Follow is a Thread a ticket follows, by its key.
type Follow struct {
	Thread string  `json:"thread"`
	Own    bool    `json:"own"`
	Closed *Closed `json:"closed,omitempty"`
}

// Execution is what its Executor chose.
type Execution struct {
	Name       string `json:"name"`
	Directory  string `json:"directory"`
	Assignment string `json:"assignment"`
	// Target is the executor of the first rule the ticket was accepted under that names one; zero for the default
	// worker (an agent or a squad; a stored agentId reads as an agent).
	Target Target `json:"target,omitzero"`
}

// Target is a Multica agent or squad.
type Target struct {
	Type string `json:"type"` // agent | squad
	ID   string `json:"id"`
}

// UnmarshalJSON reads an Execution, migrating the legacy agentId.
func (e *Execution) UnmarshalJSON(data []byte) error {
	type plain Execution
	var in struct {
		plain
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	*e = Execution(in.plain)
	if e.Target.ID == "" && in.AgentID != "" {
		e.Target = Target{Type: "agent", ID: in.AgentID}
	}
	return nil
}

// Origin is the Triggers a ticket was accepted from.
type Origin struct {
	TriggerID  string   `json:"triggerId"`
	TriggerIDs []string `json:"triggerIds"`
}

// Delivery is the latest failed delivery of an Input to a ticket's worker.
type Delivery struct {
	Error string `json:"error"`
	At    string `json:"at"`
}

// Ticket is an authorized work item with its assignments, channels, thread bindings and inputs.
type Ticket struct {
	ID          string         `json:"id"`
	Title       string         `json:"title"`
	DecisionID  string         `json:"decisionId"`
	Mandate     Mandate        `json:"mandate"`
	Channels    []ChannelRef   `json:"channels"`
	Ended       *Ended         `json:"ended,omitempty"`
	Attention   *Attention     `json:"attention,omitempty"`
	Assignments []*Assignment  `json:"assignments"`
	Inputs      []*TicketInput `json:"inputs"`
	Undelivered []string       `json:"undelivered"`
	Follows     []*Follow      `json:"follows,omitempty"`
	Answers     []string       `json:"answers,omitempty"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
	Origin      Origin         `json:"origin"`
	Execution   *Execution     `json:"execution,omitempty"`
	Delivery    *Delivery      `json:"delivery,omitempty"`
}

// LiveTicket reports whether a ticket is open: it has not ended.
func LiveTicket(ticket Ticket) bool { return ticket.Ended == nil }

// TicketOutcomeOf returns how a ticket ended, or "".
func TicketOutcomeOf(ticket Ticket) string {
	if ticket.Ended == nil {
		return ""
	}
	return ticket.Ended.Outcome
}

// RunningAssignment returns the ticket's running assignment, or nil.
func RunningAssignment(ticket *Ticket) *Assignment {
	for _, a := range ticket.Assignments {
		if a.State == "running" {
			return a
		}
	}
	return nil
}

// LatestAssignment returns the ticket's last assignment, or nil.
func LatestAssignment(ticket *Ticket) *Assignment {
	if len(ticket.Assignments) == 0 {
		return nil
	}
	return ticket.Assignments[len(ticket.Assignments)-1]
}

// LatestReport returns the latest report any of a ticket's Assignments made.
func LatestReport(ticket *Ticket) *Report {
	for i := len(ticket.Assignments) - 1; i >= 0; i-- {
		if ticket.Assignments[i].Report != nil {
			return ticket.Assignments[i].Report
		}
	}
	return nil
}

// Reached reports whether a Trigger reached a ticket (SA reached).
func Reached(ticket *Ticket, trigger Trigger, thread func(key string) *Thread, context bool) bool {
	if ticket.Origin.TriggerID == trigger.ID {
		return true
	}
	input := false
	for _, item := range ticket.Inputs {
		if item.Kind == "trigger" && item.TriggerID == trigger.ID {
			input = true
			break
		}
	}
	for _, ref := range trigger.Threads {
		current := thread(ThreadKey(ref))
		if current != nil && contains(current.Subscriptions, ticket.ID) && (context || !contains(current.Context, ticket.ID)) {
			return true
		}
	}
	if !input {
		return false
	}
	if context {
		return true
	}
	for _, ref := range trigger.Threads {
		current := thread(ThreadKey(ref))
		if current == nil || !contains(current.Context, ticket.ID) {
			return true
		}
	}
	return false
}

// ReadUntil is until when a ticket's worker had a turn to read what reached it (SA readUntil).
func ReadUntil(ticket *Ticket, progress bool) string {
	best := ""
	for i := len(ticket.Assignments) - 1; i >= 0; i-- {
		a := ticket.Assignments[i]
		if a.Report != nil && (progress || a.Report.State != "progress") {
			best = a.Report.At
			break
		}
	}
	for _, a := range ticket.Assignments {
		if a.IdleAt > best {
			best = a.IdleAt
		}
	}
	return best
}

// WaitsForPerson reports whether a ticket waits for a person (SA waitsForPerson).
func WaitsForPerson(ticket *Ticket) bool {
	latest := LatestAssignment(ticket)
	if latest == nil || latest.Report == nil {
		return false
	}
	report := latest.Report
	if report.State != "blocked" && report.State != "failed" {
		return false
	}
	for _, input := range ticket.Inputs {
		if input.Kind == "trigger" && input.Human && !input.Context && input.At > report.At {
			return false
		}
	}
	return true
}

// ISOTime normalizes valid timestamps to Date#toISOString form; values that do not parse are returned unchanged.
func ISOTime(value string) string {
	if ms, ok := ParseTime(value); ok {
		return FormatTime(ms)
	}
	return value
}

// ParseTime parses a timestamp as Date.parse does for ISO forms, in milliseconds; ok is false for SA's NaN.
func ParseTime(value string) (int64, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.000Z07:00", "2006-01-02T15:04Z07:00", "2006-01-02"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UnixMilli(), true
		}
	}
	return 0, false
}

// FormatTime formats milliseconds as Date#toISOString.
func FormatTime(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z") }

// laterThan is Date.parse(a) > Date.parse(b), false when either is NaN.
func laterThan(a, b string) bool {
	x, ok1 := ParseTime(a)
	y, ok2 := ParseTime(b)
	return ok1 && ok2 && x > y
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func without(list []string, values ...string) []string {
	out := []string{}
	for _, item := range list {
		if !contains(values, item) {
			out = append(out, item)
		}
	}
	return out
}

func uniq(list []string) []string {
	out := []string{}
	for _, item := range list {
		if !contains(out, item) {
			out = append(out, item)
		}
	}
	return out
}

func sortedBy[T any](list []T, less func(a, b T) bool) []T {
	out := append([]T(nil), list...)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j]) })
	return out
}
