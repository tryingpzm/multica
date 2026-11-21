// Package ticketskill is the personal-agent-ticket skill: the Workers' instructions (SA prompts/worker.md and the
// Worker parts of work.md and handoff.md, in Chinese, without the Router's part). A run on a ticket's issue is given it,
// whichever agent or squad member runs, so the issue no longer carries the instructions.
package ticketskill

import (
	_ "embed"
	"strings"
)

// Name is the skill's name.
const Name = "personal-agent-ticket"

//go:embed SKILL.md
var content string

// Content is SKILL.md.
func Content() string { return content }

// Description is the frontmatter's description.
func Description() string {
	for _, line := range strings.Split(content, "\n") {
		if d, ok := strings.CutPrefix(line, "description: "); ok {
			return d
		}
	}
	return ""
}
