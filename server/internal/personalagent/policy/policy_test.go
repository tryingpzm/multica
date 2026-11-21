package policy_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

// Translated from dsh-superagent-prototype tests/policy.spec.ts. The Feishu and GitHub Source scopes are faked at the
// Source boundary with SA's scope rules (sources/feishu.ts, sources/github.ts, registry.ts); the Source plugins themselves
// and settings migration belong to later stages.

const (
	MEMBER   = "ou_alice"
	CHAT     = "oc_team"
	OBSERVED = "oc_1111111111111111111111111111"
)

type fakeSources struct{ settings policy.Settings }

func (f fakeSources) Scope(source string, meta map[string]any) core.ScopeResult {
	inst, ok := f.settings.Sources[source]
	if ok && !inst.Enabled {
		return core.ScopeResult{Reason: "source_disabled"}
	}
	cfg := map[string]any{}
	if ok {
		cfg = inst.Config
	}
	row := func(s string) *string { return &s }
	switch source {
	case "feishu":
		if meta["direct"] == true {
			if cfg["directMessages"] == true {
				return core.ScopeResult{Listened: true, Row: row("direct")}
			}
			return core.ScopeResult{Reason: "direct_messages_not_allowed"}
		}
		chats, _ := cfg["chats"].([]any)
		for _, c := range chats {
			chat := c.(map[string]any)
			if chat["chatId"] == meta["chatId"] {
				return core.ScopeResult{Listened: true, Observed: chat["listen"] == "all", Row: row(chat["chatId"].(string))}
			}
		}
		if cfg["otherChats"] == "mentions" {
			return core.ScopeResult{Listened: true, Row: row("other")}
		}
		return core.ScopeResult{Reason: "chat_not_allowed"}
	case "github":
		observed := map[string]string{"issues": "observeIssues", "pulls": "observePulls", "comments": "observeComments", "ci": "observeCi"}
		name := ""
		switch {
		case meta["ci"] == true:
			if _, ok := meta["branch"].(string); ok {
				name = "ci"
			}
		case meta["kind"] == "issue":
			name = "issues"
		case meta["kind"] == "pull":
			name = "pulls"
		case meta["kind"] == "conversation" || meta["kind"] == "inline" || meta["kind"] == "review":
			name = "comments"
		}
		if name == "" {
			return core.ScopeResult{Listened: true}
		}
		return core.ScopeResult{Listened: true, Observed: cfg[observed[name]] == true, Row: row(name)}
	}
	return core.ScopeResult{Listened: true}
}

func scopeOf(s policy.Settings) core.SourceScope { return fakeSources{s} }

// settings builds Settings from a JSON-ish literal by way of ParseSettings, as SA does with SettingsSchema.parse.
func settings(t *testing.T, v any) policy.Settings {
	t.Helper()
	data, _ := json.Marshal(v)
	s, err := policy.ParseSettings(data)
	if err != nil {
		t.Fatalf("settings: %v", err)
	}
	return s
}

type m = map[string]any

func feishuSource(chats []m, direct bool) m {
	cs := []any{}
	for _, c := range chats {
		cs = append(cs, c)
	}
	return m{"enabled": true, "config": m{"chats": cs, "directMessages": direct}}
}

func baseSettings(t *testing.T) policy.Settings {
	return settings(t, m{
		"policy":  m{"sources": m{"feishu": m{"allowed": []m{{"id": MEMBER, "name": "Alice"}}}, "github": m{"allowed": []m{{"id": "Alice"}}}}},
		"eval":    m{"mode": "auto"},
		"sources": m{"feishu": feishuSource([]m{{"chatId": CHAT, "listen": "mentions"}, {"chatId": OBSERVED, "listen": "all"}}, true)},
	})
}

func pol(s policy.Settings) *policy.Policy { return policy.SettingsPolicy(s, scopeOf(s)) }

func thread(subs ...string) core.Thread {
	return core.Thread{Key: "feishu:x", Ref: core.ThreadRef{Source: "feishu", ID: "x"}, Subscriptions: subs}
}

type tOpt func(*core.Trigger)

func feishu(opts ...tOpt) core.Trigger {
	t := core.Trigger{ID: "t", Source: "feishu", Threads: []core.ThreadRef{{Source: "feishu", ID: "x"}}, Actor: core.ActorRef{Source: "feishu", ID: MEMBER},
		Human: true, Text: "hi", Addressed: "mention", Meta: m{"chatId": CHAT, "direct": false, "mentioned": true}}
	for _, o := range opts {
		o(&t)
	}
	return t
}
func from(id string) tOpt     { return func(t *core.Trigger) { t.Actor.ID = id } }
func metaOf(v m) tOpt         { return func(t *core.Trigger) { t.Meta = v } }
func addressed(a string) tOpt { return func(t *core.Trigger) { t.Addressed = a } }
func machine() tOpt           { return func(t *core.Trigger) { t.Human = false } }
func unaddressed(opts ...tOpt) core.Trigger {
	return feishu(append(opts, addressed(""))...)
}
func github(login, addr string) core.Trigger {
	return core.Trigger{ID: "g", Source: "github", Threads: []core.ThreadRef{{Source: "github", ID: "1"}}, Actor: core.ActorRef{Source: "github", ID: login},
		Human: true, Text: "x", Addressed: addr, Meta: m{"number": 1, "isPr": true}}
}

var endedTicket = core.Ticket{ID: "ticket", Ended: &core.Ended{By: "executor", Reason: "closed", Outcome: "closed"}}

func direct() m             { return m{"chatId": "p2p", "direct": true, "mentioned": false} }
func quietIn(chat string) m { return m{"chatId": chat, "direct": false, "mentioned": false} }

type want struct {
	admissible bool
	reason     string
	strength   string
	basis      map[string]any
}

func gateIs(t *testing.T, got core.GateResult, w want) {
	t.Helper()
	if got.Admissible != w.admissible || (w.reason != "" && got.Reason != w.reason) || (w.strength != "" && got.Strength != w.strength) {
		t.Fatalf("gate: got %+v, want %+v", got, w)
	}
	if !w.admissible && w.reason != "" && got.Reason != w.reason {
		t.Fatalf("reason: got %s want %s", got.Reason, w.reason)
	}
	if w.basis != nil {
		data, _ := json.Marshal(got.Basis)
		var b map[string]any
		_ = json.Unmarshal(data, &b)
		if !subset(w.basis, b) {
			t.Fatalf("basis: got %s, want subset %v", data, w.basis)
		}
	}
}

func subset(want, got map[string]any) bool {
	for k, v := range want {
		if wm, ok := v.(map[string]any); ok {
			gm, ok := got[k].(map[string]any)
			if !ok || !subset(wm, gm) {
				return false
			}
			continue
		}
		if fmt.Sprint(got[k]) != fmt.Sprint(v) {
			return false
		}
	}
	return true
}

func TestGateExplicitWhenAllowedSenderAddressed(t *testing.T) {
	p := pol(baseSettings(t))
	gateIs(t, p.Gate(feishu(), thread(), nil), want{true, "", "explicit", m{"via": "addressed", "source": "feishu", "sender": "Alice", "addressed": "mention", "scope": m{"chatId": CHAT}}})
	gateIs(t, p.Gate(feishu(addressed("direct"), metaOf(direct())), thread(), nil), want{true, "", "explicit", m{"addressed": "direct"}})
	// GitHub logins compare without case; the entry's id names the sender when it has no display name.
	gateIs(t, p.Gate(github("alice", "mention"), thread(), nil), want{true, "", "explicit", m{"source": "github", "sender": "Alice"}})
}

func TestGateChecksEachSourcesOwnList(t *testing.T) {
	base := baseSettings(t)
	gateIs(t, pol(base).Gate(github(MEMBER, "mention"), thread(), nil), want{false, "not_a_member", "", nil})
	if a := policy.AllowedSender(base, core.ActorRef{Source: "github", ID: "ALICE"}); a == nil || *a != (policy.Allowed{ID: "Alice"}) {
		t.Fatalf("allowedSender: %+v", a)
	}
	if policy.AllowedSender(base, core.ActorRef{Source: "feishu", ID: "Alice"}) != nil {
		t.Fatal("feishu Alice allowed")
	}
	if !policy.AnyoneAllowed(base) || policy.AnyoneAllowed(settings(t, m{})) {
		t.Fatal("anyoneAllowed")
	}
}

func TestGateFollowupsOnThreadsWithTickets(t *testing.T) {
	p := pol(baseSettings(t))
	quiet := metaOf(quietIn(CHAT))
	gateIs(t, p.Gate(unaddressed(quiet, from("ou_guest")), thread("ticket"), nil), want{true, "", "inferred", m{"via": "followup"}})
	gateIs(t, p.Gate(unaddressed(quiet), thread("ticket"), nil), want{true, "", "explicit", m{"via": "followup", "sender": "Alice"}})
	gateIs(t, p.Gate(github("someone", ""), thread(), []core.Ticket{endedTicket}), want{true, "", "inferred", nil})
	ctx := thread("ticket")
	ctx.Context = []string{"ticket"}
	if p.Gate(unaddressed(quiet), ctx, []core.Ticket{endedTicket}).Admissible {
		t.Fatal("context thread lent a follow-up")
	}
}

func TestGateInfersObservedMessagesUnlessEvalOff(t *testing.T) {
	base := baseSettings(t)
	observed := unaddressed(metaOf(quietIn(OBSERVED)))
	gateIs(t, pol(base).Gate(observed, thread(), nil), want{true, "", "inferred", m{"via": "admission", "observation": m{"mode": "auto"}}})
	off := base
	off.Eval.Mode = "off"
	gateIs(t, pol(off).Gate(observed, thread(), nil), want{false, "no_mention", "", nil})
}

func TestGateNoDirectMessageFromUnlistedSender(t *testing.T) {
	gateIs(t, pol(baseSettings(t)).Gate(feishu(from("ou_guest"), addressed("direct"), metaOf(direct())), thread("ticket"), nil), want{false, "not_a_member", "", nil})
}

func TestGateRejectsAutomatedUnknownAndUnaddressed(t *testing.T) {
	p := pol(baseSettings(t))
	gateIs(t, p.Gate(feishu(machine()), thread(), nil), want{false, "not_human", "", nil})
	gateIs(t, p.Gate(feishu(from("ou_guest")), thread(), nil), want{false, "not_a_member", "", nil})
	gateIs(t, p.Gate(unaddressed(metaOf(quietIn(CHAT))), thread(), nil), want{false, "no_mention", "", nil})
}

const STRANGER = "ou_stranger"

func withTable(t *testing.T, admission m, config m, mode string) policy.Settings {
	if mode == "" {
		mode = "auto"
	}
	cfg := m{"chats": []any{m{"chatId": CHAT, "listen": "mentions"}, m{"chatId": OBSERVED, "listen": "all"}}, "directMessages": true}
	for k, v := range config {
		cfg[k] = v
	}
	return settings(t, m{
		"eval":    m{"mode": mode},
		"sources": m{"feishu": m{"enabled": true, "config": cfg}},
		"policy":  m{"sources": m{"feishu": m{"allowed": []m{{"id": MEMBER, "name": "Alice"}}, "admission": admission}, "github": m{"allowed": []m{{"id": "Alice"}}}}},
	})
}

func inChat(chat, sender string, addr bool) core.Trigger {
	tr := feishu(from(sender), metaOf(m{"chatId": chat, "direct": false, "mentioned": addr}))
	if !addr {
		tr.Addressed = ""
	}
	return tr
}

func TestAdmissionTableUnsetReproducesBehaviourBefore(t *testing.T) {
	p := pol(baseSettings(t))
	gateIs(t, p.Gate(inChat(OBSERVED, MEMBER, false), thread(), nil), want{true, "", "inferred", m{"via": "admission", "row": OBSERVED, "cell": "listedUnaddressed", "observation": m{"mode": "auto"}}})
	gateIs(t, p.Gate(inChat(CHAT, STRANGER, true), thread(), nil), want{false, "not_a_member", "", nil})
	gateIs(t, p.Gate(feishu(from(STRANGER), addressed("direct"), metaOf(direct())), thread(), nil), want{false, "not_a_member", "", nil})
	gateIs(t, pol(withTable(t, m{CHAT: m{"unlistedAddressed": "ignore"}}, nil, "")).Gate(inChat(CHAT, MEMBER, true), thread(), nil), want{true, "", "explicit", nil})
}

func TestAdmissionTableUnlistedMention(t *testing.T) {
	explicit := pol(withTable(t, m{CHAT: m{"unlistedAddressed": "explicit"}}, nil, "")).Gate(inChat(CHAT, STRANGER, true), thread(), nil)
	row := CHAT
	if !reflect.DeepEqual(explicit, core.GateResult{Admissible: true, Strength: "explicit", Basis: core.MandateBasis{Source: "feishu",
		Scope: m{"chatId": CHAT, "direct": false, "mentioned": true}, Row: &row, Via: "admission", Cell: "unlistedAddressed", Addressed: "mention"}}) {
		t.Fatalf("explicit: %+v", explicit)
	}
	gateIs(t, pol(withTable(t, m{CHAT: m{"unlistedAddressed": "inferred"}}, nil, "")).Gate(inChat(CHAT, STRANGER, true), thread(), nil), want{true, "", "inferred", m{"cell": "unlistedAddressed"}})
	gateIs(t, pol(withTable(t, m{CHAT: m{"unlistedAddressed": "inferred"}}, nil, "off")).Gate(inChat(CHAT, STRANGER, true), thread(), nil), want{false, "not_a_member", "", nil})
	gateIs(t, pol(withTable(t, m{"direct": m{"unlistedAddressed": "explicit"}}, nil, "")).Gate(feishu(from(STRANGER), addressed("direct"), metaOf(direct())), thread(), nil),
		want{true, "", "explicit", m{"row": "direct", "cell": "unlistedAddressed"}})
	gateIs(t, pol(withTable(t, m{OBSERVED: m{"unlistedAddressed": "explicit"}}, nil, "")).Gate(inChat(CHAT, STRANGER, true), thread(), nil), want{false, "not_a_member", "", nil})
}

func TestAdmissionTableListedUnaddressedIgnoreAndUnreceivedMessages(t *testing.T) {
	gateIs(t, pol(withTable(t, m{OBSERVED: m{"listedUnaddressed": "ignore"}}, nil, "")).Gate(inChat(OBSERVED, MEMBER, false), thread(), nil), want{false, "admission_ignored", "", nil})
	gateIs(t, pol(withTable(t, m{CHAT: m{"listedUnaddressed": "inferred"}}, nil, "")).Gate(inChat(CHAT, MEMBER, false), thread(), nil), want{false, "no_mention", "", nil})
	gateIs(t, pol(withTable(t, m{CHAT: m{"unlistedUnaddressed": "inferred"}}, nil, "")).Gate(inChat(CHAT, STRANGER, false), thread(), nil), want{false, "not_a_member", "", nil})
}

func TestAdmissionTableOtherChatsRow(t *testing.T) {
	others := withTable(t, m{"other": m{"unlistedAddressed": "explicit"}}, m{"otherChats": "mentions"}, "")
	gateIs(t, pol(others).Gate(inChat("oc_elsewhere", MEMBER, true), thread(), nil), want{true, "", "explicit", m{"row": "other"}})
	gateIs(t, pol(others).Gate(inChat("oc_elsewhere", STRANGER, true), thread(), nil), want{true, "", "explicit", m{"row": "other", "cell": "unlistedAddressed"}})
}

func TestAdmissionTableNoInferredFollowupWhileOff(t *testing.T) {
	off := baseSettings(t)
	off.Eval.Mode = "off"
	quiet := metaOf(quietIn(CHAT))
	gateIs(t, pol(off).Gate(unaddressed(quiet, from("ou_guest")), thread("ticket"), nil), want{false, "not_a_member", "", nil})
	gateIs(t, pol(off).Gate(unaddressed(quiet), thread("ticket"), nil), want{true, "", "explicit", m{"via": "followup"}})
}

func TestAdmissionTableExplicitCellBeatsFollowupInEveryMode(t *testing.T) {
	for _, mode := range []string{"auto", "observe", "off"} {
		gateIs(t, pol(withTable(t, m{CHAT: m{"unlistedAddressed": "explicit"}}, nil, mode)).Gate(inChat(CHAT, STRANGER, true), thread("ticket"), nil),
			want{true, "", "explicit", m{"via": "admission", "cell": "unlistedAddressed"}})
		strict := pol(withTable(t, m{CHAT: m{"unlistedAddressed": "ignore"}}, nil, mode)).Gate(inChat(CHAT, STRANGER, true), thread("ticket"), nil)
		if mode == "off" {
			gateIs(t, strict, want{false, "", "", nil})
		} else {
			gateIs(t, strict, want{true, "", "inferred", m{"via": "followup"}})
		}
	}
}

func TestAdmissionTableFollowupsOutsideTable(t *testing.T) {
	strict := pol(withTable(t, m{OBSERVED: m{"listedUnaddressed": "ignore", "unlistedUnaddressed": "ignore"}}, nil, ""))
	gateIs(t, strict.Gate(inChat(OBSERVED, MEMBER, false), thread("ticket"), nil), want{true, "", "explicit", m{"via": "followup"}})
	gateIs(t, strict.Gate(inChat(OBSERVED, STRANGER, false), thread("ticket"), nil), want{true, "", "inferred", m{"via": "followup"}})
}

func TestAdmissionTableInvalidatesMandateWhenCellWeakens(t *testing.T) {
	open := withTable(t, m{CHAT: m{"unlistedAddressed": "explicit"}}, nil, "")
	gate := pol(open).Gate(inChat(CHAT, STRANGER, true), thread(), nil)
	mandate := core.Mandate{Strength: gate.Strength, PolicyVersion: "v", Basis: gate.Basis, Actor: core.ActorRef{Source: "feishu", ID: STRANGER}}
	if !pol(open).Revalidate(mandate) || pol(withTable(t, m{CHAT: m{"unlistedAddressed": "inferred"}}, nil, "")).Revalidate(mandate) || pol(withTable(t, m{}, nil, "")).Revalidate(mandate) {
		t.Fatal("revalidate by cell")
	}
}

func TestAdmissionTableCellChangesVersion(t *testing.T) {
	plain := policy.PolicyVersion(withTable(t, m{}, nil, ""))
	if policy.PolicyVersion(withTable(t, m{CHAT: m{"unlistedAddressed": "explicit"}}, nil, "")) == plain {
		t.Fatal("cell did not change version")
	}
	if policy.PolicyVersion(withTable(t, m{}, nil, "")) != policy.PolicyVersion(baseSettings(t)) {
		t.Fatal("empty table changed version")
	}
}

// describe('revalidating a Mandate from the admission table'): the full it.each matrix.
func TestRevalidateMandateFromAdmissionTableMatrix(t *testing.T) {
	const SENDER = "ou_sender"
	cells := []string{"listedUnaddressed", "unlistedAddressed", "unlistedUnaddressed"}
	origins := []struct{ cell, strength string }{{"listedUnaddressed", "inferred"}, {"unlistedAddressed", "explicit"}, {"unlistedAddressed", "inferred"}, {"unlistedUnaddressed", "inferred"}}
	n := 0
	for _, o := range origins {
		for _, listed := range []bool{true, false} {
			for _, mode := range []string{"off", "observe", "auto"} {
				for _, rule := range []bool{true, false} {
					for _, scope := range []string{"all", "mentions", "none"} {
						for _, holds := range []bool{true, false} {
							n++
							expected := func() bool {
								if scope == "none" {
									return false
								}
								if o.cell == "unlistedAddressed" && listed {
									return true
								}
								now := o.cell
								if o.cell == "unlistedUnaddressed" && listed {
									now = "listedUnaddressed"
								}
								if (now == "listedUnaddressed" && !listed) || !holds {
									return false
								}
								return o.strength == "explicit" || (mode == "auto" && rule)
							}()
							grant := func(cell string) string {
								if !holds {
									return "ignore"
								}
								if cell == "unlistedAddressed" {
									return o.strength
								}
								return "inferred"
							}
							row := m{}
							for _, c := range cells {
								row[c] = grant(c)
							}
							chats := []m{}
							if scope != "none" {
								chats = append(chats, m{"chatId": CHAT, "listen": scope})
							}
							allowed := []m{}
							if listed {
								allowed = append(allowed, m{"id": SENDER})
							}
							s := settings(t, m{"eval": m{"mode": mode}, "sources": m{"feishu": feishuSource(chats, true)},
								"policy": m{"rules": []m{{"id": "copy", "name": "Copy", "enabled": rule, "instruction": "x"}},
									"sources": m{"feishu": m{"allowed": allowed, "admission": m{CHAT: row}}}}})
							addr := o.cell == "unlistedAddressed"
							chat := CHAT
							basis := core.MandateBasis{Via: "admission", Source: "feishu", Scope: m{"chatId": CHAT, "direct": false, "mentioned": addr}, Row: &chat, Cell: o.cell}
							if addr {
								basis.Addressed = "mention"
							}
							if o.strength == "inferred" {
								basis.Observation, basis.Rules = &core.Observation{Mode: "auto"}, []string{"copy"}
							}
							mandate := core.Mandate{Strength: o.strength, PolicyVersion: "v", Basis: basis, Actor: core.ActorRef{Source: "feishu", ID: SENDER}}
							if got := pol(s).Revalidate(mandate); got != expected {
								t.Errorf("%s (%s), listed %v, mode %s, rule %v, scope %s, holds %v: got %v want %v", o.cell, o.strength, listed, mode, rule, scope, holds, got, expected)
							}
						}
					}
				}
			}
		}
	}
	if n != 288 {
		t.Fatalf("cases: %d", n)
	}
}

func TestRevalidateExplicitAndRuleMandates(t *testing.T) {
	base := baseSettings(t)
	p := pol(base)
	basis := core.MandateBasis{Via: "addressed", Source: "feishu", Scope: m{"chatId": CHAT, "direct": false}, Sender: "Alice", Addressed: "mention"}
	mandate := core.Mandate{Strength: "explicit", PolicyVersion: "v", Basis: basis, Actor: core.ActorRef{Source: "feishu", ID: MEMBER}}
	if !p.Revalidate(mandate) {
		t.Fatal("explicit mandate")
	}
	nobody := base
	nobody.Policy.Sources = map[string]policy.SourcePolicy{}
	if pol(nobody).Revalidate(mandate) {
		t.Fatal("sender removed")
	}
	silent := settings(t, m{"policy": m{"sources": m{"feishu": m{"allowed": []m{{"id": MEMBER, "name": "Alice"}}}, "github": m{"allowed": []m{{"id": "Alice"}}}}},
		"eval": m{"mode": "auto"}, "sources": m{"feishu": feishuSource(nil, true)}})
	if pol(silent).Revalidate(mandate) {
		t.Fatal("chat no longer received")
	}
	listed := mandate
	listed.Basis = core.MandateBasis{Via: "listed", Source: "feishu", Scope: m{"chatId": CHAT, "direct": false}, Sender: "Alice"}
	if pol(silent).Revalidate(listed) || !p.Revalidate(listed) {
		t.Fatal("listed mandate")
	}
	inferred := mandate
	inferred.Strength = "inferred"
	inferred.Basis = core.MandateBasis{Via: "listed", Observation: &core.Observation{}, Rules: []string{"copy"}}
	if !p.Revalidate(inferred) {
		t.Fatal("inferred in auto")
	}
	observe := base
	observe.Eval.Mode = "observe"
	if pol(observe).Revalidate(inferred) {
		t.Fatal("inferred in observe")
	}
	disabled := base
	disabled.Policy.Rules = nil
	for _, r := range base.Policy.Rules {
		r.Enabled = r.ID != "copy"
		disabled.Policy.Rules = append(disabled.Policy.Rules, r)
	}
	if pol(disabled).Revalidate(inferred) {
		t.Fatal("inferred with rule off")
	}
	followup := mandate
	followup.Basis = core.MandateBasis{Via: "followup"}
	if !pol(nobody).Revalidate(followup) {
		t.Fatal("explicit follow-up")
	}
}

func TestVersionChangesForAuthorizationOnly(t *testing.T) {
	base := baseSettings(t)
	version := policy.PolicyVersion(base)
	same := base
	same.Paused = true
	same.Eval.TimeoutSeconds = 300
	same.Sources = map[string]policy.SourceInstance{"feishu": base.Sources["feishu"], "github": {Enabled: true, Config: m{"pollSeconds": 600}}}
	if policy.PolicyVersion(same) != version {
		t.Fatal("pause/timeout/source config changed version")
	}
	quiet := base
	quiet.Sources = map[string]policy.SourceInstance{"feishu": {Enabled: true, Config: m{"chats": []any{}, "directMessages": false}}}
	if policy.PolicyVersion(quiet) != version {
		t.Fatal("source config changed version")
	}
	off := base
	off.Eval.Mode = "off"
	if policy.PolicyVersion(off) == version {
		t.Fatal("mode did not change version")
	}
	parts := settings(t, m{"policy": m{"sources": m{"feishu": m{"allowed": []m{{"id": MEMBER, "name": "Alice"}}}, "github": m{"allowed": []m{}}}},
		"eval": m{"mode": "auto"}})
	if policy.PolicyVersion(parts) == version {
		t.Fatal("source part did not change version")
	}
}

var direction = m{"implementation_requested": true, "target_basis": "specified", "requires_design_decision": false, "evidence": "asked"}

func modeSettings(t *testing.T, mode string) policy.Settings {
	return settings(t, m{"eval": m{"mode": mode}})
}

func judged(s policy.Settings, matched bool) []policy.RuleAssessment {
	out := []policy.RuleAssessment{}
	for _, r := range s.Policy.Rules {
		if r.Enabled {
			out = append(out, policy.RuleAssessment{RuleID: r.ID, Matched: matched, Reason: "judged"})
		}
	}
	return out
}

func strp(s string) *string { return &s }

func acceptAssessment(s policy.Settings, matched bool) policy.Assessment {
	return policy.Assessment{Decision: "accept", RuleAssessments: judged(s, matched), Direction: direction, Title: "Fix", Scope: "small", Reason: "asked",
		IntentEvidence: strp("quote"), WorktreeName: "fix-copy"}
}

func TestAssessmentOutcomesAcceptInferredOnlyInAuto(t *testing.T) {
	auto := modeSettings(t, "auto")
	o, err := policy.AssessmentOutcome(acceptAssessment(auto, true), auto, "inferred")
	if err != nil || o.Kind != "accept" || !reflect.DeepEqual(o.Rules, []string{"copy", "visual"}) || o.Name != "fix-copy" {
		t.Fatalf("auto accept: %+v %v", o, err)
	}
	observe := modeSettings(t, "observe")
	o, _ = policy.AssessmentOutcome(acceptAssessment(observe, true), observe, "inferred")
	if o.Kind != "ignore" || o.ReasonCode.Code != "observed" || o.ReasonCode.Values["text"] != "asked" || o.Audit["would_accept"] != true {
		t.Fatalf("observe: %+v", o)
	}
	o, _ = policy.AssessmentOutcome(acceptAssessment(auto, false), auto, "inferred")
	if o.Kind != "ignore" || o.ReasonCode.Code != "noRuleMatched" {
		t.Fatalf("no match: %+v", o)
	}
	none := acceptAssessment(auto, true)
	none.RuleAssessments = nil
	if _, err := policy.AssessmentOutcome(none, auto, "inferred"); err == nil || !strings.Contains(err.Error(), "Exactly one assessment for every enabled rule") {
		t.Fatalf("coverage: %v", err)
	}
	noEvidence := acceptAssessment(auto, true)
	noEvidence.IntentEvidence = nil
	if _, err := policy.AssessmentOutcome(noEvidence, auto, "inferred"); err == nil || err.Error() != "Acceptance requires intent_evidence" {
		t.Fatalf("evidence: %v", err)
	}
	scope := fakeSources{auto}
	if !policy.SettingsPolicy(auto, scope).AdmitsInferred([]string{"copy"}) || policy.SettingsPolicy(auto, scope).AdmitsInferred([]string{"unknown"}) ||
		policy.SettingsPolicy(auto, scope).AdmitsInferred([]string{}) || policy.SettingsPolicy(observe, scope).AdmitsInferred([]string{"copy"}) {
		t.Fatal("admitsInferred")
	}
}

func TestAdmissionSettingsRefuseExplicitUnaddressedCell(t *testing.T) {
	for _, cell := range []string{"listedUnaddressed", "unlistedUnaddressed"} {
		data, _ := json.Marshal(m{"policy": m{"sources": m{"feishu": m{"allowed": []m{}, "admission": m{CHAT: m{cell: "explicit"}}}}}})
		if _, err := policy.ParseSettings(data); err == nil {
			t.Fatalf("%s explicit accepted", cell)
		}
	}
	s := settings(t, m{"policy": m{"sources": m{"feishu": m{"allowed": []m{}, "admission": m{CHAT: m{"unlistedAddressed": "explicit"}}}}}})
	if s.Policy.Sources["feishu"].Admission[CHAT].UnlistedAddressed != "explicit" {
		t.Fatal("explicit addressed cell dropped")
	}
}

func TestAdmissionSettingsExplicitAcceptedInEveryMode(t *testing.T) {
	for _, mode := range []string{"observe", "off"} {
		s := modeSettings(t, mode)
		rules := []policy.RuleAssessment{}
		for i, r := range judged(s, false) {
			r.Matched = i == 0
			rules = append(rules, r)
		}
		a := policy.Assessment{Decision: "accept", Direction: direction, Title: "Fix", Scope: "small", Reason: "asked", IntentEvidence: strp("quote"), RuleAssessments: rules}
		if o, _ := policy.AssessmentOutcome(a, s, "explicit"); o.Kind != "accept" || o.Title != "Fix" {
			t.Fatalf("%s explicit: %+v", mode, o)
		}
		if o, _ := policy.AssessmentOutcome(a, s, "inferred"); o.Kind != "ignore" || o.ReasonCode.Code != "observed" {
			t.Fatalf("%s inferred: %+v", mode, o)
		}
	}
}

func TestAdmissionSettingsListedBotMention(t *testing.T) {
	s := settings(t, m{"policy": m{"sources": m{"feishu": m{"allowed": []m{{"id": MEMBER, "name": "Alice"}}, "bots": []m{{"id": "cli_alerts"}}}}},
		"eval": m{"mode": "auto"}, "sources": m{"feishu": feishuSource([]m{{"chatId": CHAT, "listen": "mentions"}, {"chatId": OBSERVED, "listen": "all"}}, true)}})
	mention := feishu(from("cli_alerts"), machine())
	gateIs(t, pol(s).Gate(mention, thread(), nil), want{true, "", "inferred", m{"via": "bot", "addressed": "mention", "row": CHAT}})
}

func TestRuleJudgementsRefuseDoubleJudgement(t *testing.T) {
	s := settings(t, m{"eval": m{"mode": "auto"}, "policy": m{"rules": []m{{"id": "copy", "name": "Copy", "enabled": true, "instruction": "Fix typos."},
		{"id": "visual", "name": "Visual", "enabled": true, "instruction": "Fix colors."}}}})
	a := policy.Assessment{Decision: "accept", Direction: direction, Title: "Fix", Scope: "small", Reason: "asked", IntentEvidence: strp("quote"),
		RuleAssessments: []policy.RuleAssessment{{RuleID: "copy", Matched: true, Reason: "typo"}, {RuleID: "copy", Matched: false, Reason: "again"}}}
	if _, err := policy.AssessmentOutcome(a, s, "inferred"); err == nil || !strings.Contains(err.Error(), "Exactly one assessment for every enabled rule") {
		t.Fatalf("double: %v", err)
	}
	a.RuleAssessments = []policy.RuleAssessment{a.RuleAssessments[0], {RuleID: "visual", Matched: false, Reason: "no colors"}}
	if o, _ := policy.AssessmentOutcome(a, s, "inferred"); o.Kind != "accept" || !reflect.DeepEqual(o.Rules, []string{"copy"}) {
		t.Fatalf("once: %+v", o)
	}
}

func effectsBase(t *testing.T) policy.Settings {
	return settings(t, m{"policy": m{"rules": []m{{"id": "copy", "name": "Copy", "enabled": true, "instruction": "Fix typos."},
		{"id": "visual", "name": "Visual", "enabled": true, "instruction": "Fix colors."}}, "sources": m{"feishu": m{"allowed": []m{{"id": "ou_alice"}}}}}})
}

type observedAll struct{ row *string }

func (o observedAll) Scope(string, map[string]any) core.ScopeResult {
	return core.ScopeResult{Listened: true, Observed: true, Row: o.row}
}
func allScope(policy.Settings) core.SourceScope { return observedAll{} }

func tk(id, strength string, actor string, basis core.MandateBasis) core.Ticket {
	return core.Ticket{ID: id, Mandate: core.Mandate{Strength: strength, Actor: core.ActorRef{Source: "feishu", ID: actor}, Basis: basis}}
}

func no() *bool { b := false; return &b }

func TestPolicyEffectsTellBearingTicketsOnly(t *testing.T) {
	base := effectsBase(t)
	inferred := tk("t1", "inferred", "ou_guest", core.MandateBasis{Via: "listed", Rules: []string{"copy"}})
	explicit := tk("t2", "explicit", "ou_alice", core.MandateBasis{Via: "listed", Source: "feishu"})
	rules := func(copyFn, visualFn func(*policy.Rule)) policy.Settings {
		s := base
		s.Policy.Rules = []policy.Rule{base.Policy.Rules[0], base.Policy.Rules[1]}
		copyFn(&s.Policy.Rules[0])
		visualFn(&s.Policy.Rules[1])
		return s
	}
	keep := func(*policy.Rule) {}
	if n := len(policy.PolicyEffects([]core.Ticket{inferred, explicit}, base, rules(keep, func(r *policy.Rule) { r.Enabled = false }), allScope)); n != 0 {
		t.Fatalf("unrelated rule: %d", n)
	}
	got := policy.PolicyEffects([]core.Ticket{inferred}, base, rules(func(r *policy.Rule) { r.Instruction = "Fix typos only." }, keep), allScope)["t1"]
	if !reflect.DeepEqual(got, core.PolicyEffect{Rules: []core.RuleEffect{{ID: "copy", Name: "Copy", Change: "changed", Instruction: "Fix typos only."}}}) {
		t.Fatalf("changed: %+v", got)
	}
	got = policy.PolicyEffects([]core.Ticket{inferred}, base, rules(func(r *policy.Rule) { r.Enabled = false }, keep), allScope)["t1"]
	if !reflect.DeepEqual(got, core.PolicyEffect{Rules: []core.RuleEffect{{ID: "copy", Name: "Copy", Change: "disabled"}}}) {
		t.Fatalf("disabled: %+v", got)
	}
	allowed := func(ids ...string) policy.Settings {
		s := base
		list := []policy.Allowed{}
		for _, id := range ids {
			list = append(list, policy.Allowed{ID: id})
		}
		s.Policy.Sources = map[string]policy.SourcePolicy{"feishu": {Allowed: list, Admission: map[string]policy.AdmissionRow{}}}
		return s
	}
	if n := len(policy.PolicyEffects([]core.Ticket{inferred, explicit}, base, allowed("ou_alice", "ou_bob"), allScope)); n != 0 {
		t.Fatalf("added sender: %d", n)
	}
	got = policy.PolicyEffects([]core.Ticket{explicit}, base, allowed(), allScope)["t2"]
	if !reflect.DeepEqual(got, core.PolicyEffect{RequesterAllowed: no(), Requester: &core.ActorRef{Source: "feishu", ID: "ou_alice"}}) {
		t.Fatalf("requester: %+v", got)
	}
}

func TestPolicyEffectsModeLeavingAutoAndBots(t *testing.T) {
	base := effectsBase(t)
	auto, observe := base, base
	auto.Eval.Mode, observe.Eval.Mode = "auto", "observe"
	rule := tk("t3", "inferred", "ou_alice", core.MandateBasis{Via: "listed", Source: "feishu", Rules: []string{"copy"}, Observation: &core.Observation{}})
	explicit := tk("t2", "explicit", "ou_alice", core.MandateBasis{Via: "listed", Source: "feishu"})
	effects := policy.PolicyEffects([]core.Ticket{rule, explicit}, auto, observe, allScope)
	if !reflect.DeepEqual(effects["t3"], core.PolicyEffect{Proactive: no()}) {
		t.Fatalf("t3: %+v", effects["t3"])
	}
	if _, ok := effects["t2"]; ok {
		t.Fatal("t2 told")
	}
	bot := tk("t4", "explicit", "cli_ci_bot", core.MandateBasis{Via: "listed", Source: "feishu"})
	listed := base
	listed.Policy.Sources = map[string]policy.SourcePolicy{"feishu": {Allowed: []policy.Allowed{{ID: "cli_ci_bot"}}, Admission: map[string]policy.AdmissionRow{}}}
	if got := policy.PolicyEffects([]core.Ticket{bot}, listed, base, allScope)["t4"]; !reflect.DeepEqual(got, core.PolicyEffect{RequesterAllowed: no(), Requester: &core.ActorRef{Source: "feishu", ID: "cli_ci_bot"}}) {
		t.Fatalf("bot: %+v", got)
	}
}

func TestPolicyEffectsInferredFollowupMandate(t *testing.T) {
	base := effectsBase(t)
	followup := tk("t5", "inferred", "ou_guest", core.MandateBasis{Via: "followup", Source: "feishu", Scope: m{"chatId": "oc_team"}, Rules: []string{"copy"}})
	mode := func(v string) policy.Settings { s := base; s.Eval.Mode = v; return s }
	disabled := mode("auto")
	disabled.Policy.Rules = []policy.Rule{base.Policy.Rules[0], base.Policy.Rules[1]}
	disabled.Policy.Rules[0].Enabled = false
	if !policy.SettingsPolicy(mode("auto"), observedAll{}).Revalidate(followup.Mandate) {
		t.Fatal("auto")
	}
	for _, s := range []policy.Settings{mode("observe"), mode("off"), disabled} {
		if policy.SettingsPolicy(s, observedAll{}).Revalidate(followup.Mandate) {
			t.Fatal("held outside auto or with rule off")
		}
	}
	if got := policy.PolicyEffects([]core.Ticket{followup}, mode("auto"), mode("observe"), allScope)["t5"]; !reflect.DeepEqual(got, core.PolicyEffect{Proactive: no()}) {
		t.Fatalf("t5: %+v", got)
	}
	explicitFollowup := followup.Mandate
	explicitFollowup.Strength, explicitFollowup.Actor = "explicit", core.ActorRef{Source: "feishu", ID: "ou_alice"}
	if !policy.SettingsPolicy(mode("off"), observedAll{}).Revalidate(explicitFollowup) {
		t.Fatal("explicit follow-up off")
	}
}

func TestPolicyEffectsWeakerCellRevokesAndDeletedRuleDisabled(t *testing.T) {
	base := effectsBase(t)
	chat := "oc_1111111111111111111111111111"
	table := func(cell, mode string) policy.Settings {
		s := base
		s.Eval.Mode = mode
		s.Policy.Sources = map[string]policy.SourcePolicy{"feishu": {Allowed: []policy.Allowed{{ID: "ou_alice"}}, Admission: map[string]policy.AdmissionRow{chat: {UnlistedUnaddressed: cell}}}}
		return s
	}
	observed := func(policy.Settings) core.SourceScope { return observedAll{row: &chat} }
	proactive := tk("t3", "inferred", "ou_guest", core.MandateBasis{Via: "admission", Cell: "unlistedUnaddressed", Source: "feishu", Scope: m{"chatId": chat}, Row: &chat,
		Rules: []string{"copy"}, Observation: &core.Observation{Mode: "auto"}})
	if n := len(policy.PolicyEffects([]core.Ticket{proactive}, table("inferred", "auto"), table("inferred", "auto"), observed)); n != 0 {
		t.Fatalf("unchanged: %d", n)
	}
	if got := policy.PolicyEffects([]core.Ticket{proactive}, table("inferred", "auto"), table("ignore", "auto"), observed)["t3"]; !reflect.DeepEqual(got, core.PolicyEffect{RequesterAllowed: no(), Requester: &core.ActorRef{Source: "feishu", ID: "ou_guest"}}) {
		t.Fatalf("weaker: %+v", got)
	}
	if got := policy.PolicyEffects([]core.Ticket{proactive}, table("inferred", "auto"), table("inferred", "observe"), observed)["t3"]; !reflect.DeepEqual(got, core.PolicyEffect{Proactive: no()}) {
		t.Fatalf("observe: %+v", got)
	}
	deleted := base
	deleted.Policy.Rules = []policy.Rule{base.Policy.Rules[0]}
	visual := tk("t4", "explicit", "ou_alice", core.MandateBasis{Via: "listed", Source: "feishu", Rules: []string{"visual"}})
	if got := policy.PolicyEffects([]core.Ticket{visual}, base, deleted, allScope)["t4"]; !reflect.DeepEqual(got, core.PolicyEffect{Rules: []core.RuleEffect{{ID: "visual", Name: "Visual", Change: "disabled"}}}) {
		t.Fatalf("deleted: %+v", got)
	}
}

func observing(t *testing.T, config m, part m, mode string) policy.Settings {
	gh := m{"allowed": []m{{"id": "Alice"}}, "bots": []m{{"id": "dependabot[bot]"}}}
	for k, v := range part {
		gh[k] = v
	}
	if mode == "" {
		mode = "auto"
	}
	return settings(t, m{"policy": m{"sources": m{"github": gh}}, "eval": m{"mode": mode}, "sources": m{"github": m{"enabled": true, "config": config}}})
}

var comment = core.Trigger{ID: "b", Source: "github", Threads: []core.ThreadRef{{Source: "github", ID: "1"}}, Actor: core.ActorRef{Source: "github", ID: "dependabot[bot]"},
	Text: "Bump lodash", Meta: m{"number": 1, "isPr": true, "kind": "conversation"}}

func ci(extra m) core.Trigger {
	meta := m{"ci": true}
	for k, v := range extra {
		meta[k] = v
	}
	return core.Trigger{ID: "ci", Source: "github", Threads: []core.ThreadRef{{Source: "github", ID: "ci:main"}}, Actor: core.ActorRef{Source: "github"},
		Text: "CI failed 1/9 on main", Meta: meta}
}

func TestBotsListedBotInferredWhereObserved(t *testing.T) {
	p := pol(observing(t, m{"observeComments": true}, nil, ""))
	gateIs(t, p.Gate(comment, thread(), nil), want{true, "", "inferred", m{"via": "bot", "source": "github", "sender": "dependabot[bot]", "row": "comments", "observation": m{"mode": "auto"}}})
	mentioned := comment
	mentioned.Addressed = "mention"
	gateIs(t, p.Gate(mentioned, thread("ticket"), []core.Ticket{endedTicket}), want{true, "", "inferred", m{"via": "bot", "addressed": "mention"}})
	other := comment
	other.Actor.ID = "renovate[bot]"
	gateIs(t, p.Gate(other, thread(), nil), want{false, "not_human", "", nil})
	gateIs(t, pol(observing(t, m{}, nil, "")).Gate(comment, thread(), nil), want{false, "not_human", "", nil})
	gateIs(t, pol(observing(t, m{"observeComments": true}, nil, "off")).Gate(comment, thread(), nil), want{false, "admission_ignored", "", nil})
}

func TestBotsMandateHoldsWhileListedAndAuto(t *testing.T) {
	s := observing(t, m{"observeComments": true}, nil, "")
	row := "comments"
	mandate := core.Mandate{Strength: "inferred", Actor: comment.Actor, Basis: core.MandateBasis{Via: "bot", Source: "github", Scope: comment.Meta, Row: &row,
		Sender: "dependabot[bot]", Observation: &core.Observation{Mode: "auto"}}}
	if !pol(s).Revalidate(mandate) {
		t.Fatal("listed bot")
	}
	if pol(observing(t, m{"observeComments": true}, m{"bots": []m{}}, "")).Revalidate(mandate) {
		t.Fatal("bot removed")
	}
	observe := s
	observe.Eval.Mode = "observe"
	if pol(observe).Revalidate(mandate) {
		t.Fatal("observe")
	}
}

func TestBotsDefaultBranchCIThroughRow(t *testing.T) {
	admission := m{"ci": m{"unlistedUnaddressed": "inferred"}}
	gateIs(t, pol(observing(t, m{"observeCi": true}, m{"admission": admission}, "")).Gate(ci(m{"branch": "main"}), thread(), nil),
		want{true, "", "inferred", m{"via": "admission", "cell": "unlistedUnaddressed", "row": "ci", "observation": m{"mode": "auto"}}})
	gateIs(t, pol(observing(t, m{"observeCi": true}, nil, "")).Gate(ci(m{"branch": "main"}), thread(), nil), want{false, "admission_ignored", "", nil})
	gateIs(t, pol(observing(t, m{"observeCi": true}, m{"admission": admission}, "")).Gate(ci(m{"number": 9, "isPr": true}), thread(), nil), want{false, "not_human", "", nil})
}

func TestBotsFeishuBotInObservedChat(t *testing.T) {
	s := settings(t, m{"policy": m{"sources": m{"feishu": m{"allowed": []m{{"id": MEMBER, "name": "Alice"}}, "bots": []m{{"id": "cli_alerts", "name": "Alerts"}}}}},
		"eval": m{"mode": "auto"}, "sources": m{"feishu": feishuSource([]m{{"chatId": CHAT, "listen": "mentions"}, {"chatId": OBSERVED, "listen": "all"}}, true)}})
	alert := unaddressed(from("cli_alerts"), machine(), metaOf(quietIn(OBSERVED)))
	gateIs(t, pol(s).Gate(alert, thread(), nil), want{true, "", "inferred", m{"via": "bot", "sender": "Alerts", "row": OBSERVED}})
	alert.Meta = quietIn(CHAT)
	gateIs(t, pol(s).Gate(alert, thread(), nil), want{false, "not_human", "", nil})
}
