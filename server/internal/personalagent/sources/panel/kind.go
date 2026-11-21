// Package panel is the built-in Source for annotations a person writes on an item in the personal agent panel: an
// annotation is an instruction (design Q4/Q5), so its Trigger is the owner's own explicit request on that Thread.
package panel

import (
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
)

// Source is the kind's ID.
const Source = "panel"

// Annotation is one pa_annotation row.
type Annotation struct {
	ID string
	// Thread is the Thread the annotation was written on.
	Thread core.ThreadRef
	Owner  string
	Text   string
	At     time.Time
}

// TriggerID is the Trigger of an annotation.
func TriggerID(id string) string { return "panel:" + id }

// Trigger converts an annotation into an explicit Trigger from the owner, on the annotated Thread.
func Trigger(a Annotation) core.Trigger {
	return core.Trigger{
		ID: TriggerID(a.ID), Source: Source, Threads: []core.ThreadRef{a.Thread},
		Actor: core.ActorRef{Source: Source, ID: a.Owner}, Human: true, Addressed: "direct",
		At: a.At.UTC().Format("2006-01-02T15:04:05.000Z"), Text: a.Text,
		Meta: map[string]any{"annotationId": a.ID},
	}
}

// Kind is the panel kind: it listens to every annotation and has no channel (replies go to the annotated Thread's own).
func Kind() sources.Kind {
	return sources.Kind{
		Kind:        Source,
		Title:       sources.Localized{Zh: "面板批注", En: "Panel annotations"},
		Description: sources.Localized{Zh: "你在面板里给事项写的批注，作为明确的指令。", En: "Annotations you write on items in the panel, as explicit instructions."},
		Schema:      func(map[string]any) (sources.Config, error) { return sources.Config{}, nil },
		Health:      []string{},
		Policy: &sources.PolicyPart{
			Schema: func(raw map[string]any) (sources.Config, error) {
				out, err := sources.PeoplePolicy(raw)
				return out, err
			},
			Explicit: sources.Localized{Zh: "批注只能由你本人写，总是明确的请求。", En: "Only you write annotations; each is an explicit request."},
		},
	}
}
