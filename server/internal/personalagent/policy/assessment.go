package policy

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// RuleAssessment is an assessment's judgement of one enabled rule.
type RuleAssessment struct {
	RuleID  string `json:"rule_id"`
	Matched bool   `json:"matched"`
	Reason  string `json:"reason"`
}

// Assessment is what the assessor's Session submits for a candidate (SA Assessment).
type Assessment struct {
	Decision        string           `json:"decision"`
	RuleAssessments []RuleAssessment `json:"rule_assessments"`
	Direction       any              `json:"direction"`
	Title           string           `json:"title"`
	Scope           string           `json:"scope"`
	Reason          string           `json:"reason"`
	IntentEvidence  *string          `json:"intent_evidence,omitempty"`
	Acknowledgement *string          `json:"acknowledgement,omitempty"`
	WorktreeName    string           `json:"worktree_name,omitempty"`
}

// DirectionAllowed checks that an assessment established implementation intent and a settled target without open
// design decisions (SA AdmissionDirectionSchema, strict).
func DirectionAllowed(value any) bool {
	m, ok := value.(map[string]any)
	if !ok || len(m) != 4 {
		return false
	}
	requested, ok1 := m["implementation_requested"].(bool)
	basis, ok2 := m["target_basis"].(string)
	design, ok3 := m["requires_design_decision"].(bool)
	evidence, ok4 := m["evidence"].(string)
	evidence = strings.TrimSpace(evidence)
	if !ok1 || !ok2 || !ok3 || !ok4 || evidence == "" || len([]rune(evidence)) > 700 {
		return false
	}
	if basis != "specified" && basis != "existing-standard" && basis != "undetermined" {
		return false
	}
	return requested && basis != "undetermined" && !design
}

// AssessmentOutcome validates an assessment against enabled rules and converts it into an Engine outcome. It accepts an
// inferred candidate only in auto mode, otherwise recording it as would_accept.
func AssessmentOutcome(a Assessment, s Settings, strength core.Strength) (core.Outcome, error) {
	enabled := []Rule{}
	for _, r := range s.Policy.Rules {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	ids := map[string]bool{}
	for _, item := range a.RuleAssessments {
		ids[item.RuleID] = true
	}
	covered := len(a.RuleAssessments) == len(enabled) && len(ids) == len(a.RuleAssessments)
	for _, r := range enabled {
		covered = covered && ids[r.ID]
	}
	if !covered {
		return core.Outcome{}, errors.New("Exactly one assessment for every enabled rule is required; disabled, unknown or missing rule IDs are not allowed")
	}
	matches := []map[string]any{}
	rules := []string{}
	for _, item := range a.RuleAssessments {
		if item.Matched {
			matches = append(matches, map[string]any{"rule_id": item.RuleID, "reason": item.Reason})
			rules = append(rules, item.RuleID)
		}
	}
	judged := []any{}
	data, _ := json.Marshal(a.RuleAssessments)
	_ = json.Unmarshal(data, &judged)
	audit := map[string]any{"matches": matches, "rule_assessments": judged, "direction": a.Direction}
	if a.IntentEvidence != nil && *a.IntentEvidence != "" {
		audit["intent_evidence"] = *a.IntentEvidence
	}
	if a.Decision != "accept" {
		return core.Outcome{Kind: a.Decision, Said: a.Reason, Audit: audit}, nil
	}
	if !DirectionAllowed(a.Direction) {
		return core.Outcome{Kind: "ignore", Audit: audit, ReasonCode: &core.Reason{Code: "directionUnsettled"}}, nil
	}
	if len(matches) == 0 {
		return core.Outcome{Kind: "ignore", Audit: audit, ReasonCode: &core.Reason{Code: "noRuleMatched"}}, nil
	}
	if a.IntentEvidence == nil || *a.IntentEvidence == "" {
		return core.Outcome{}, errNoEvidence
	}
	if strength == core.Inferred && s.Eval.Mode != "auto" && !(s.Eval.Mode == "observe" && PlanOnly(rules)) {
		audit["would_accept"] = true
		proposal := map[string]any{"title": a.Title, "scope": a.Scope}
		if a.WorktreeName != "" {
			proposal["worktree_name"] = a.WorktreeName
		}
		audit["proposal"] = proposal
		return core.Outcome{Kind: "ignore", Audit: audit, ReasonCode: &core.Reason{Code: "observed", Values: map[string]string{"text": a.Reason}}}, nil
	}
	o := core.Outcome{Kind: "accept", Title: a.Title, Scope: a.Scope, Said: a.Reason, Rules: rules, Audit: audit, Name: a.WorktreeName}
	if a.Acknowledgement != nil {
		o.Acknowledgement = *a.Acknowledgement
	}
	return o, nil
}
