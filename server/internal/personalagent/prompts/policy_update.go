package prompts

// personal-agent the Router's Session keeps its context, so the Policy section its prompt carried goes
// stale when the rules change; a changed Policy is sent to it as an update that replaces the earlier one.

import (
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

const policyUpdateHead = "The Policy changed. This section replaces the earlier Policy section; assess every candidate from now on against it only.\n\n"

// PolicyUpdate returns the Policy update to send to a Session whose messages so far are sent, oldest first; ok is false
// when the latest Policy they carry is already s's.
func PolicyUpdate(sent []string, s policy.Settings, agentName func(id string) string) (string, bool) {
	section := PolicySection(s, agentName)
	for i := len(sent) - 1; i >= 0; i-- {
		if at := strings.LastIndex(sent[i], "## Policy\n"); at >= 0 {
			if strings.HasPrefix(sent[i][at:], section) {
				return "", false
			}
			break
		}
	}
	return policyUpdateHead + section, true
}
