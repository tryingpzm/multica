package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

// Agent is a personal_agent row.
type Agent struct {
	ID          string
	WorkspaceID string
	UserID      string
	Enabled     bool
	Mode        string
	// RouterAgentID is the Multica agent whose chat session is the Router; its runtime decides where it runs. A ticket's
	// issue goes to the agent of the rule it was accepted under, or the deployment's default.
	// RouterSessionID is the Router's chat session once created.
	RouterAgentID   string
	RouterSessionID string
}

const agentColumns = "id::text, workspace_id::text, user_id::text, enabled, mode, COALESCE(router_agent_id::text, ''), COALESCE(router_session_id::text, '')"

func (a *Agent) fields() []any {
	return []any{&a.ID, &a.WorkspaceID, &a.UserID, &a.Enabled, &a.Mode, &a.RouterAgentID, &a.RouterSessionID}
}

// CreateAgent inserts a personal agent for a user, disabled and in observe mode, or returns the existing one.
func CreateAgent(s *Store, workspaceID, userID string) Agent {
	var a Agent
	check(s.q().QueryRow(s.ctx, `INSERT INTO personal_agent (workspace_id, user_id) VALUES ($1, $2)
		ON CONFLICT (workspace_id, user_id) DO UPDATE SET updated_at = personal_agent.updated_at
		RETURNING `+agentColumns, workspaceID, userID).Scan(a.fields()...))
	return a
}

// EnabledAgents lists the enabled personal agents, those of one workspace when workspaceID is set.
func EnabledAgents(ctx context.Context, pool *pgxpool.Pool, workspaceID string) ([]Agent, error) {
	rows, err := pool.Query(ctx, `SELECT `+agentColumns+` FROM personal_agent
		WHERE enabled AND ($1 = '' OR workspace_id::text = $1) ORDER BY created_at, id`, workspaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agent{}
	for rows.Next() {
		var a Agent
		if err := rows.Scan(a.fields()...); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Settings assembles the agent's policy.Settings from personal_agent, pa_source_scope and pa_allowed. A Source with
// scope rows gets them as its config "rows" ([{row, listen}]) and their admission cells as its admission table.
func (s *Store) Settings() (policy.Settings, error) {
	var mode string
	var paused bool
	var timeout int
	var language string
	var rules, stored []byte
	if err := s.q().QueryRow(s.ctx, "SELECT mode, paused, eval_timeout_seconds, language, rules, sources FROM personal_agent WHERE id = $1", s.agentID).Scan(&mode, &paused, &timeout, &language, &rules, &stored); err != nil {
		return policy.Settings{}, err
	}
	raw := map[string]any{"personal": true, "paused": paused, "eval": map[string]any{"mode": mode, "timeoutSeconds": timeout, "language": language}}
	parts := map[string]map[string]any{}
	// personal_agent.sources holds each Source's switch and the configuration that is not scope rows.
	sources := map[string]any{}
	_ = json.Unmarshal(stored, &sources)
	part := func(source string) map[string]any {
		if parts[source] == nil {
			parts[source] = map[string]any{"allowed": []any{}, "admission": map[string]any{}}
		}
		return parts[source]
	}
	rows, err := s.q().Query(s.ctx, "SELECT source, row_key, listen, admission FROM pa_source_scope WHERE agent_id = $1 ORDER BY source, row_key", s.agentID)
	if err != nil {
		return policy.Settings{}, err
	}
	for rows.Next() {
		var source, row, listen string
		var admission []byte
		if err := rows.Scan(&source, &row, &listen, &admission); err != nil {
			rows.Close()
			return policy.Settings{}, err
		}
		var cells map[string]any
		_ = json.Unmarshal(admission, &cells)
		if len(cells) > 0 {
			part(source)["admission"].(map[string]any)[row] = cells
		}
		inst, _ := sources[source].(map[string]any)
		if inst == nil {
			inst = map[string]any{"enabled": true}
			sources[source] = inst
		}
		cfg, _ := inst["config"].(map[string]any)
		if cfg == nil {
			cfg = map[string]any{}
			inst["config"] = cfg
		}
		list, _ := cfg["rows"].([]any)
		cfg["rows"] = append(list, map[string]any{"row": row, "listen": listen})
	}
	rows.Close()
	allowed, err := s.q().Query(s.ctx, "SELECT source, actor_id, name, kind FROM pa_allowed WHERE agent_id = $1 ORDER BY source, kind, actor_id", s.agentID)
	if err != nil {
		return policy.Settings{}, err
	}
	for allowed.Next() {
		var source, id, name, kind string
		if err := allowed.Scan(&source, &id, &name, &kind); err != nil {
			allowed.Close()
			return policy.Settings{}, err
		}
		list := "allowed"
		if kind == "bot" {
			list = "bots"
		}
		entry := map[string]any{"id": id}
		if name != "" {
			entry["name"] = name
		}
		p := part(source)
		existing, _ := p[list].([]any)
		p[list] = append(existing, entry)
	}
	allowed.Close()
	policyRaw := map[string]any{"sources": parts}
	if rules != nil {
		var r any
		_ = json.Unmarshal(rules, &r)
		policyRaw["rules"] = r
	}
	raw["policy"] = policyRaw
	raw["sources"] = sources
	data, _ := json.Marshal(raw)
	return policy.ParseSettings(data)
}
