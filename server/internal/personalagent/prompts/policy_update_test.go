package prompts_test

// MUL-13396: the Router's Session keeps its context, so a Policy changed after its prompt was sent reaches it as a
// Policy update; the same Policy is not sent twice.

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/prompts"
)

func TestAChangedPolicyIsSentToTheRouterOnce(t *testing.T) {
	before := policy.MustParseSettings(`{}`)
	prompt := prompts.Compose(prompts.Defaults(), prompts.Orchestrator, prompts.Environment, prompts.PolicySection(before, nil))
	if _, ok := prompts.PolicyUpdate([]string{prompt}, before, nil); ok {
		t.Fatal("the Policy the prompt carries is sent again")
	}
	after := policy.MustParseSettings(`{"policy":{"rules":[{"id":"status","name":"状态询问","enabled":true,"instruction":"问进度"}]}}`)
	update, ok := prompts.PolicyUpdate([]string{prompt}, after, nil)
	if !ok || !strings.Contains(update, "状态询问 (status): 问进度") || strings.Contains(update, "(copy)") ||
		!strings.Contains(update, policy.PolicyVersion(after)) {
		t.Fatalf("update %v %q", ok, update)
	}
	// Once sent, the latest Policy is the update's.
	if _, ok := prompts.PolicyUpdate([]string{prompt, "a card", update}, after, nil); ok {
		t.Fatal("the update is sent twice")
	}
}
