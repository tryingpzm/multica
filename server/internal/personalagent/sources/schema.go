package sources

import (
	"fmt"
	"regexp"
	"strings"
)

// ScopeRow is one receiving scope of a Source: a group or channel and whether it receives every message.
type ScopeRow struct {
	Row    string `json:"row"`
	Listen string `json:"listen"` // all | mention
}

// ParseRows validates config["rows"] as stored by pa_source_scope: unique rows, listen all or mention.
func ParseRows(raw any) ([]ScopeRow, error) {
	out := []ScopeRow{}
	if raw == nil {
		return out, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("rows must be a list")
	}
	seen := map[string]bool{}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("row must be an object")
		}
		row, _ := m["row"].(string)
		listen, _ := m["listen"].(string)
		if strings.TrimSpace(row) == "" {
			return nil, fmt.Errorf("row is required")
		}
		if listen == "" {
			listen = "mention"
		}
		if listen != "all" && listen != "mention" {
			return nil, fmt.Errorf("row %s: listen must be all or mention", row)
		}
		if seen[row] {
			return nil, fmt.Errorf("Duplicate rows")
		}
		seen[row] = true
		out = append(out, ScopeRow{Row: row, Listen: listen})
	}
	return out, nil
}

// RowsAny is rows as a configuration value.
func RowsAny(rows []ScopeRow) []any {
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{"row": r.Row, "listen": r.Listen})
	}
	return out
}

// Rows reads already validated rows from a configuration.
func Rows(config Config) []ScopeRow {
	rows, _ := ParseRows(config["rows"])
	return rows
}

// Strings validates a list of strings.
func Strings(raw any, field string) ([]string, error) {
	out := []string{}
	if raw == nil {
		return out, nil
	}
	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a list of strings", field)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("%s must be a list of strings", field)
}

// String validates an optional string matching pattern (when not nil).
func String(raw any, field string, pattern *regexp.Regexp) (string, error) {
	if raw == nil {
		return "", nil
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", field)
	}
	if pattern != nil && s != "" && !pattern.MatchString(s) {
		return "", fmt.Errorf("%s is invalid", field)
	}
	return s, nil
}

// AllowedList validates a policy part's allowed or bots list: unique non-empty ids, optional names.
func AllowedList(raw any, field string) ([]any, error) {
	out := []any{}
	if raw == nil {
		return out, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list", field)
	}
	seen := map[string]bool{}
	for _, item := range list {
		m, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s entry must be an object", field)
		}
		id, _ := m["id"].(string)
		if strings.TrimSpace(id) == "" || len(id) > 200 {
			return nil, fmt.Errorf("%s entry has an invalid id", field)
		}
		if seen[id] {
			return nil, fmt.Errorf("Duplicate %s ids", field)
		}
		seen[id] = true
		entry := map[string]any{"id": id}
		if name, _ := m["name"].(string); strings.TrimSpace(name) != "" {
			entry["name"] = strings.TrimSpace(name)
		}
		for key := range m {
			if key != "id" && key != "name" {
				return nil, fmt.Errorf("%s entry has unknown key %s", field, key)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// PeoplePolicy is the policy part of a kind whose senders have their own identity: allowed people and listed bots.
func PeoplePolicy(raw map[string]any) (Config, error) {
	for key := range raw {
		if key != "allowed" && key != "bots" {
			return nil, fmt.Errorf("unknown policy key %s", key)
		}
	}
	allowed, err := AllowedList(raw["allowed"], "allowed")
	if err != nil {
		return nil, err
	}
	out := Config{"allowed": allowed}
	if raw["bots"] != nil {
		bots, err := AllowedList(raw["bots"], "bots")
		if err != nil {
			return nil, err
		}
		out["bots"] = bots
	}
	return out, nil
}

// Mentions reports whether text @-mentions any of names: "@name" followed by the end, a space or punctuation.
func Mentions(text string, names []string) bool {
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		re := regexp.MustCompile("@" + regexp.QuoteMeta(name) + `(?:$|[\s,，:：.。!！?？)）\]])`)
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

// PeopleFields are the panel fields of a PeoplePolicy part: the people who may originate work and the listed bots,
// each a row of the sender's id in the Source (label idLabel) and an optional name (SA feishu/github policy fields).
func PeopleFields(idLabel Localized) []FieldSpec {
	columns := []Column{{Key: "id", Type: "text", Label: idLabel}, {Key: "name", Type: "text", Optional: true, Label: Localized{Zh: "名字", En: "Name"}}}
	return []FieldSpec{
		{Key: "allowed", Type: "rows", Label: Localized{Zh: "能发起工作的人", En: "People who may originate work"}, Columns: columns},
		{Key: "bots", Type: "rows", Label: Localized{Zh: "机器人名单", En: "Bots"}, Columns: columns,
			Hint: &Localized{Zh: "名单里的机器人发的消息最多是推断的候选，不会是明确请求。", En: "A listed bot's message is at most an inferred candidate, never an explicit request."}},
	}
}

