package views

import (
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

var emptyParen = regexp.MustCompile(`\s*[（(][^（）()]*?(?:id|ID|Id)?[\s:：=]*[）)]`)

// readableIDs shows a worker's text with each uuid named: an issue by its number, an agent or squad by its
// name; one that resolves to nothing is cut, with the brackets it leaves empty.
func (v *View) readableIDs(text string) string {
	if !uuidLike.MatchString(text) {
		return text
	}
	out := uuidLike.ReplaceAllStringFunc(text, func(id string) string {
		if link := v.issue(id); link != nil {
			return link.Identifier
		}
		if v.names.Target != nil {
			if name := v.names.Target(core.Target{Type: "agent", ID: id}); name != "" {
				return name
			}
			if name := v.names.Target(core.Target{Type: "squad", ID: id}); name != "" {
				return name
			}
		}
		return ""
	})
	out = emptyParen.ReplaceAllStringFunc(out, func(m string) string {
		inner := strings.Trim(strings.TrimSpace(m), "（）()")
		if strings.Trim(inner, " :：=idID") == "" || strings.HasSuffix(strings.TrimSpace(inner), "id") {
			return ""
		}
		return m
	})
	return out
}
