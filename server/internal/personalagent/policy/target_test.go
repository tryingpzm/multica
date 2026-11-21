package policy_test

// Seam tests of MUL-13499: a rule's executor is an agent or a squad; a stored agentId reads as an agent target; the
// Router template's rules carry no executor, and the executor an owner picks survives a template sync.

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

func TestARuleTargetsAnAgentOrASquad(t *testing.T) {
	s := policy.MustParseSettings(`{"policy": {"rules": [
		{"id": "a", "name": "A", "enabled": true, "instruction": "x", "target": {"type": "squad", "id": "sq-1"}},
		{"id": "b", "name": "B", "enabled": true, "instruction": "x", "target": {"type": "agent", "id": "ag-1"}}]}}`)
	if got := s.Policy.Rules[0].Target; got != (policy.RuleTarget{Type: "squad", ID: "sq-1"}) {
		t.Fatalf("squad target = %+v", got)
	}
	if got := s.Policy.Rules[1].Target; got != (policy.RuleTarget{Type: "agent", ID: "ag-1"}) {
		t.Fatalf("agent target = %+v", got)
	}
	if _, err := policy.ParseSettings([]byte(`{"policy": {"rules": [{"id": "a", "name": "A", "enabled": true, "instruction": "x", "target": {"type": "team", "id": "t"}}]}}`)); err == nil {
		t.Fatal("an unknown target type is accepted")
	}
}

func TestAStoredAgentIDMigratesToAnAgentTarget(t *testing.T) {
	s := policy.MustParseSettings(`{"policy": {"rules": [{"id": "a", "name": "A", "enabled": true, "instruction": "x", "agentId": "ag-legacy"}]}}`)
	if got := s.Policy.Rules[0].Target; got != (policy.RuleTarget{Type: "agent", ID: "ag-legacy"}) {
		t.Fatalf("migrated target = %+v", got)
	}
	data, _ := json.Marshal(s.Policy.Rules[0])
	if string(data) != `{"id":"a","name":"A","enabled":true,"instruction":"x","target":{"type":"agent","id":"ag-legacy"}}` {
		t.Fatalf("saved as %s", data)
	}
}

func TestTemplateRulesCarryNoExecutor(t *testing.T) {
	stripped := policy.WithoutTargets([]policy.Rule{{ID: "a", Name: "A", Enabled: true, Instruction: "x", Target: policy.RuleTarget{Type: "agent", ID: "ag"}}})
	if stripped[0].Target != (policy.RuleTarget{}) {
		t.Fatalf("template rule keeps %+v", stripped[0].Target)
	}
	rules, _, _ := policy.SyncTemplateRules(nil, nil, 1, stripped)
	if rules[0].Target != (policy.RuleTarget{}) {
		t.Fatalf("copied rule has executor %+v", rules[0].Target)
	}
}

func TestTheOwnersExecutorSurvivesATemplateSync(t *testing.T) {
	v1 := []policy.Rule{{ID: "a", Name: "A", Enabled: true, Instruction: "v1"}}
	rules, origins, _ := policy.SyncTemplateRules(nil, nil, 1, v1)
	rules[0].Target = policy.RuleTarget{Type: "squad", ID: "sq"}
	if !policy.FromTemplate(rules[0], origins) {
		t.Fatal("picking an executor made the rule the owner's")
	}
	v2 := []policy.Rule{{ID: "a", Name: "A", Enabled: true, Instruction: "v2"}}
	rules, _, _ = policy.SyncTemplateRules(rules, origins, 2, v2)
	if rules[0].Instruction != "v2" || rules[0].Target != (policy.RuleTarget{Type: "squad", ID: "sq"}) {
		t.Fatalf("after sync: %+v", rules[0])
	}
}
