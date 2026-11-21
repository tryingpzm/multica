package prompts_test

// MUL-13396: the Router writes its reasons, acknowledgements, titles and scopes in the language the owner set, Simplified
// Chinese by default; a change of language reaches a running Session as a Policy update.

import (
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
	"github.com/multica-ai/multica/server/internal/personalagent/prompts"
)

func TestTheRouterWritesInSimplifiedChineseByDefault(t *testing.T) {
	zh := prompts.PolicySection(policy.MustParseSettings(`{}`), nil)
	if !strings.Contains(zh, "Simplified Chinese") || !strings.Contains(zh, "rule_assessments") {
		t.Fatalf("default section has no Chinese output rule:\n%s", zh)
	}
	en := prompts.PolicySection(policy.MustParseSettings(`{"eval":{"language":"en"}}`), nil)
	if strings.Contains(en, "Simplified Chinese") || !strings.Contains(en, "English") {
		t.Fatalf("en section:\n%s", en)
	}
	if _, ok := prompts.PolicyUpdate([]string{zh}, policy.MustParseSettings(`{"eval":{"language":"en"}}`), nil); !ok {
		t.Fatal("a language change is not sent")
	}
	if _, err := policy.ParseSettings([]byte(`{"eval":{"language":"fr"}}`)); err == nil {
		t.Fatal("an unknown language is accepted")
	}
}
