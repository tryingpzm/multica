package views

import (
	"regexp"
	"sort"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
)

// State is the panel's polled state (SA stateView). A personal agent has no deployment of its own, so setup reports
// no problem, and its Worker runs on Multica runtimes without a slot limit (maxWorkers 0).
func (v *View) State() map[string]any {
	running := v.running()
	return map[string]any{
		"setup":    map[string]any{"problem": nil, "feishuProblem": nil, "accounts": nil},
		"load":     map[string]any{"running": running, "queued": v.queued(), "maxWorkers": 0},
		"masterId": v.a.RouterSessionID, "paused": v.a.Settings.Paused, "enabled": v.a.Enabled, "health": map[string]any{},
	}
}

// running is the open tickets whose worker is at work on its issue.
func (v *View) running() int {
	n := 0
	for _, t := range v.s.Tickets().Open() {
		t := t
		if stage := core.StageOf(&t); stage.Stage == "working" && stage.Action == "" {
			n++
		}
	}
	return n
}

func (v *View) queued() int {
	n := 0
	for _, t := range v.s.Tickets().Open() {
		t := t
		if stage := core.StageOf(&t); stage.Stage == "working" && stage.Action == "queued" {
			n++
		}
	}
	return n
}

// unjudged is the Threads whose latest Decision since since is an undismissed unavailable Eval, neither evaluating nor queued.
func (v *View) unjudged(since string) []struct {
	thread   core.Thread
	decision core.Decision
} {
	seen := map[string]bool{}
	out := []struct {
		thread   core.Thread
		decision core.Decision
	}{}
	for _, d := range v.decisionsOfKinds([]string{"unavailable"}, 200) {
		if d.At < since || seen[d.ThreadKey] {
			continue
		}
		seen[d.ThreadKey] = true
		thread := v.thread(d.ThreadKey)
		if thread == nil || thread.Evaluating != nil || len(v.awaitingEval(*thread)) > 0 || d.Dismissed {
			continue
		}
		list := v.s.Decisions().ByThread(thread.Key)
		if len(list) == 0 || list[len(list)-1].ID != d.ID {
			continue
		}
		out = append(out, struct {
			thread   core.Thread
			decision core.Decision
		}{*thread, d})
	}
	return out
}

func (v *View) sourcesOn() bool {
	for _, inst := range v.a.Registry.Instances() {
		if inst.Enabled && inst.Installed && inst.ID != "panel" {
			return true
		}
	}
	return false
}

// Overview is the panel's first page (SA overview), with the replies waiting for confirmation.
func (v *View) Overview() map[string]any {
	running := v.running()
	since := v.dayAgo()
	now := iso(v.now)
	problems := []any{}
	if !v.sourcesOn() {
		problems = append(problems, map[string]any{"kind": "no-source"})
	}
	// personal-agent anyone who @s the owner may originate work, so an empty allowed list is no problem.
	if v.a.RouterAgentID == "" {
		problems = append(problems, map[string]any{"kind": "no-executor"})
	}
	if v.a.Settings.Paused {
		problems = append(problems, map[string]any{"kind": "executor-paused"})
	}
	// with no default worker, an enabled rule without an executor cannot dispatch.
	if !v.defaultWorker {
		for _, r := range v.a.Settings.Policy.Rules {
			if r.Enabled && r.Target.ID == "" {
				problems = append(problems, map[string]any{"kind": "rule-no-target", "rule": map[string]string{"id": r.ID, "name": r.Name}})
			}
		}
	}
	evaluating := 0
	for _, thread := range v.s.Threads().Active() {
		if thread.Evaluating == nil {
			continue
		}
		evaluating++
		if thread.Evaluating.Deadline < now {
			problems = append(problems, map[string]any{"kind": "eval-overdue", "thread": v.threadSummary(thread)})
		}
	}
	for _, item := range v.unjudged(since) {
		trigger := ""
		if len(item.decision.TriggerIDs) > 0 {
			trigger = item.decision.TriggerIDs[0]
		}
		problems = append(problems, map[string]any{"kind": "eval-unavailable", "decisionId": item.decision.ID,
			"thread": v.threadSummary(item.thread), "reason": reasons(item.decision.Reason), "triggerId": trigger})
	}
	// each Watchdog finding is a problem on the overview.
	for _, f := range v.s.StuckTickets() {
		if t := v.ticket(f.TicketID); t != nil {
			problems = append(problems, map[string]any{"kind": "stuck", "ticket": v.ticketRow(t)})
		}
	}
	open := v.s.Tickets().Open()
	newestFirst(open)
	person, idle := []any{}, 0
	for i := range open {
		t := &open[i]
		switch core.StageOf(t).Stage {
		case "person":
			person = append(person, v.ticketRow(t))
		case "idle":
			idle++
		}
	}
	received, filtered := 0, 0
	for _, s := range v.triggerStats(since) {
		received += s.Received
		filtered += s.Filtered
	}
	judged, accepted := v.flow(since)
	return map[string]any{
		"problems": problems, "person": person, "confirm": v.pending(),
		"executor": map[string]any{"running": running, "queued": v.queued(), "maxWorkers": 0, "idle": idle},
		"flow": map[string]any{"received": received, "filtered": filtered, "triggers": received - filtered, "judged": judged,
			"accepted": accepted, "evaluating": evaluating, "dropped": len(v.outbox("dropped", since, 1000))},
		"policyVersion": policy.PolicyVersion(v.a.Settings),
	}
}

// pending is the replies waiting for their owner to confirm them, oldest first.
func (v *View) pending() []any {
	out := []any{}
	for _, m := range v.outbox("pending_confirm", "1970-01-01T00:00:00Z", 200) {
		item := map[string]any{"id": m.ID, "kind": m.Kind, "channel": m.Channel, "text": m.Text, "at": m.At}
		if m.Error != "" {
			item["error"] = m.Error // the last send failed; confirming sends it again.
		}
		if link := v.channelLink(m.Channel); link.URL != "" || link.Label.Zh != "" {
			item["link"] = link
		}
		v.anchor(item, m.TicketID, m.Channel)
		// whom it answers and where it lands, named rather than by id.
		if to := v.repliesTo(m.Channel, m.At); to != nil {
			item["to"] = map[string]any{"actor": v.actor(to.Actor, to.Human), "preview": preview(to.Text, 80)}
		}
		item["where"] = v.where(m.Channel)
		out = append(out, item)
	}
	return out
}

// repliesTo is the message a reply answers: the Trigger its comment address names, else the newest human Trigger on
// its Thread from before the reply.
func (v *View) repliesTo(channel core.ChannelRef, at string) *core.Trigger {
	list := v.threadTriggers(channel.Thread, 50, "", "", false)
	var last *core.Trigger
	for i := range list {
		t := &list[i]
		if c := channel.Address["commentId"]; c != "" && strings.Contains(t.ID, c) {
			return t
		}
		if t.Human && t.Source != "panel" && (at == "" || t.At <= at) {
			last = t
		}
	}
	return last
}

// where names a reply's destination: its channel, the place (an issue's comments, a channel or chat), and the post.
func (v *View) where(channel core.ChannelRef) map[string]any {
	out := map[string]any{"source": channel.Source}
	link := v.channelLink(channel)
	if link.URL != "" {
		out["url"] = link.URL
	}
	if channel.Source == "multica" {
		if issue := v.issue(channel.Address["issueId"]); issue != nil {
			out["place"], out["url"], out["comment"] = issue.Identifier, issue.URL, channel.Address["commentId"] != ""
		}
		return out
	}
	// the place is the channel's own name; the kind's generic label is already the source.
	generic := v.a.Registry.Link(core.ChannelRef{Source: channel.Source}, v.links).Label.Zh
	if v.names.Place != nil {
		if name := v.names.Place(channel); name != "" {
			out["place"] = name
		}
	}
	if label := link.Label.Zh; out["place"] == nil && label != "" && label != generic && !uuidLike.MatchString(label) && label != channel.Source {
		out["place"] = label
	}
	if th := v.thread(channel.Thread); th != nil {
		if title := v.postSummary(*th); title != "" && title != th.Key && !uuidLike.MatchString(title) {
			out["post"] = title
		}
	}
	return out
}

// postSummary is a post named in at most 20 characters: its label when it has one, else the start of its first message.
func (v *View) postSummary(thread core.Thread) string {
	title := v.threadTitle(thread)
	if title == thread.Key {
		return title
	}
	r := []rune(strings.TrimSpace(title))
	if len(r) > 20 {
		return string(r[:20]) + "…"
	}
	return string(r)
}

var uuidLike = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// anchor links an outbound message to its ticket, or to its Thread when it has none.
func (v *View) anchor(item map[string]any, ticketID string, channel core.ChannelRef) {
	if t := v.ticket(ticketID); ticketID != "" && t != nil {
		item["ticket"] = map[string]string{"id": t.ID, "title": t.Title}
	}
	if v.thread(channel.Thread) != nil {
		item["thread"] = channel.Thread
	}
}

func (v *View) ticketRow(t *core.Ticket) map[string]any {
	out := v.ticketLine(t)
	stage := core.StageOf(t)
	if stage.Stage == "person" && len(t.Channels) > 0 {
		out["reply"] = v.channelLink(t.Channels[0])
	}
	// who asked, what for, and who it went to.
	out["actor"] = v.actor(t.Mandate.Actor, t.Mandate.Basis.Via != "bot")
	out["scope"] = t.Mandate.Scope
	executor := map[string]string{"type": "agent", "id": "", "name": ""}
	if t.Execution != nil && t.Execution.Target.ID != "" {
		executor["type"], executor["id"] = t.Execution.Target.Type, t.Execution.Target.ID
		if v.names.Target != nil {
			executor["name"] = v.names.Target(t.Execution.Target)
		}
	}
	out["executor"] = executor
	if q := waitingQuestion(t); q != nil { //
		out["question"] = q
	}
	if link := v.issue(v.s.TicketIssue(t.ID)); link != nil {
		out["issue"] = link
	}
	// a Watchdog finding marks the row stuck.
	for _, f := range v.s.StuckTickets() {
		if f.TicketID == t.ID {
			out["stuck"] = map[string]string{"kind": f.Kind, "since": f.Since}
		}
	}
	return out
}

func (v *View) sourceHealth(inst sources.Instance) []any {
	out := []any{}
	if k := v.a.Registry.Kind(inst.Kind); k != nil {
		for _, name := range k.Health {
			out = append(out, map[string]any{"name": name, "state": "ready", "detail": ""})
		}
	}
	return out
}

func title(r *sources.Registry, inst sources.Instance) sources.Localized {
	if k := r.Kind(inst.Kind); k != nil {
		return k.Title
	}
	return sources.Localized{Zh: inst.ID, En: inst.ID}
}

// Sources is the Source page (SA sourcesStatus).
func (v *View) Sources() []any {
	since := v.dayAgo()
	stats := v.triggerStats(since)
	dropped := v.dropped()
	out := []any{}
	for _, inst := range v.a.Registry.Instances() {
		state := "ready"
		switch {
		case !inst.Installed:
			state = "missing"
		case !inst.Enabled:
			state = "off"
		}
		filtered := []any{}
		for _, t := range v.filteredTriggers(inst.ID, 5) {
			if t.At < since {
				continue
			}
			item := map[string]any{"id": t.ID, "at": t.At, "actor": v.actor(t.Actor, t.Human), "preview": preview(t.Text, 120)}
			if d := v.s.Triggers().Disposition(t.ID); d != nil && d.Filtered != nil {
				item["reason"] = d.Filtered
			}
			filtered = append(filtered, item)
		}
		mine := []any{}
		for _, item := range dropped {
			if item["channel"].(core.ChannelRef).Source == inst.ID {
				mine = append(mine, item)
			}
		}
		stat := stats[inst.ID]
		out = append(out, map[string]any{"id": inst.ID, "title": title(v.a.Registry, inst), "installed": inst.Installed, "enabled": inst.Enabled,
			"state": state, "health": v.sourceHealth(inst), "stats": stat, "filtered": filtered, "dropped": mine})
	}
	return out
}

// dropped is the replies of the last day their conversation could no longer take.
func (v *View) dropped() []map[string]any {
	out := []map[string]any{}
	for _, m := range v.outbox("dropped", v.dayAgo(), 20) {
		item := map[string]any{"id": m.ID, "kind": m.Kind, "channel": m.Channel, "preview": preview(m.Text, 160),
			"dropped": map[string]string{"at": m.At, "reason": "channelGone"}}
		v.anchor(item, m.TicketID, m.Channel)
		out = append(out, item)
	}
	return out
}

// Threads is the Trigger page's list (SA threadList).
func (v *View) Threads(filter string, offset, limit int) Page {
	since := v.dayAgo()
	open := map[string]bool{}
	for _, t := range v.s.Tickets().Open() {
		open[t.ID] = true
	}
	lastAt := func(t core.Thread) string {
		if t.LastAt != "" {
			return t.LastAt
		}
		return t.CreatedAt
	}
	var candidates []core.Thread
	switch filter {
	case "subscribed":
		candidates = v.subscribedThreads()
	case "evaluating":
		candidates = v.s.Threads().Active()
	default:
		candidates = v.s.Threads().List()
	}
	kept := []core.Thread{}
	for _, t := range candidates {
		ok := false
		switch filter {
		case "recent":
			ok = lastAt(t) >= since
		case "all":
			ok = true
		case "subscribed":
			for _, id := range t.Subscriptions {
				ok = ok || open[id]
			}
		default:
			ok = t.Evaluating != nil || len(v.awaitingEval(t)) > 0
		}
		if ok {
			kept = append(kept, t)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return lastAt(kept[i]) > lastAt(kept[j]) })
	return page(kept, offset, limit, func(t core.Thread) any { return v.threadSummary(t) })
}

// Thread is a Thread's page (SA threadDetail); nil when there is none.
func (v *View) Thread(key, before string, limit int, around string) map[string]any {
	thread := v.thread(key)
	if thread == nil {
		return nil
	}
	var triggers []core.Trigger
	if target := v.s.Triggers().Get(around); around != "" && target != nil {
		triggers = v.threadTriggers(key, limit+1, target.At, target.ID, true)
	} else {
		at, id, _ := strings.Cut(before, "|")
		triggers = v.threadTriggers(key, limit+1, at, id, false)
	}
	decisions := v.s.Decisions().ByThread(key)
	more := len(triggers) > limit
	shown := triggers
	if more {
		shown = triggers[1:]
	}
	out := v.threadSummary(*thread)
	if url, _ := out["url"].(string); url == "" {
		if first := v.firstTrigger(key, false); first != nil && first.Channel != nil {
			if link := v.channelLink(*first.Channel); link.URL != "" {
				out["url"] = link.URL
			}
		}
	}
	context := thread.Context
	if context == nil {
		context = []string{}
	}
	correlated := []any{}
	for _, other := range thread.Correlated {
		name := other
		if t := v.thread(other); t != nil {
			name = v.threadTitle(*t)
		}
		correlated = append(correlated, map[string]string{"key": other, "title": name})
	}
	tickets := []any{}
	for _, id := range thread.Subscriptions {
		if t := v.ticket(id); t != nil {
			tickets = append(tickets, v.ticketLine(t))
		}
	}
	decisionViews := []any{}
	for i := len(decisions) - 1; i >= 0; i-- {
		decisionViews = append(decisionViews, v.decisionView(decisions[i]))
	}
	timeline := []any{}
	for _, t := range shown {
		timeline = append(timeline, v.triggerView(t, decisions))
	}
	out["context"], out["correlated"], out["tickets"], out["decisions"], out["timeline"] = context, correlated, tickets, decisionViews, timeline
	out["earlier"] = nil
	if more && len(shown) > 0 {
		out["earlier"] = shown[0].At + "|" + shown[0].ID
	}
	return out
}

// Search finds Triggers, Threads and Tickets (SA search).
func (v *View) Search(query string) map[string]any {
	value := strings.TrimSpace(query)
	empty := map[string]any{"tickets": []any{}, "items": []any{}, "threads": []any{}}
	if value == "" {
		return empty
	}
	found := v.a.Registry.Find(value)
	terms := append([]string{value}, found.Terms...)
	byID := map[string]core.Trigger{}
	for _, term := range terms {
		for _, t := range v.searchTriggers(term) {
			byID[t.ID] = t
		}
	}
	threads := []any{}
	shown := map[string]bool{}
	for _, key := range found.Threads {
		if t := v.thread(key); t != nil {
			threads = append(threads, v.threadSummary(*t))
			shown[key] = true
		}
	}
	lower := strings.ToLower(value)
	all := v.s.Tickets().List()
	newestFirst(all)
	tickets := []any{}
	for i := range all {
		t := &all[i]
		match := strings.Contains(strings.ToLower(t.Title), lower)
		for _, f := range t.Follows {
			match = match || contains(found.Threads, f.Thread)
		}
		for _, th := range v.threadsOf(t.ID) {
			match = match || contains(found.Threads, th.Key)
		}
		if match && len(tickets) < 10 {
			tickets = append(tickets, v.ticketSummary(t))
		}
	}
	list := []core.Trigger{}
	for _, t := range byID {
		if t.Human || !shown[core.ThreadKey(t.Threads[0])] {
			list = append(list, t)
		}
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].At > list[j].At })
	if len(list) > 20 {
		list = list[:20]
	}
	items := []any{}
	for _, t := range list {
		row := v.triggerView(t, nil)
		row.Text = ""
		if home := v.thread(core.ThreadKey(t.Threads[0])); home != nil {
			name := v.threadTitle(*home)
			row.ThreadTitle = &name
		} else if d := v.s.Triggers().Disposition(t.ID); d != nil && d.Filtered != nil {
			row.Filtered = d.Filtered
		} else {
			row.Filtered = ""
		}
		items = append(items, row)
	}
	return map[string]any{"tickets": tickets, "items": items, "threads": threads}
}

// Decisions is the Eval page's list (SA decisionList).
func (v *View) Decisions(kinds []string, offset, limit int, window bool) map[string]any {
	since := ""
	if window {
		since = v.dayAgo()
	}
	list, more := v.decisions(kinds, since, offset, limit)
	items := []any{}
	for _, d := range list {
		items = append(items, v.decisionView(d))
	}
	var next *int
	if more {
		n := offset + limit
		next = &n
	}
	return map[string]any{"items": items, "next_offset": next}
}

// Tickets is the Execution page's list (SA ticketList).
func (v *View) Tickets(stage string, offset, limit int) Page {
	var list []core.Ticket
	if stage == "ended" {
		list = v.s.Tickets().List()
	} else {
		list = v.s.Tickets().Open()
	}
	kept := []core.Ticket{}
	for i := range list {
		t := list[i]
		if (stage == "open" && core.LiveTicket(t)) || (stage != "open" && core.StageOf(&t).Stage == stage) {
			kept = append(kept, t)
		}
	}
	newestFirst(kept)
	if stage == "open" {
		// what waits on the owner first, then work underway, then what has finished its turn.
		rank := map[string]int{"person": 0, "working": 1, "idle": 2}
		sort.SliceStable(kept, func(i, j int) bool { return rank[core.StageOf(&kept[i]).Stage] < rank[core.StageOf(&kept[j]).Stage] })
	}
	return page(kept, offset, limit, func(t core.Ticket) any { return v.ticketRow(&t) })
}

// waitingQuestion is what a ticket waiting for the owner asks, with a short reason: the attention question,
// or, for a blocked report made without human_action, the summary's last sentence that asks; the reason is the
// summary's first paragraph, without the question.
func waitingQuestion(t *core.Ticket) map[string]string {
	if core.StageOf(t).Stage != "person" {
		return nil
	}
	report := core.LatestReport(t)
	summary := ""
	if report != nil && (report.State == "blocked" || report.State == "failed") {
		summary = strings.TrimSpace(report.Summary)
	}
	question := ""
	if t.Attention != nil {
		question = strings.TrimSpace(t.Attention.Question)
	} else {
		question = askedIn(summary)
	}
	if question == "" {
		return nil
	}
	reason := strings.TrimSpace(strings.SplitN(summary, "\n\n", 2)[0])
	reason = strings.TrimSpace(strings.Replace(reason, question, "", 1))
	if r := []rune(reason); len(r) > 200 {
		reason = string(r[:200]) + "…"
	}
	return map[string]string{"text": question, "reason": reason}
}

// askedIn is the last sentence of text that ends with a question mark.
func askedIn(text string) string {
	runes := []rune(text)
	end := -1
	for i := len(runes) - 1; i >= 0; i-- {
		if runes[i] == '？' || runes[i] == '?' {
			end = i
			break
		}
	}
	if end < 0 {
		return ""
	}
	start := 0
	for i := end - 1; i >= 0; i-- {
		if strings.ContainsRune("。！!\n；;", runes[i]) {
			start = i + 1
			break
		}
	}
	return strings.TrimSpace(string(runes[start : end+1]))
}
