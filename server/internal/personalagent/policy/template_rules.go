package policy

// personal-agent a personal agent's rules follow the platform's Router template rule set. Each rule
// copied from the template records the template version and the rule as copied; a rule still equal to its copy is the
// owner's untouched template rule and follows the template, any other rule is the owner's.

import "reflect"

// RuleOrigin is the template rule a personal agent's rule was copied from.
type RuleOrigin struct {
	Version int  `json:"version"`
	Rule    Rule `json:"rule"`
}

// FromTemplate reports whether rule is still the template rule it was copied from; the executor is the owner's to pick
// and does not count.
func FromTemplate(rule Rule, origins map[string]RuleOrigin) bool {
	o, ok := origins[rule.ID]
	rule.Target, o.Rule.Target = RuleTarget{}, RuleTarget{}
	return ok && o.Rule == rule
}

// SyncTemplateRules brings rules in line with the template's rules at version: rules still on SA's defaults with no
// origins become the template's; otherwise the untouched template rules follow it (dropped when it drops them), the
// rules the owner changed or added stay, the ones they deleted stay deleted, and template rules new to them are added.
func SyncTemplateRules(rules []Rule, origins map[string]RuleOrigin, version int, template []Rule) ([]Rule, map[string]RuleOrigin, bool) {
	if len(origins) == 0 && reflect.DeepEqual(rules, DefaultRules()) {
		rules = nil
	}
	byID := map[string]Rule{}
	for _, t := range template {
		byID[t.ID] = t
	}
	out, next, kept := []Rule{}, map[string]RuleOrigin{}, map[string]bool{}
	for _, r := range rules {
		kept[r.ID] = true
		if !FromTemplate(r, origins) {
			if o, ok := origins[r.ID]; ok {
				next[r.ID] = o
			}
			out = append(out, r)
			continue
		}
		if t, ok := byID[r.ID]; ok {
			next[r.ID] = RuleOrigin{Version: version, Rule: t}
			// The executor the owner picked stays; one left as copied follows the template.
			if r.Target != origins[r.ID].Rule.Target {
				t.Target = r.Target
			}
			out = append(out, t)
		}
	}
	for _, t := range template {
		if kept[t.ID] {
			continue
		}
		if o, deleted := origins[t.ID]; deleted {
			next[t.ID] = o
			continue
		}
		out = append(out, t)
		next[t.ID] = RuleOrigin{Version: version, Rule: t}
	}
	return out, next, !reflect.DeepEqual(out, rules) || !reflect.DeepEqual(next, origins)
}
