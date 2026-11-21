package executor_test

// Translated from dsh-superagent-prototype tests/runtime-concurrency.spec.ts, "the Executor's parts" and the passing
// failure case, with the input and relay renderings of personalagent/cards.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/cards"
	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/effects"
	"github.com/multica-ai/multica/server/internal/personalagent/executor"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

func isLater(err error) bool {
	var later *effects.Later
	return errors.As(err, &later)
}

func TestHoldsOneWorkerSlotPerTicketThatRunsOrStartsAndLetsLaunchesTakeTheSlotsNoWorkerOrWakeHolds(t *testing.T) {
	running := []string{"t1"}
	slots := executor.NewSlots(func() []string { return running }, func() int { return 3 })
	must(t, slots.Reserve("t2", "launch"))
	must(t, slots.Reserve("t3", "wake"))
	if slots.Used() != 3 {
		t.Fatalf("used %d", slots.Used())
	}
	if slots.Launches() != 1 {
		t.Fatalf("launches %d", slots.Launches())
	}
	if !isLater(slots.Reserve("t4", "wake")) {
		t.Fatal("t4 should wait")
	}
	must(t, slots.Reserve("t3", "wake"))
	slots.Free("t3")
	if slots.Used() != 2 {
		t.Fatalf("used %d", slots.Used())
	}
	must(t, slots.Reserve("t4", "launch"))
	if slots.Used() != 3 {
		t.Fatalf("used %d", slots.Used())
	}
}

func TestWakesAWorkerForAPersonsMessageInTheOneSlotBeyondTheLimitAndAheadOfLaunches(t *testing.T) {
	running := []string{"t1", "t2", "t3"}
	slots := executor.NewSlots(func() []string { return running }, func() int { return 3 })
	if !isLater(slots.Reserve("t4", "launch")) || !isLater(slots.Reserve("t4", "wake")) {
		t.Fatal("a launch and a wake should wait")
	}
	must(t, slots.Reserve("t5", "person"))
	if slots.Used() != 4 || slots.Launches() > 0 {
		t.Fatalf("used %d launches %d", slots.Used(), slots.Launches())
	}
	if !isLater(slots.Reserve("t6", "person")) {
		t.Fatal("no second person's wake beyond the limit")
	}
	running = running[1:]
	if !isLater(slots.Reserve("t7", "launch")) {
		t.Fatal("a launch still waits")
	}
	must(t, slots.Reserve("t6", "person"))
	if slots.Used() != 4 {
		t.Fatalf("used %d", slots.Used())
	}
}

type status int

func (s status) Error() string   { return "x" }
func (s status) StatusCode() int { return int(s) }

func TestTellsAPassingFailureOfTheProviderOrTheNetworkFromOneALaterTryWouldMeetAgain(t *testing.T) {
	for _, err := range []error{status(429), status(503), errors.New("connect ECONNRESET"), errors.New("fetch failed"), errors.New("getaddrinfo EAI_AGAIN api.example.com")} {
		if !executor.Transient(err) {
			t.Fatalf("%v should be passing", err)
		}
	}
	for _, err := range []error{status(400), status(401), errors.New("context too long"), errors.New("LlmError: offline")} {
		if executor.Transient(err) {
			t.Fatalf("%v should not be passing", err)
		}
	}
}

func TestWordsAnAssignmentAndTheLimitsAnInferredMandateSetsFromTheTicketAlone(t *testing.T) {
	rules := []policy.Rule{{ID: "copy", Name: "Copy fixes", Enabled: true, Instruction: "Fix typos only."}}
	ticket := core.Ticket{ID: "ticket-0123456789abcdef", Origin: core.Origin{TriggerID: "tr1"},
		Mandate: core.Mandate{Strength: "inferred", PolicyVersion: "v1", Basis: core.MandateBasis{Rules: []string{"copy"}}, Actor: core.ActorRef{Source: "github", ID: "alice"}}}
	execution, err := executor.TicketExecution(ticket, core.Outcome{Kind: "accept", Title: "Fix", Scope: "the typo", Name: "fix-typo"}, rules)
	must(t, err)
	if execution.Name != "fix-typo-ticket-01234" {
		t.Fatalf("name %q", execution.Name)
	}
	if !strings.Contains(execution.Assignment, "Scope: the typo.") || strings.Contains(execution.Assignment, "trigger/tr1") {
		t.Fatalf("assignment %q", execution.Assignment)
	}
	explicit := ticket
	explicit.Mandate.Strength = "explicit"
	other, err := executor.TicketExecution(explicit, core.Outcome{Kind: "accept", Title: "Fix", Scope: "Fix", Name: "fix-login", Note: "the login page"}, rules)
	must(t, err)
	// MUL-13499: an explicit ticket's Worker gets its matched rules' procedure too.
	if !strings.HasPrefix(other.Assignment, "Routing note (not the source request): the login page\nMatched rules; follow their procedure for this ticket: it takes precedence over the default worker behaviour (implementing and opening a PR yourself, reporting dropped, not commenting on or creating issues) and over the default acknowledgement and messaging policy:\n- Copy fixes (copy): Fix typos only.") {
		t.Fatalf("assignment %q", other.Assignment)
	}
	explicit.Mandate.Basis.Rules = nil
	plain, err := executor.TicketExecution(explicit, core.Outcome{Kind: "accept", Title: "Fix", Scope: "Fix", Name: "fix-login", Note: "the login page"}, rules)
	must(t, err)
	if plain.Assignment != "Routing note (not the source request): the login page" {
		t.Fatalf("assignment %q", plain.Assignment)
	}
	if strings.Contains(execution.Assignment, executor.MandateInstructions(ticket.Mandate)) {
		t.Fatal("the assignment repeats the mandate's limits")
	}
	if !strings.Contains(execution.Assignment, "- Copy fixes (copy): Fix typos only.") {
		t.Fatalf("assignment %q", execution.Assignment)
	}
	if executor.MandateInstructions(core.Mandate{Strength: "explicit"}) != "" {
		t.Fatal("an explicit mandate sets no limits")
	}
}

func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	var m map[string]any
	must(t, json.Unmarshal([]byte(text), &m))
	return m
}

func roundTrip(t *testing.T, v any) map[string]any {
	t.Helper()
	data, err := json.Marshal(v)
	must(t, err)
	return decode(t, string(data))
}

func TestRendersEachKindOfInputAsItsWorkerReadsItInlineAndNoReopenInput(t *testing.T) {
	asked := core.Trigger{ID: "tr1", Source: "feishu", Threads: []core.ThreadRef{{Source: "feishu", ID: "oc:om_1"}}, Actor: core.ActorRef{Source: "feishu", ID: "ou_a"},
		Human: true, At: "2026-09-01T00:00:00Z", Text: "Which page?", Meta: map[string]any{}}
	reply := asked
	reply.ID = "tr2"
	reply.Text = strings.Repeat("The login page. ", 50)
	card := cards.TriggerMessage(reply, cards.Link{SourceURL: "https://x"}, "see the log", &asked)
	text := card["text"].(string)
	if !strings.HasPrefix(text, "The login page.") || len([]rune(text)) != 601 {
		t.Fatalf("text %q", text)
	}
	want := map[string]any{"trigger_id": "tr2", "from": "ou_a", "thread": "feishu:oc:om_1", "at": "2026-09-01T00:00:00Z", "text": text,
		"chars": float64(len(reply.Text)), "read": "trigger/tr2", "source_url": "https://x", "routing_note": "see the log",
		"replies_to": map[string]any{"trigger_id": "tr1", "preview": "Which page?"}}
	if got := roundTrip(t, card); !reflect.DeepEqual(got, want) {
		t.Fatalf("card %v", got)
	}
	if got := roundTrip(t, cards.TriggerMessage(asked, cards.Link{}, "", nil)); !reflect.DeepEqual(got, map[string]any{"trigger_id": "tr1", "from": "ou_a",
		"thread": "feishu:oc:om_1", "at": asked.At, "text": "Which page?"}) {
		t.Fatalf("card %v", got)
	}
	message, _ := cards.InputMessage(core.TicketInput{ID: "i1", Kind: "trigger", TriggerID: "tr2"}, card)
	if got := decode(t, message); !reflect.DeepEqual(got, want) {
		t.Fatalf("input %v", got)
	}
	message, _ = cards.InputMessage(core.TicketInput{ID: "i1", Kind: "trigger", TriggerID: "gone"}, nil)
	if got := decode(t, message); !reflect.DeepEqual(got, map[string]any{"trigger_id": "gone", "read": "trigger/gone"}) {
		t.Fatalf("input %v", got)
	}
	if got, _ := cards.InputMessage(core.TicketInput{ID: "i2", Kind: "text", Text: "Please also update the docs"}, nil); got != "Please also update the docs" {
		t.Fatalf("text input %q", got)
	}
	no := false
	changed, _ := cards.InputMessage(core.TicketInput{ID: "i3", Kind: "policy-changed", From: "v1", To: "v2", Effect: &core.PolicyEffect{RequesterAllowed: &no,
		Requester: &core.ActorRef{Source: "github", ID: "ci-bot"},
		Rules:     []core.RuleEffect{{ID: "copy", Name: "Copy fixes", Change: "changed", Instruction: "Fix typos only."}, {ID: "visual", Name: "Visual", Change: "disabled"}}}}, nil)
	for _, part := range []string{"Rule Copy fixes (copy), which this ticket was accepted under, changed. Keep the remaining work within it as it reads now: Fix typos only.",
		"Rule Visual (visual), which this ticket was accepted under, no longer applies", "The sender this ticket is for, ci-bot on github, may no longer start work here"} {
		if !strings.Contains(changed, part) {
			t.Fatalf("policy change lacks %q: %s", part, changed)
		}
	}
	if strings.Contains(changed, "person") || strings.Contains(changed, "v2") {
		t.Fatalf("policy change %s", changed)
	}
	mode, _ := cards.InputMessage(core.TicketInput{ID: "i6", Kind: "policy-changed", From: "v1", To: "v2", Effect: &core.PolicyEffect{Proactive: &no}}, nil)
	if !strings.Contains(mode, "Work inferred from a rule is no longer accepted, as the accept mode is no longer auto") || strings.Contains(mode, "sender") {
		t.Fatalf("mode change %s", mode)
	}
	if got, _ := cards.InputMessage(core.TicketInput{ID: "i5", Kind: "resumed", TriggerID: "tr2"}, nil); strings.Contains(got, "tr2") {
		t.Fatalf("resumed %s", got)
	}
	if _, ok := cards.InputMessage(core.TicketInput{ID: "i4", Kind: "reopen"}, nil); ok {
		t.Fatal("a reopen input is not delivered")
	}
}

func TestRelaysAWorkersQuestionResultOrAnswerToTheRouterAndKeepsItsProgressInternal(t *testing.T) {
	ticket := core.Ticket{ID: "t1", Title: "Fix", UpdatedAt: "2026-09-01T00:00:00Z", Attention: &core.Attention{ID: "a1", Question: "Which color?"},
		Mandate: core.Mandate{Strength: "explicit"}, Origin: core.Origin{TriggerID: "tr1"}}
	if _, ok := cards.ReportRelay(ticket, core.WorkerReport{State: "progress", Summary: "halfway"}); ok {
		t.Fatal("progress is internal")
	}
	question := "Which color?"
	asked, ok := cards.ReportRelay(ticket, core.WorkerReport{State: "blocked", Summary: "need input", Question: &question})
	if !ok || asked.ID != "attention:t1:a1" || !strings.Contains(asked.Text, `"kind":"worker_attention"`) {
		t.Fatalf("relay %+v", asked)
	}
	ticket.Attention = nil
	ready, ok := cards.ReportRelay(ticket, core.WorkerReport{State: "ready", Summary: "Done", Answers: "tr9"})
	if !ok || !strings.HasPrefix(ready.ID, "report:") {
		t.Fatalf("relay %+v", ready)
	}
	got := decode(t, ready.Text)
	for k, v := range map[string]any{"kind": "worker_report", "trigger_id": "tr1", "state": "ready", "answer_to": "tr9", "summary": "Done"} {
		if got[k] != v {
			t.Fatalf("%s = %v", k, got[k])
		}
	}
	if got["ticket"].(map[string]any)["ticket_id"] != "t1" {
		t.Fatalf("ticket %v", got["ticket"])
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// MUL-13583: a rule whose procedure comments on an existing issue and @s its executor gives the Worker the commands,
// and lifts the template's "do not reply to people in issues" for that comment.
func TestAMatchedRuleTellsTheWorkerHowToCommentOnTheOriginalIssueAndMentionItsExecutor(t *testing.T) {
	rules := []policy.Rule{{ID: "fix", Name: "修正补充", Enabled: true, Instruction: "A. 找到且单在办：在那张单上追加评论并 @ 那张单的执行 agent。"}}
	ticket := core.Ticket{ID: "ticket-0123456789abcdef", Mandate: core.Mandate{Strength: "explicit", Basis: core.MandateBasis{Rules: []string{"fix"}}}}
	execution, err := executor.TicketExecution(ticket, core.Outcome{Kind: "accept", Title: "Fix", Scope: "Fix", Name: "fix-icon"}, rules)
	must(t, err)
	for _, want := range []string{
		"\"$MULTICA_CLI_PATH\" issue get <原单> --output json",
		"[@<执行方名>](mention://agent/<assignee_id>)",
		"\"$MULTICA_CLI_PATH\" issue comment add <原单> --content-file ./supplement.md",
		"不在 issue 里回复别人",
	} {
		if !strings.Contains(execution.Assignment, want) {
			t.Fatalf("assignment lacks %q:\n%s", want, execution.Assignment)
		}
	}
	plain := ticket
	plain.Mandate.Basis.Rules = nil
	other, err := executor.TicketExecution(plain, core.Outcome{Kind: "accept", Title: "Fix", Scope: "Fix", Name: "fix-icon"}, rules)
	must(t, err)
	if strings.Contains(other.Assignment, "issue comment add") {
		t.Fatalf("an unmatched ticket gets the commands: %q", other.Assignment)
	}
}
