// Package prompts assembles the Router's and the Workers' system prompts from dsh-superagent-prototype's prompts/
// (src/app/prompts.ts, src/app/context.ts, src/app/skill-files.ts). The texts are SA's, with its orchestrator_* tool
// names replaced by the multica ticket commands that do the same; the team sections take SA's defaults.
package prompts

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

//go:embed text
var packaged embed.FS

// Text is the packaged prompt files.
var Text, _ = fs.Sub(packaged, "text")

var paragraphBreak = regexp.MustCompile(`\n(?:[ \t]*\n)+`)

// File reads a prompt file of fsys: paragraphs split at blank lines, each paragraph's lines trimmed and joined by
// spaces, whatever its line ends. A missing or empty file panics naming it.
func File(fsys fs.FS, name string) string {
	file := name + ".md"
	data, err := fs.ReadFile(fsys, file)
	if err != nil {
		panic(fmt.Sprintf("Missing packaged file: %s", file))
	}
	text := strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n"))
	if text == "" {
		panic(fmt.Sprintf("Empty packaged file: %s", file))
	}
	paragraphs := paragraphBreak.Split(text, -1)
	for i, p := range paragraphs {
		lines := strings.Split(p, "\n")
		for j, line := range lines {
			lines[j] = strings.TrimSpace(line)
		}
		paragraphs[i] = strings.Join(lines, " ")
	}
	return strings.Join(paragraphs, "\n")
}

func file(name string) string { return File(Text, name) }

// Roles.
const (
	Orchestrator = "orchestrator"
	Worker       = "worker"
)

// Built-in texts.
var (
	Shared        = file("shared")
	Master        = file("orchestrator")
	WorkerPrompt  = file("worker")
	CLINotes      = file("cli")
	Admission     = file("admission")
	Guides        = map[string]string{"subscriptions": file("subscriptions"), "replies": file("replies"), "dispatch": file("dispatch"), "work": file("work")}
	handoffShared = file("handoff")
	handoffRole   = map[string]string{Orchestrator: file("handoff-orchestrator"), Worker: file("handoff-worker")}
)

// Handoff is a role's handoff policy.
func Handoff(role string) string { return handoffShared + "\n" + handoffRole[role] }

// Config is the team sections; an empty one leaves the prompt.
type Config struct {
	Communication string `json:"communication"`
	Scope         string `json:"scope"`
	Delivery      string `json:"delivery"`
}

// Defaults are SA's team sections.
func Defaults() Config {
	return Config{Communication: file("team/communication"), Scope: file("team/scope"), Delivery: file("team/delivery")}
}

var teamHeadings = [][2]string{{"communication", "Communication"}, {"scope", "Ticket scope"}, {"delivery", "Delivery requirements"}}

func (c Config) section(name string) string {
	switch name {
	case "communication":
		return c.Communication
	case "scope":
		return c.Scope
	}
	return c.Delivery
}

// Part is one section of a prompt.
type Part struct {
	Name string
	Kind string
	Text string
}

const policyHead = "## Policy\nPolicy version %s; accept mode %s. The accept mode and the rules decide proactive candidates only: an explicit request is routed as dispatch says, in any mode and whatever the rules."

// rulesRoute says how rules choose the agent a ticket goes to.
const rulesRoute = "Each rule ends with the agent its tickets go to. When you accept, pass rules with the ids of the rules the request falls under, most specific first, explicit requests included: the first one that names an agent decides who works the ticket, and with none the default worker agent does."

// outputLanguage is the language rule for what the Router writes, by the owner's setting.
var outputLanguage = map[string]string{
	"zh": "Write every reason, acknowledgement, title, scope and rule_assessments reason in Simplified Chinese (简体中文); keep ids, worktree_name and quoted source text as they are.",
	"en": "Write every reason, acknowledgement, title, scope and rule_assessments reason in English; keep ids, worktree_name and quoted source text as they are.",
}

// PolicySection is the Router's Policy section: version, accept mode and each enabled rule on one line, with the agent
// its tickets go to; agentName names an agent id ("" or nil: the id itself).
func PolicySection(s policy.Settings, agentName func(id string) string) string {
	line := regexp.MustCompile(`\s*\n\s*`)
	var rules []string
	for _, r := range s.Policy.Rules {
		if r.Enabled {
			to := "the default worker agent"
			if r.Target.ID != "" {
				to = r.Target.ID
				if agentName != nil && agentName(r.Target.ID) != "" {
					to = agentName(r.Target.ID)
				}
				if r.Target.Type == "squad" {
					to = "squad " + to
				}
			}
			rules = append(rules, "- "+line.ReplaceAllString(r.Name, " ")+" ("+r.ID+"): "+line.ReplaceAllString(r.Instruction, " ")+" → "+to)
		}
	}
	head := fmt.Sprintf(policyHead, policy.PolicyVersion(s), s.Eval.Mode) + " " + outputLanguage[s.Eval.Language]
	if len(rules) == 0 {
		return head + " No rule is enabled: a proactive candidate can only be ignored."
	}
	return head + " " + rulesRoute + " The enabled rules, by name and id:\n" + strings.Join(rules, "\n")
}

// Parts assembles a role's sections in order: fixed texts around the team sections, the environment, and last the
// Router's Policy section.
func Parts(c Config, role, environment, policySection string) []Part {
	fixed := func(name, text string) Part { return Part{Name: name, Kind: "fixed", Text: text} }
	own := Master
	if role == Worker {
		own = WorkerPrompt
	}
	parts := []Part{fixed("runtime/shared", Shared), fixed("runtime/"+role, own)}
	for _, h := range teamHeadings {
		if text := c.section(h[0]); text != "" {
			parts = append(parts, Part{Name: h[0], Kind: "team", Text: "## " + h[1] + "\n" + text})
		}
	}
	last := "work"
	if role == Orchestrator {
		last = "dispatch"
	}
	for _, name := range []string{"subscriptions", "replies", last} {
		parts = append(parts, fixed("runtime/"+name, "## "+name+"\n"+Guides[name]))
	}
	if role == Orchestrator {
		parts = append(parts, fixed("runtime/admission", Admission))
	}
	parts = append(parts, fixed("runtime/handoff", Handoff(role)))
	if environment != "" {
		parts = append(parts, Part{Name: "runtime/environment", Kind: "generated", Text: environment})
	}
	if role == Orchestrator && policySection != "" {
		parts = append(parts, Part{Name: "runtime/policy", Kind: "generated", Text: policySection})
	}
	return parts
}

// Compose is a role's whole prompt.
func Compose(c Config, role, environment, policySection string) string {
	parts := Parts(c, role, environment, policySection)
	texts := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.Text
	}
	return strings.Join(texts, "\n\n")
}

// Environment is the Multica execution environment section: where a ticket's Worker runs and how it reports.
const Environment = "Execution environment: each ticket is a Multica issue assigned to this personal agent's Worker agent, " +
	"and each delivery is a comment on it that starts a run. Use the multica CLI (\"$MULTICA_CLI_PATH\") for every ticket command; " +
	"the ticket commands take their arguments as JSON with --input-file <path>, written inside your working directory."
