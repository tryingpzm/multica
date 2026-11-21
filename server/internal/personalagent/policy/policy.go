package policy

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// PolicyRevision is mixed into every Policy version.
const PolicyRevision = "mandate-v3"

// PolicyVersion hashes the settings that affect work authorization: rules, accept mode and the Sources' parts.
func PolicyVersion(s Settings) string {
	data, _ := json.Marshal(struct {
		Rules   []Rule                  `json:"rules"`
		Mode    string                  `json:"mode"`
		Sources map[string]SourcePolicy `json:"sources"`
	}{s.Policy.Rules, s.Eval.Mode, s.Policy.Sources})
	return core.Key(PolicyRevision, string(data))
}

func sameName(a, b string) bool { return strings.ToLower(a) == strings.ToLower(b) }

// AllowedSender looks actor up in its Source's allowed list.
func AllowedSender(s Settings, actor core.ActorRef) *Allowed {
	for _, entry := range s.Policy.Sources[actor.Source].Allowed {
		if sameName(entry.ID, actor.ID) {
			e := entry
			return &e
		}
	}
	return nil
}

// ListedBot looks actor up in its Source's bot list, whose messages are inferred candidates at most.
func ListedBot(s Settings, actor core.ActorRef) *Allowed {
	for _, entry := range s.Policy.Sources[actor.Source].Bots {
		if sameName(entry.ID, actor.ID) {
			e := entry
			return &e
		}
	}
	return nil
}

// AnyoneAllowed reports whether any Source allows a sender.
func AnyoneAllowed(s Settings) bool {
	for _, part := range s.Policy.Sources {
		if len(part.Allowed) > 0 {
			return true
		}
	}
	return false
}

func senderName(entry Allowed) string {
	if entry.Name != "" {
		return entry.Name
	}
	return entry.ID
}

// participating reports whether a thread has tickets of its own (excluding context-only subscriptions).
func participating(thread core.Thread, tickets []core.Ticket) bool {
	ids := append([]string{}, thread.Subscriptions...)
	for _, t := range tickets {
		ids = append(ids, t.ID)
	}
	for _, id := range ids {
		if !contains(thread.Context, id) {
			return true
		}
	}
	return false
}

func admissionCell(allowed, addressed bool) string {
	switch {
	case allowed && addressed:
		return ""
	case allowed:
		return "listedUnaddressed"
	case addressed:
		return "unlistedAddressed"
	}
	return "unlistedUnaddressed"
}

func admissionOf(s Settings, source string, row *string, cell string) Admission {
	if row != nil {
		if r, ok := s.Policy.Sources[source].Admission[*row]; ok {
			if v := r.cell(cell); v != "" {
				return v
			}
		}
	}
	if s.Personal {
		return PersonalAdmissionDefaults[cell]
	}
	return AdmissionDefaults[cell]
}

// botOf is the bot entry actor passes the gate as: a listed bot, or for a personal agent any bot.
func botOf(s Settings, actor core.ActorRef) *Allowed {
	if bot := ListedBot(s, actor); bot != nil || !s.Personal {
		return bot
	}
	return &Allowed{ID: actor.ID}
}

var strength = map[Admission]int{"ignore": 0, "inferred": 1, "explicit": 2}

// Policy is the active Policy built from settings (SA settingsPolicy).
type Policy struct {
	settings Settings
	sources  core.SourceScope
	version  string
}

// SettingsPolicy builds the active Policy for gating Triggers and revalidating Mandates.
func SettingsPolicy(s Settings, sources core.SourceScope) *Policy {
	return &Policy{settings: s, sources: sources, version: PolicyVersion(s)}
}

func (p *Policy) Version() string      { return p.version }
func (p *Policy) Paused() bool         { return p.settings.Paused }
func (p *Policy) EvalTimeoutMs() int64 { return int64(p.settings.Eval.TimeoutSeconds) * 1000 }

func received(t core.Trigger, row *string) core.MandateBasis {
	return core.MandateBasis{Source: t.Source, Scope: t.Meta, Row: row}
}

// machineGate gates non-human triggers: only listed bots (as inferred candidates) and senderless system/CI events pass.
func (p *Policy) machineGate(t core.Trigger) core.GateResult {
	s, mode := p.settings, p.settings.Eval.Mode
	var bot *Allowed
	if t.Actor.ID != "" {
		bot = botOf(s, t.Actor)
		if bot == nil {
			return core.GateResult{Reason: "not_human"}
		}
	}
	scope := p.sources.Scope(t.Source, t.Meta)
	if !scope.Listened || !(t.Addressed != "" || scope.Observed) {
		return core.GateResult{Reason: "not_human"}
	}
	basis := received(t, scope.Row)
	if bot != nil {
		if mode == "off" {
			return core.GateResult{Reason: "admission_ignored"}
		}
		basis.Via, basis.Sender, basis.Addressed, basis.Observation = "bot", senderName(*bot), t.Addressed, &core.Observation{Mode: mode}
		return core.GateResult{Admissible: true, Strength: core.Inferred, Basis: basis}
	}
	cell := "unlistedUnaddressed"
	if admissionOf(s, t.Source, scope.Row, cell) != "inferred" || mode == "off" {
		return core.GateResult{Reason: "admission_ignored"}
	}
	basis.Via, basis.Cell, basis.Observation = "admission", cell, &core.Observation{Mode: mode}
	return core.GateResult{Admissible: true, Strength: core.Inferred, Basis: basis}
}

// Gate decides who may originate work.
func (p *Policy) Gate(t core.Trigger, thread core.Thread, tickets []core.Ticket) core.GateResult {
	if !t.Human {
		return p.machineGate(t)
	}
	s, mode := p.settings, p.settings.Eval.Mode
	allowed := AllowedSender(s, t.Actor)
	scope := p.sources.Scope(t.Source, t.Meta)
	var row *string
	if scope.Listened {
		row = scope.Row
	}
	recorded := received(t, row)
	if allowed != nil {
		recorded.Sender = senderName(*allowed)
	}
	if allowed != nil && t.Addressed != "" {
		b := recorded
		b.Via, b.Addressed = "addressed", t.Addressed
		return core.GateResult{Admissible: true, Strength: core.Explicit, Basis: b}
	}
	cell := admissionCell(allowed != nil, t.Addressed != "")
	// Unaddressed messages only use the admission table when the Source observes all messages in that scope.
	receivable := t.Addressed != "" || (scope.Listened && scope.Observed)
	admission := "ignore"
	if receivable {
		admission = admissionOf(s, t.Source, row, cell)
	}
	basis := recorded
	basis.Via, basis.Cell, basis.Addressed = "admission", cell, t.Addressed
	// Check explicit admission cells before thread follow-ups so a thread's tickets never weaken an explicit request.
	if admission == "explicit" {
		return core.GateResult{Admissible: true, Strength: core.Explicit, Basis: basis}
	}
	// Direct-message threads belong to one sender, and inferred follow-ups require an accept mode other than off.
	if !(t.Addressed == "direct" && allowed == nil) && (allowed != nil || mode != "off") && participating(thread, tickets) {
		b := recorded
		b.Via = "followup"
		st := core.Inferred
		if allowed != nil {
			st = core.Explicit
		}
		return core.GateResult{Admissible: true, Strength: st, Basis: b}
	}
	if admission == "inferred" && mode != "off" {
		basis.Observation = &core.Observation{Mode: mode}
		return core.GateResult{Admissible: true, Strength: core.Inferred, Basis: basis}
	}
	reason := "no_mention"
	if allowed == nil {
		reason = "not_a_member"
	} else if receivable && admission == "ignore" {
		reason = "admission_ignored"
	}
	return core.GateResult{Reason: reason}
}

// AdmitsInferred reports whether an inferred candidate may be accepted on the rules its assessment matched.
func (p *Policy) AdmitsInferred(rules []string) bool {
	mode := p.settings.Eval.Mode
	return (mode == "auto" || (mode == "observe" && PlanOnly(rules))) && len(rules) > 0 && p.rulesOn(rules)
}

// NeedsMeRule is the 「需要我处理」 rule: its ticket only proposes a plan, which the owner executes in the
// panel, so observe admits it.
const NeedsMeRule = "needs-me"

// PlanOnly reports whether every rule is NeedsMeRule.
func PlanOnly(rules []string) bool {
	for _, r := range rules {
		if r != NeedsMeRule {
			return false
		}
	}
	return len(rules) > 0
}

func (p *Policy) rulesOn(rules []string) bool {
	for _, id := range rules {
		on := false
		for _, rule := range p.settings.Policy.Rules {
			on = on || (rule.Enabled && rule.ID == id)
		}
		if !on {
			return false
		}
	}
	return true
}

// Revalidate re-checks a Mandate's gate-layer basis under this Policy (SA docs/002).
func (p *Policy) Revalidate(m core.Mandate) bool {
	s, mode, basis := p.settings, p.settings.Eval.Mode, m.Basis
	var current *core.ScopeResult
	if basis.Source != "" && basis.Scope != nil {
		c := p.sources.Scope(basis.Source, basis.Scope)
		current = &c
	}
	if current != nil && !current.Listened {
		return false
	}
	// Explicit follow-ups remain valid; inferred follow-ups require auto mode and enabled matched rules.
	if basis.Via == "followup" {
		return m.Strength == core.Explicit || ((mode == "auto" || (mode == "observe" && PlanOnly(basis.Rules))) && p.rulesOn(basis.Rules))
	}
	if basis.Via == "bot" && botOf(s, m.Actor) == nil {
		return false
	}
	if basis.Via == "admission" {
		listed := AllowedSender(s, m.Actor) != nil
		if basis.Cell == "unlistedAddressed" && listed {
			return true
		}
		now := basis.Cell
		if basis.Cell == "unlistedUnaddressed" && listed {
			now = "listedUnaddressed"
		}
		if now == "listedUnaddressed" && !listed {
			return false
		}
		var row *string
		if current != nil && current.Listened {
			row = current.Row
		}
		if strength[admissionOf(s, basis.Source, row, now)] < strength[m.Strength] {
			return false
		}
		if m.Strength == core.Explicit {
			return true
		}
	} else if basis.Via != "bot" && AllowedSender(s, m.Actor) == nil {
		return false
	}
	// Observed/inferred Mandates hold only while auto mode is active and all matched rules remain enabled.
	if (basis.Via == "admission" || basis.Via == "listed" || basis.Via == "bot") && basis.Observation != nil {
		return mode == "auto" && p.rulesOn(basis.Rules)
	}
	return true
}

// PolicyEffects computes how a settings change from old to now bears on each open ticket.
func PolicyEffects(tickets []core.Ticket, old, now Settings, scope func(Settings) core.SourceScope) map[string]core.PolicyEffect {
	before, after := SettingsPolicy(old, scope(old)), SettingsPolicy(now, scope(now))
	effects := map[string]core.PolicyEffect{}
	find := func(rules []Rule, id string) *Rule {
		for _, r := range rules {
			if r.ID == id {
				rr := r
				return &rr
			}
		}
		return nil
	}
	for _, ticket := range tickets {
		effect := core.PolicyEffect{}
		for _, id := range ticket.Mandate.Basis.Rules {
			was, is := find(old.Policy.Rules, id), find(now.Policy.Rules, id)
			if is == nil || !is.Enabled {
				if was != nil && was.Enabled {
					effect.Rules = append(effect.Rules, core.RuleEffect{ID: id, Name: was.Name, Change: "disabled"})
				}
				continue
			}
			if !reflect.DeepEqual(was, is) {
				effect.Rules = append(effect.Rules, core.RuleEffect{ID: id, Name: is.Name, Change: "changed", Instruction: is.Instruction})
			}
		}
		if before.Revalidate(ticket.Mandate) && !after.Revalidate(ticket.Mandate) {
			// With the previous accept mode and rules, the Mandate holds unless the sender or receiving scope lost authorization.
			asBefore := now
			asBefore.Eval = old.Eval
			asBefore.Policy.Rules = old.Policy.Rules
			if !SettingsPolicy(asBefore, scope(now)).Revalidate(ticket.Mandate) {
				no := false
				actor := ticket.Mandate.Actor
				effect.RequesterAllowed, effect.Requester = &no, &actor
			} else if ticket.Mandate.Strength == core.Inferred && old.Eval.Mode == "auto" && now.Eval.Mode != "auto" {
				no := false
				effect.Proactive = &no
			}
		}
		if !effect.Empty() {
			effects[ticket.ID] = effect
		}
	}
	return effects
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

var errNoEvidence = errors.New("Acceptance requires intent_evidence")
