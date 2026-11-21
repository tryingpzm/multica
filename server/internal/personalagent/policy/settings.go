// Package policy is a Go translation of dsh-superagent-prototype src/policy.ts and src/admission.ts: the settings that
// authorize work, the gate's admission table, Mandate revalidation and assessment outcomes.
package policy

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Admission is how a cell of the admission table admits a message.
type Admission = string

// AdmissionOptions are the values each cell allows; a listed sender who addresses the agent uses no cell.
var AdmissionOptions = map[string][]Admission{
	"listedUnaddressed":   {"inferred", "ignore"},
	"unlistedAddressed":   {"explicit", "inferred", "ignore"},
	"unlistedUnaddressed": {"inferred", "ignore"},
}

// AdmissionDefaults match the behaviour before the table: unaddressed messages from listed senders are inferred
// candidates; unlisted senders are ignored.
var AdmissionDefaults = map[string]Admission{"listedUnaddressed": "inferred", "unlistedAddressed": "ignore", "unlistedUnaddressed": "ignore"}

// PersonalAdmissionDefaults are a personal agent's defaults: anyone who @s the owner asks explicitly, and
// unaddressed messages, which only reach the table in rows that receive all messages, are inferred candidates.
var PersonalAdmissionDefaults = map[string]Admission{"listedUnaddressed": "inferred", "unlistedAddressed": "explicit", "unlistedUnaddressed": "inferred"}

// AdmissionRow is one receiving scope's row of the admission table, by cell.
type AdmissionRow struct {
	ListedUnaddressed   string `json:"listedUnaddressed,omitempty"`
	UnlistedAddressed   string `json:"unlistedAddressed,omitempty"`
	UnlistedUnaddressed string `json:"unlistedUnaddressed,omitempty"`
}

func (r AdmissionRow) cell(name string) string {
	switch name {
	case "listedUnaddressed":
		return r.ListedUnaddressed
	case "unlistedAddressed":
		return r.UnlistedAddressed
	}
	return r.UnlistedUnaddressed
}

// Allowed is a sender (person or bot) on a Source's list.
type Allowed struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// SourcePolicy is a Source's part of the Policy: who may originate work there, and its admission table by row.
type SourcePolicy struct {
	Allowed   []Allowed               `json:"allowed"`
	Admission map[string]AdmissionRow `json:"admission"`
	Bots      []Allowed               `json:"bots,omitempty"`
}

// Rule is an assessment rule.
type Rule struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Instruction string `json:"instruction"`
	// Target, when set, is the Multica agent or squad a ticket accepted under this rule is assigned to (,
	// ); a stored agentId reads as an agent target.
	Target RuleTarget `json:"target,omitzero"`
}

// RuleTarget is the executor of a rule: a Multica agent or squad.
type RuleTarget struct {
	Type string `json:"type"` // agent | squad
	ID   string `json:"id"`
}

// UnmarshalJSON reads a rule, migrating the legacy agentId to an agent target.
func (r *Rule) UnmarshalJSON(data []byte) error {
	type plain Rule
	var in struct {
		plain
		AgentID string `json:"agentId"`
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	*r = Rule(in.plain)
	if r.Target.ID == "" && in.AgentID != "" {
		r.Target = RuleTarget{Type: "agent", ID: in.AgentID}
	}
	return nil
}

// WithoutTargets is rules with no executor: the Router template's rules are shared across teams.
func WithoutTargets(rules []Rule) []Rule {
	out := make([]Rule, len(rules))
	for i, r := range rules {
		r.Target = RuleTarget{}
		out[i] = r
	}
	return out
}

// PolicyConfig is the rules and the Sources' parts.
type PolicyConfig struct {
	Rules   []Rule                  `json:"rules"`
	Sources map[string]SourcePolicy `json:"sources"`
}

// RoleModel names a model.
type RoleModel struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

// EvalConfig is the accept mode, the assessment timeout and the assessor's model.
type EvalConfig struct {
	Mode           string     `json:"mode"`
	TimeoutSeconds int        `json:"timeoutSeconds"`
	Model          *RoleModel `json:"model"`
	// Language is the language the Router writes reasons, acknowledgements, titles and scopes in: zh (default) or en.
	Language string `json:"language"`
}

// SourceInstance is a Source's switch and configuration.
type SourceInstance struct {
	Enabled bool           `json:"enabled"`
	Config  map[string]any `json:"config"`
}

// Settings is a personal agent's runtime settings (SA SettingsSchema without the DSH executor block).
type Settings struct {
	Paused bool `json:"paused"`
	// personal-agent Personal marks a personal agent's settings: no list of who may originate work is
	// required, so its admission defaults are PersonalAdmissionDefaults and any bot is an inferred candidate.
	Personal bool                      `json:"personal,omitempty"`
	Sources  map[string]SourceInstance `json:"sources"`
	Policy   PolicyConfig              `json:"policy"`
	Eval     EvalConfig                `json:"eval"`
}

// DefaultRules are SA's default rules.
func DefaultRules() []Rule {
	return []Rule{
		{ID: "copy", Name: "Copy fixes", Enabled: true, Instruction: "Explicit requests to correct a typo, wording, punctuation or existing translation. Preserve product promises and business meaning."},
		{ID: "visual", Name: "Visual fixes", Enabled: true, Instruction: "Explicit implementation requests for local spacing, alignment, overflow or styling, with a specified target or an objectively established existing standard/correct state to restore. Locating an element or calling it inconsistent, unattractive or hard to understand does not establish a target. Do not invent visual style, information hierarchy, rendering or interaction decisions. No redesign, new interaction or behavior. A known spacing defect does not authorize adjacent undefined restyling; assess only a clearly separable authorized scope. If direction or scope is unsettled, observe only: no acknowledgement, worker or unsolicited question."},
	}
}

var (
	ruleID     = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
	sourceName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
)

type rawSettings struct {
	Paused   *bool `json:"paused"`
	Personal bool  `json:"personal"`
	Sources  map[string]*struct {
		Enabled *bool          `json:"enabled"`
		Config  map[string]any `json:"config"`
	} `json:"sources"`
	Policy *struct {
		Rules   *[]Rule `json:"rules"`
		Sources map[string]*struct {
			Allowed   []Allowed                    `json:"allowed"`
			Admission map[string]map[string]string `json:"admission"`
			Bots      []Allowed                    `json:"bots"`
		} `json:"sources"`
	} `json:"policy"`
	Eval *struct {
		Mode           *string    `json:"mode"`
		TimeoutSeconds *int       `json:"timeoutSeconds"`
		Model          *RoleModel `json:"model"`
		Language       *string    `json:"language"`
	} `json:"eval"`
}

// ParseSettings parses stored settings JSON, filling SA's defaults and refusing what SA's schema refuses.
func ParseSettings(data []byte) (Settings, error) {
	var raw rawSettings
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return Settings{}, err
		}
	}
	s := Settings{Sources: map[string]SourceInstance{}, Policy: PolicyConfig{Rules: DefaultRules(), Sources: map[string]SourcePolicy{}},
		Eval: EvalConfig{Mode: "observe", TimeoutSeconds: 120, Language: "zh"}}
	if raw.Paused != nil {
		s.Paused = *raw.Paused
	}
	s.Personal = raw.Personal
	for name, src := range raw.Sources {
		if !sourceName.MatchString(name) {
			return Settings{}, fmt.Errorf("invalid source name %q", name)
		}
		inst := SourceInstance{Enabled: true, Config: map[string]any{}}
		if src != nil {
			if src.Enabled != nil {
				inst.Enabled = *src.Enabled
			}
			if src.Config != nil {
				inst.Config = src.Config
			}
		}
		s.Sources[name] = inst
	}
	if raw.Policy != nil {
		if raw.Policy.Rules != nil {
			s.Policy.Rules = *raw.Policy.Rules
		}
		for name, part := range raw.Policy.Sources {
			if !sourceName.MatchString(name) {
				return Settings{}, fmt.Errorf("invalid source name %q", name)
			}
			sp := SourcePolicy{Allowed: []Allowed{}, Admission: map[string]AdmissionRow{}}
			if part != nil {
				if part.Allowed != nil {
					sp.Allowed = part.Allowed
				}
				sp.Bots = part.Bots
				for row, cells := range part.Admission {
					r, err := admissionRow(cells)
					if err != nil {
						return Settings{}, fmt.Errorf("admission row %s: %w", row, err)
					}
					sp.Admission[row] = r
				}
			}
			for _, a := range append(append([]Allowed{}, sp.Allowed...), sp.Bots...) {
				if id := strings.TrimSpace(a.ID); id == "" || len(id) > 200 {
					return Settings{}, fmt.Errorf("invalid allowed id %q", a.ID)
				}
			}
			s.Policy.Sources[name] = sp
		}
	}
	seen := map[string]bool{}
	for _, rule := range s.Policy.Rules {
		if !ruleID.MatchString(rule.ID) {
			return Settings{}, fmt.Errorf("invalid rule id %q", rule.ID)
		}
		if seen[rule.ID] {
			return Settings{}, fmt.Errorf("Duplicate rule IDs")
		}
		seen[rule.ID] = true
		if strings.TrimSpace(rule.Name) == "" || len(rule.Name) > 100 || strings.TrimSpace(rule.Instruction) == "" || len(rule.Instruction) > 2000 {
			return Settings{}, fmt.Errorf("invalid rule %q", rule.ID)
		}
		if rule.Target != (RuleTarget{}) && (rule.Target.Type != "agent" && rule.Target.Type != "squad" || strings.TrimSpace(rule.Target.ID) == "") {
			return Settings{}, fmt.Errorf("invalid target of rule %q", rule.ID)
		}
	}
	if raw.Eval != nil {
		if raw.Eval.Mode != nil {
			s.Eval.Mode = *raw.Eval.Mode
		}
		if raw.Eval.TimeoutSeconds != nil {
			s.Eval.TimeoutSeconds = *raw.Eval.TimeoutSeconds
		}
		s.Eval.Model = raw.Eval.Model
		if raw.Eval.Language != nil {
			s.Eval.Language = *raw.Eval.Language
		}
	}
	if s.Eval.Mode != "off" && s.Eval.Mode != "observe" && s.Eval.Mode != "auto" {
		return Settings{}, fmt.Errorf("invalid accept mode %q", s.Eval.Mode)
	}
	if s.Eval.Language != "zh" && s.Eval.Language != "en" {
		return Settings{}, fmt.Errorf("invalid language %q", s.Eval.Language)
	}
	if s.Eval.TimeoutSeconds < 30 || s.Eval.TimeoutSeconds > 600 {
		return Settings{}, fmt.Errorf("eval timeout must be within 30..600 seconds")
	}
	return s, nil
}

// MustParseSettings is ParseSettings of a literal that must be valid.
func MustParseSettings(data string) Settings {
	s, err := ParseSettings([]byte(data))
	if err != nil {
		panic(err)
	}
	return s
}

// AdmissionTable validates a submitted admission table (SA AdmissionTableSchema), keyed by row.
func AdmissionTable(raw any) (map[string]AdmissionRow, error) {
	table := map[string]AdmissionRow{}
	if raw == nil {
		return table, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var rows map[string]map[string]string
	if err := json.Unmarshal(data, &rows); err != nil {
		return nil, fmt.Errorf("admission table: %w", err)
	}
	for row, cells := range rows {
		r, err := admissionRow(cells)
		if err != nil {
			return nil, fmt.Errorf("admission row %s: %w", row, err)
		}
		table[row] = r
	}
	return table, nil
}

func admissionRow(cells map[string]string) (AdmissionRow, error) {
	var row AdmissionRow
	for cell, value := range cells {
		options, ok := AdmissionOptions[cell]
		if !ok {
			return row, fmt.Errorf("unknown cell %q", cell)
		}
		valid := false
		for _, o := range options {
			valid = valid || o == value
		}
		if !valid {
			return row, fmt.Errorf("cell %s cannot be %q", cell, value)
		}
		switch cell {
		case "listedUnaddressed":
			row.ListedUnaddressed = value
		case "unlistedAddressed":
			row.UnlistedAddressed = value
		default:
			row.UnlistedUnaddressed = value
		}
	}
	return row, nil
}
