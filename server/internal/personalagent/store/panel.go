package store

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
)

// The panel's writes of a personal agent's settings. Every block shares the agent's settings_version: a
// save names the version it was edited from and bumps it, so a stale panel's save is refused (SA SettingsConflict).

// ErrSettingsChanged refuses a save edited from an older settings version.
var ErrSettingsChanged = errors.New("Settings changed. Reload before saving.")

// Revision is the agent's settings version.
func (s *Store) Revision() int {
	var v int
	check(s.q().QueryRow(s.ctx, "SELECT settings_version FROM personal_agent WHERE id = $1", s.agentID).Scan(&v))
	return v
}

// claim bumps the settings version from revision, or panics with ErrSettingsChanged; call it inside Atomic.
func (s *Store) claim(revision int) int {
	var next int
	err := s.q().QueryRow(s.ctx, `UPDATE personal_agent SET settings_version = settings_version + 1, updated_at = now()
		WHERE id = $1 AND settings_version = $2 RETURNING settings_version`, s.agentID, revision).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		panic(ErrSettingsChanged)
	}
	check(err)
	return next
}

// SavePolicy stores the rules, each Source's allowed and bot lists, and the admission cells of its existing scope rows.
func (s *Store) SavePolicy(revision int, rules []policy.Rule, parts map[string]map[string]any) int {
	next := s.claim(revision)
	data, _ := json.Marshal(rules)
	_, err := s.q().Exec(s.ctx, "UPDATE personal_agent SET rules = $2 WHERE id = $1", s.agentID, data)
	check(err)
	_, err = s.q().Exec(s.ctx, "DELETE FROM pa_allowed WHERE agent_id = $1", s.agentID)
	check(err)
	for source, part := range parts {
		for list, kind := range map[string]string{"allowed": "person", "bots": "bot"} {
			entries, _ := part[list].([]any)
			for _, e := range entries {
				m, _ := e.(map[string]any)
				id, _ := m["id"].(string)
				name, _ := m["name"].(string)
				_, err := s.q().Exec(s.ctx, `INSERT INTO pa_allowed (agent_id, source, actor_id, name, kind) VALUES ($1, $2, $3, $4, $5)
					ON CONFLICT DO NOTHING`, s.agentID, source, id, name, kind)
				check(err)
			}
		}
		table, _ := part["admission"].(map[string]any)
		_, err := s.q().Exec(s.ctx, "UPDATE pa_source_scope SET admission = '{}'::jsonb WHERE agent_id = $1 AND source = $2", s.agentID, source)
		check(err)
		for row, cells := range table {
			data, _ := json.Marshal(cells)
			_, err := s.q().Exec(s.ctx, "UPDATE pa_source_scope SET admission = $4 WHERE agent_id = $1 AND source = $2 AND row_key = $3",
				s.agentID, source, row, data)
			check(err)
		}
	}
	return next
}

// RuleOrigins is where each rule copied from the Router template came from.
func (s *Store) RuleOrigins() map[string]policy.RuleOrigin {
	var data []byte
	check(s.q().QueryRow(s.ctx, "SELECT rule_origins FROM personal_agent WHERE id = $1", s.agentID).Scan(&data))
	origins := map[string]policy.RuleOrigin{}
	_ = json.Unmarshal(data, &origins)
	return origins
}

// SyncRules stores rules brought in line with the Router template and their origins, bumping the settings version so
// an open panel reloads before saving over them.
func (s *Store) SyncRules(rules []policy.Rule, origins map[string]policy.RuleOrigin) {
	data, _ := json.Marshal(rules)
	o, _ := json.Marshal(origins)
	_, err := s.q().Exec(s.ctx, `UPDATE personal_agent SET rules = $2, rule_origins = $3, settings_version = settings_version + 1,
		updated_at = now() WHERE id = $1`, s.agentID, data, o)
	check(err)
}

// SaveEval stores the accept mode and the Eval timeout.
func (s *Store) SaveEval(revision int, mode string, timeoutSeconds int) int {
	next := s.claim(revision)
	_, err := s.q().Exec(s.ctx, "UPDATE personal_agent SET mode = $2, eval_timeout_seconds = $3 WHERE id = $1", s.agentID, mode, timeoutSeconds)
	check(err)
	return next
}

// SetLanguage stores the language the Router writes in; call it with a save that claimed the revision.
func (s *Store) SetLanguage(language string) {
	_, err := s.q().Exec(s.ctx, "UPDATE personal_agent SET language = $2 WHERE id = $1", s.agentID, language)
	check(err)
}

// SaveExecutor stores the Router agent; an empty id unsets it.
func (s *Store) SaveExecutor(revision int, routerAgentID string) int {
	next := s.claim(revision)
	_, err := s.q().Exec(s.ctx, `UPDATE personal_agent SET router_agent_id = NULLIF($2, '')::uuid,
		router_session_id = CASE WHEN router_agent_id IS DISTINCT FROM NULLIF($2, '')::uuid THEN NULL ELSE router_session_id END
		WHERE id = $1`, s.agentID, routerAgentID)
	check(err)
	return next
}

// EnsureAllowed puts a person on a Source's allowed list unless they are on it already.
func (s *Store) EnsureAllowed(source, id, name string) {
	_, err := s.q().Exec(s.ctx, `INSERT INTO pa_allowed (agent_id, source, actor_id, name, kind) VALUES ($1, $2, $3, $4, 'person')
		ON CONFLICT DO NOTHING`, s.agentID, source, id, name)
	check(err)
}

// RemoveAllowed takes a person off a Source's allowed list.
func (s *Store) RemoveAllowed(source, id string) {
	_, err := s.q().Exec(s.ctx, "DELETE FROM pa_allowed WHERE agent_id = $1 AND source = $2 AND kind = 'person' AND actor_id = $3", s.agentID, source, id)
	check(err)
}

// FillRouter sets the Router agent when it is not set yet; a set one is never replaced.
func (s *Store) FillRouter(routerAgentID string) {
	_, err := s.q().Exec(s.ctx, `UPDATE personal_agent SET router_agent_id = NULLIF($2, '')::uuid, settings_version = settings_version + 1,
		updated_at = now() WHERE id = $1 AND router_agent_id IS NULL AND $2 <> ''`, s.agentID, routerAgentID)
	check(err)
}

// SaveSource stores a Source's switch and configuration: its scope rows in pa_source_scope (keeping the admission cells
// of the rows that stay) and the rest in personal_agent.sources.
func (s *Store) SaveSource(revision int, id string, enabled bool, config sources.Config) int {
	next := s.claim(revision)
	rows := sources.Rows(config)
	keep := []string{}
	for _, r := range rows {
		keep = append(keep, r.Row)
		_, err := s.q().Exec(s.ctx, `INSERT INTO pa_source_scope (agent_id, source, row_key, listen) VALUES ($1, $2, $3, $4)
			ON CONFLICT (agent_id, source, row_key) DO UPDATE SET listen = EXCLUDED.listen`, s.agentID, id, r.Row, r.Listen)
		check(err)
	}
	_, err := s.q().Exec(s.ctx, "DELETE FROM pa_source_scope WHERE agent_id = $1 AND source = $2 AND NOT (row_key = ANY($3))", s.agentID, id, keep)
	check(err)
	rest := map[string]any{}
	for k, v := range config {
		if k != "rows" {
			rest[k] = v
		}
	}
	data, _ := json.Marshal(map[string]any{"enabled": enabled, "config": rest})
	_, err = s.q().Exec(s.ctx, "UPDATE personal_agent SET sources = jsonb_set(sources, ARRAY[$2], $3::jsonb) WHERE id = $1", s.agentID, id, data)
	check(err)
	return next
}

// SetEnabled switches the personal agent on or off.
func (s *Store) SetEnabled(enabled bool) {
	_, err := s.q().Exec(s.ctx, "UPDATE personal_agent SET enabled = $2, updated_at = now() WHERE id = $1", s.agentID, enabled)
	check(err)
}

// Labels are the names of the groups or channels a Source's rows can choose from, by id.
func (s *Store) Labels(source string) map[string]string {
	var data []byte
	check(s.q().QueryRow(s.ctx, "SELECT COALESCE(labels -> $2, '{}'::jsonb) FROM personal_agent WHERE id = $1", s.agentID, source).Scan(&data))
	out := map[string]string{}
	_ = json.Unmarshal(data, &out)
	return out
}

// HasLabels reports whether a Source's labels were ever stored, which marks it as provisioned.
func (s *Store) HasLabels(source string) bool {
	var ok bool
	check(s.q().QueryRow(s.ctx, "SELECT COALESCE(labels ? $2, false) FROM personal_agent WHERE id = $1", s.agentID, source).Scan(&ok))
	return ok
}

// SetLabels replaces a Source's labels.
func (s *Store) SetLabels(source string, labels map[string]string) {
	data, _ := json.Marshal(labels)
	_, err := s.q().Exec(s.ctx, "UPDATE personal_agent SET labels = jsonb_set(labels, ARRAY[$2], $3::jsonb) WHERE id = $1", s.agentID, source, data)
	check(err)
}

// SetPaused pauses or resumes processing.
func (s *Store) SetPaused(paused bool) {
	_, err := s.q().Exec(s.ctx, "UPDATE personal_agent SET paused = $2, updated_at = now() WHERE id = $1", s.agentID, paused)
	check(err)
}

// AddAnnotation records a person's annotation on a ticket or Thread and returns its id.
func (s *Store) AddAnnotation(ticketID, threadKey, authorID, text string) string {
	var id string
	check(s.q().QueryRow(s.ctx, `INSERT INTO pa_annotation (agent_id, ticket_id, thread_key, author_id, text) VALUES ($1, NULLIF($2, ''), $3, $4, $5)
		RETURNING id::text`, s.agentID, ticketID, threadKey, authorID, text).Scan(&id))
	return id
}

// SetAnnotationTrigger records the Trigger an annotation became.
func (s *Store) SetAnnotationTrigger(id, triggerID string) {
	_, err := s.q().Exec(s.ctx, "UPDATE pa_annotation SET trigger_id = $3 WHERE agent_id = $1 AND id = $2::uuid", s.agentID, id, triggerID)
	check(err)
}

// State is a Source's stored polling value under key (SA ctx.state.get); empty when none.
func (s *Store) State(key string) string {
	var v string
	check(s.q().QueryRow(s.ctx, "SELECT COALESCE(source_state ->> $2, '') FROM personal_agent WHERE id = $1", s.agentID, key).Scan(&v))
	return v
}

// SetState stores a Source's polling value under key.
func (s *Store) SetState(key, value string) {
	_, err := s.q().Exec(s.ctx, "UPDATE personal_agent SET source_state = jsonb_set(source_state, ARRAY[$2], to_jsonb($3::text)) WHERE id = $1", s.agentID, key, value)
	check(err)
}
