package views

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// Read-only queries of the views over the pa_* tables (SA src/app/store-read.ts).

func (v *View) values(sql string, args ...any) [][]byte {
	rows, err := v.s.Q().Query(v.s.Context(), sql, args...)
	check(err)
	defer rows.Close()
	out := [][]byte{}
	for rows.Next() {
		var data []byte
		check(rows.Scan(&data))
		out = append(out, data)
	}
	check(rows.Err())
	return out
}

func decode[T any](rows [][]byte) []T {
	out := make([]T, 0, len(rows))
	for _, data := range rows {
		var t T
		check(json.Unmarshal(data, &t))
		out = append(out, t)
	}
	return out
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

type stat struct {
	Received int    `json:"received"`
	Filtered int    `json:"filtered"`
	LastAt   string `json:"lastAt"`
}

// triggerStats counts each Source's Triggers since since, with its latest arrival of all time.
func (v *View) triggerStats(since string) map[string]stat {
	rows, err := v.s.Q().Query(v.s.Context(), `SELECT value->>'source', COALESCE(MAX(at), ''), COUNT(*) FILTER (WHERE at >= $2),
		COUNT(*) FILTER (WHERE at >= $2 AND disposition ? 'filtered') FROM pa_trigger WHERE agent_id = $1 GROUP BY 1`, v.id, since)
	check(err)
	defer rows.Close()
	out := map[string]stat{}
	for rows.Next() {
		var source string
		var s stat
		check(rows.Scan(&source, &s.LastAt, &s.Received, &s.Filtered))
		out[source] = s
	}
	return out
}

// flow counts the Triggers since since that passed their Source and reached a Decision, and the accepts they made.
func (v *View) flow(since string) (judged, accepted int) {
	check(v.s.Q().QueryRow(v.s.Context(), `SELECT COUNT(DISTINCT t.id) FROM pa_decision d
		CROSS JOIN LATERAL jsonb_array_elements_text(d.value->'triggerIds') j(id)
		JOIN pa_trigger t ON t.agent_id = d.agent_id AND t.id = j.id
		WHERE d.agent_id = $1 AND t.at >= $2 AND NOT (COALESCE(t.disposition, '{}'::jsonb) ? 'filtered')`, v.id, since).Scan(&judged))
	check(v.s.Q().QueryRow(v.s.Context(), `SELECT COUNT(DISTINCT d.id) FROM pa_decision d
		CROSS JOIN LATERAL jsonb_array_elements_text(d.value->'triggerIds') j(id)
		JOIN pa_trigger t ON t.agent_id = d.agent_id AND t.id = j.id
		WHERE d.agent_id = $1 AND d.kind IN ('accept', 'reopen') AND t.at >= $2`, v.id, since).Scan(&accepted))
	return
}

// filteredTriggers is a Source's newest filtered Triggers.
func (v *View) filteredTriggers(source string, limit int) []core.Trigger {
	return decode[core.Trigger](v.values(`SELECT value FROM pa_trigger WHERE agent_id = $1 AND value->>'source' = $2
		AND COALESCE(disposition, '{}'::jsonb) ? 'filtered' ORDER BY at DESC LIMIT $3`, v.id, source, limit))
}

// threadTriggers is the latest limit Triggers on a Thread, oldest first, before (or through) a place.
func (v *View) threadTriggers(key string, limit int, at, id string, through bool) []core.Trigger {
	place, args := "", []any{v.id, key, limit}
	if at != "" || id != "" {
		op := "<"
		if through {
			op = "<="
		}
		place = " AND (x.at, x.trigger_id) " + op + " ($4, $5)"
		args = append(args, at, id)
	}
	out := decode[core.Trigger](v.values(`SELECT t.value FROM pa_trigger_thread x JOIN pa_trigger t ON t.agent_id = x.agent_id AND t.id = x.trigger_id
		WHERE x.agent_id = $1 AND x.thread_key = $2`+place+` ORDER BY x.at DESC, x.trigger_id DESC LIMIT $3`, args...))
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// firstTrigger is the oldest Trigger on a Thread, a person's when human.
func (v *View) firstTrigger(key string, human bool) *core.Trigger {
	who := ""
	if human {
		who = " AND x.human"
	}
	rows := decode[core.Trigger](v.values(`SELECT t.value FROM pa_trigger_thread x JOIN pa_trigger t ON t.agent_id = x.agent_id AND t.id = x.trigger_id
		WHERE x.agent_id = $1 AND x.thread_key = $2`+who+` ORDER BY x.at, t.seq LIMIT 1`, v.id, key))
	if len(rows) == 0 {
		return nil
	}
	return &rows[0]
}

func like(query string) string {
	return strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(query)
}

// searchTriggers finds Triggers by id, URL prefix, message id or text.
func (v *View) searchTriggers(query string) []core.Trigger {
	return decode[core.Trigger](v.values(`SELECT value FROM pa_trigger WHERE agent_id = $1 AND (id = $2 OR value->>'url' LIKE $3
		OR value->'meta'->>'messageId' = $2 OR value->>'text' LIKE $4) ORDER BY at DESC LIMIT 20`, v.id, query, like(query)+"%", "%"+like(query)+"%"))
}

// decisions pages the Decisions of kinds (all when empty) whose Triggers arrived since since, newest first.
func (v *View) decisions(kinds []string, since string, offset, limit int) ([]core.Decision, bool) {
	rows := decode[core.Decision](v.values(`SELECT d.value FROM pa_decision d WHERE d.agent_id = $1
		AND (cardinality($2::text[]) = 0 OR d.kind = ANY($2)) AND ($3 = '' OR EXISTS (SELECT 1 FROM jsonb_array_elements_text(d.value->'triggerIds') j(id)
		JOIN pa_trigger t ON t.agent_id = d.agent_id AND t.id = j.id WHERE t.at >= $3)) ORDER BY d.seq DESC LIMIT $4 OFFSET $5`,
		v.id, kinds, since, limit+1, offset))
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

func (v *View) decisionsOfKinds(kinds []string, limit int) []core.Decision {
	return decode[core.Decision](v.values(`SELECT value FROM pa_decision WHERE agent_id = $1 AND kind = ANY($2) ORDER BY seq DESC LIMIT $3`, v.id, kinds, limit))
}

func (v *View) subscribedThreads() []core.Thread {
	return decode[core.Thread](v.values(`SELECT value FROM pa_thread t WHERE agent_id = $1 AND EXISTS
		(SELECT 1 FROM pa_subscription s WHERE s.agent_id = t.agent_id AND s.thread_key = t.key) ORDER BY seq`, v.id))
}

// outboxRow is a pa_outbox message with when its status last changed.
type outboxRow struct {
	core.OutboundMessage
	At    string
	Error string
}

func (v *View) outbox(status string, since string, limit int) []outboxRow {
	rows, err := v.s.Q().Query(v.s.Context(), `SELECT value, updated_at, last_error FROM pa_outbox WHERE agent_id = $1 AND status = $2 AND updated_at >= $3::timestamptz
		ORDER BY seq LIMIT $4`, v.id, status, since, limit)
	check(err)
	defer rows.Close()
	out := []outboxRow{}
	for rows.Next() {
		var data []byte
		var at time.Time
		var failed string
		check(rows.Scan(&data, &at, &failed))
		var m core.OutboundMessage
		check(json.Unmarshal(data, &m))
		out = append(out, outboxRow{OutboundMessage: m, At: iso(at), Error: failed})
	}
	return out
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }
