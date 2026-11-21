package prompts_test

// Translated from dsh-superagent-prototype tests/prompts.spec.ts: the cases that assemble the roles' prompts and read
// prompt files. Team-section versioning, overrides and skills are the panel's.

import (
	"strings"
	"testing"
	"testing/fstest"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/prompts"
)

func TestEndsEachPromptWithTheHandoffAndTheEnvironmentAndTheRoutersWithItsPolicy(t *testing.T) {
	settings := policy.MustParseSettings(`{}`)
	section := prompts.PolicySection(settings, nil)
	for _, role := range []string{prompts.Orchestrator, prompts.Worker} {
		parts := prompts.Parts(prompts.Defaults(), role, "", "")
		if last := parts[len(parts)-1]; last.Name != "runtime/handoff" || last.Kind != "fixed" || last.Text != prompts.Handoff(role) {
			t.Fatalf("%s: last part %+v", role, last)
		}
		prompt := prompts.Compose(prompts.Defaults(), role, prompts.Environment, section)
		if !strings.Contains(prompt, prompts.Handoff(role)+"\n\nExecution environment") {
			t.Fatalf("%s: no environment after the handoff", role)
		}
		want := prompts.Environment
		if role == prompts.Orchestrator {
			want += "\n\n" + section
		}
		if !strings.HasSuffix(prompt, want) {
			t.Fatalf("%s prompt ends %q", role, prompt[len(prompt)-80:])
		}
	}
}

func names(parts []prompts.Part) []string {
	out := []string{}
	for _, p := range parts {
		out = append(out, p.Name)
	}
	return out
}

func TestAssemblesEveryRoleFromFixedSectionsAroundTheTeamSections(t *testing.T) {
	config := prompts.Config{Communication: "Say little.", Scope: "", Delivery: "Attach screenshots."}
	for _, role := range []string{prompts.Orchestrator, prompts.Worker} {
		got := names(prompts.Parts(config, role, "env", ""))
		last := "runtime/work"
		if role == prompts.Orchestrator {
			last = "runtime/dispatch"
		}
		want := []string{"runtime/shared", "runtime/" + role, "communication", "delivery", "runtime/subscriptions", "runtime/replies", last}
		if role == prompts.Orchestrator {
			want = append(want, "runtime/admission")
		}
		want = append(want, "runtime/handoff", "runtime/environment")
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%s: %v, want %v", role, got, want)
		}
		prompt := prompts.Compose(config, role, "", "")
		own := prompts.Master
		if role == prompts.Worker {
			own = prompts.WorkerPrompt
		}
		for _, fixed := range []string{prompts.Shared, own, prompts.Guides["subscriptions"], prompts.Guides["replies"], prompts.Handoff(role)} {
			if !strings.Contains(prompt, fixed) {
				t.Fatalf("%s prompt lacks %q", role, fixed[:40])
			}
		}
		if !strings.HasSuffix(prompt, prompts.Handoff(role)) {
			t.Fatalf("%s prompt does not end with its handoff", role)
		}
	}
	if !strings.Contains(prompts.Compose(config, prompts.Orchestrator, "", ""), prompts.Admission) {
		t.Fatal("the orchestrator prompt lacks admission")
	}
	if strings.Contains(prompts.Compose(config, prompts.Worker, "", ""), prompts.Admission) {
		t.Fatal("the worker prompt has admission")
	}
}

func TestReadsAPromptFileWhateverItsLineEndsAndBlankLinesAndNamesAMissingOrEmptyOne(t *testing.T) {
	fsys := fstest.MapFS{"crlf.md": {Data: []byte("One.\r\nTwo. \r\n \t\r\n\r\nThree.\r\n")}, "blank.md": {Data: []byte(" \n\t\n")}}
	if got := prompts.File(fsys, "crlf"); got != "One. Two.\nThree." {
		t.Fatalf("got %q", got)
	}
	for name, want := range map[string]string{"blank": "Empty packaged file: blank.md", "absent": "Missing packaged file: absent.md"} {
		func() {
			defer func() {
				if r := recover(); r != want {
					t.Fatalf("%s: got %v, want %q", name, r, want)
				}
			}()
			prompts.File(fsys, name)
		}()
	}
}

func TestLeavesTheUIBeforeAndAfterEvidenceToTheDeliveryRequirementsWhichATeamCanChange(t *testing.T) {
	config := prompts.Defaults()
	config.Delivery = "Only capture the finished screen."
	for _, role := range []string{prompts.Orchestrator, prompts.Worker} {
		if strings.Contains(prompts.Compose(config, role, "", ""), "Before and After visual evidence") {
			t.Fatalf("%s prompt names the default evidence", role)
		}
	}
}

func TestNamesTheMulticaTicketCommandsInPlaceOfDSHTools(t *testing.T) {
	for _, role := range []string{prompts.Orchestrator, prompts.Worker} {
		prompt := prompts.Compose(prompts.Defaults(), role, prompts.Environment, "")
		if strings.Contains(prompt, "orchestrator_") {
			t.Fatalf("%s prompt still names a DSH tool", role)
		}
	}
	if !strings.Contains(prompts.Compose(prompts.Defaults(), prompts.Orchestrator, "", ""), "multica ticket route") {
		t.Fatal("the orchestrator prompt does not name multica ticket route")
	}
	if !strings.Contains(prompts.Compose(prompts.Defaults(), prompts.Worker, "", ""), "multica ticket report") {
		t.Fatal("the worker prompt does not name multica ticket report")
	}
}

func TestThePolicySectionNamesTheAgentEachRuleSendsItsTicketsTo(t *testing.T) {
	settings := policy.MustParseSettings(`{"policy": {"rules": [{"id": "copy", "name": "文案", "enabled": true, "instruction": "改文案", "target": {"type": "agent", "id": "a-fe"}},
		{"id": "visual", "name": "样式", "enabled": true, "instruction": "改样式"}]}}`)
	section := prompts.PolicySection(settings, func(id string) string { return map[string]string{"a-fe": "前端助手"}[id] })
	for _, want := range []string{"- 文案 (copy): 改文案 → 前端助手", "- 样式 (visual): 改样式 → the default worker agent", "pass rules"} {
		if !strings.Contains(section, want) {
			t.Fatalf("section lacks %q:\n%s", want, section)
		}
	}
}

// MUL-13576 (C-05, C-09, B-28): a message in another thread that names a ticket's Issue or PR attaches to it; the
// Router never ignores a request as a duplicate of another thread's ticket.
func TestTheRouterAttachesAMessageThatNamesATicketsIssueAndNeverIgnoresOneAsADuplicate(t *testing.T) {
	prompt := prompts.Compose(prompts.Defaults(), prompts.Orchestrator, "", "")
	for _, want := range []string{
		"names the same Issue or PR identifier as the ticket's source message or acknowledgement",
		"Never ignore a request as a duplicate of another thread's ticket",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("router prompt lacks %q", want)
		}
	}
	if strings.Contains(prompt, "accept or ignore it, never attach it") {
		t.Fatal("router prompt still offers ignore for a similar request")
	}
}
