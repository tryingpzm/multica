package views

import (
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/panel"
)

// Settings is the settings view by block (SA settingsView); every block carries the agent's settings_version, which a
// save must name. The panel Source is internal (annotations) and has no settings card.
func (v *View) Settings(revision int) map[string]any {
	s := v.a.Settings
	// Each rule shows whether it is still the Router template's or the owner's own.
	origins := v.a.Store.RuleOrigins()
	rules := []map[string]any{}
	for _, r := range s.Policy.Rules {
		item := map[string]any{"id": r.ID, "name": r.Name, "enabled": r.Enabled, "instruction": r.Instruction, "origin": "customized"}
		if r.Target.ID != "" {
			item["target"] = r.Target
		}
		if policy.FromTemplate(r, origins) {
			item["origin"] = "template"
		}
		// Where the executor comes from: the template's, the owner's pick, or none and the fallback executor.
		switch o, ok := origins[r.ID]; {
		case r.Target.ID == "":
			item["target_origin"] = "fallback"
		case ok && o.Rule.Target == r.Target:
			item["target_origin"] = "template"
		default:
			item["target_origin"] = "owner"
		}
		rules = append(rules, item)
	}
	parts := map[string]any{}
	for id, part := range s.Policy.Sources {
		if id == panel.Source {
			continue
		}
		if part.Allowed == nil {
			part.Allowed = []policy.Allowed{}
		}
		if part.Admission == nil {
			part.Admission = map[string]policy.AdmissionRow{}
		}
		parts[id] = part
	}
	block := func(value any) map[string]any { return map[string]any{"value": value, "revision": revision} }
	// self is the owner's id on each Source's allowed list, shown as the owner and not removable.
	self := map[string]string{multica.Source: v.a.UserID}
	list := []any{}
	for _, inst := range v.a.Registry.Instances() {
		if inst.ID == panel.Source {
			continue
		}
		k := v.a.Registry.Kind(inst.Kind)
		item := map[string]any{"id": inst.ID, "kind": inst.Kind, "title": title(v.a.Registry, inst), "description": sources.Localized{},
			"installed": inst.Installed, "value": map[string]any{"enabled": inst.Enabled, "config": inst.Config}, "revision": revision,
			"restarts": []string{}, "fields": []sources.FieldSpec{}, "facts": map[string]any{}, "labels": v.a.Store.Labels(inst.ID), "self": self[inst.ID]}
		if k != nil {
			item["description"] = k.Description
			if k.Facts != nil {
				item["facts"] = k.Facts(inst.Config)
			}
			if k.Fields != nil {
				item["fields"] = k.Fields
			}
			if k.Policy != nil {
				p := map[string]any{"fields": k.Policy.Fields, "explicit": k.Policy.Explicit}
				if k.Policy.Fields == nil {
					p["fields"] = []sources.FieldSpec{}
				}
				if k.Policy.Rows != nil {
					p["rows"] = k.Policy.Rows(inst.Config)
				}
				item["policy"] = p
			}
		}
		list = append(list, item)
	}
	return map[string]any{
		"policyVersion": policy.PolicyVersion(s),
		"blocks": map[string]any{
			"policy":   block(map[string]any{"rules": rules, "sources": parts}),
			"eval":     block(map[string]any{"mode": s.Eval.Mode, "timeoutSeconds": s.Eval.TimeoutSeconds, "language": s.Eval.Language, "model": nil}),
			"executor": block(map[string]any{"routerAgentId": v.a.RouterAgentID}),
		},
		"sources": list,
	}
}
