package core

import (
	"regexp"
	"strings"
)

// Reasons are the structured reason codes and templates for decisions, filtered triggers and ended assignments.
var Reasons = map[string]string{
	"said":               "{text}",
	"timeout":            "Assessment timed out",
	"policyChanged":      "Policy changed during assessment",
	"noAdmissible":       "No admissible trigger",
	"startedTicket":      "Already started ticket {ticket}",
	"acceptedBefore":     "Trigger {trigger} was accepted before",
	"routed":             "Routed to ticket {ticket}",
	"explicitRouted":     "Explicit request from an allowed sender routed by the orchestrator",
	"humanStop":          "Human asked to stop",
	"legacyImport":       "Imported from the pre-domain-model task record",
	"forwarded":          "Forwarded to the worker of the Ticket already on this Thread",
	"senderNotMember":    "The sender is not on the Source's list",
	"retried":            "Retried by a person",
	"hostRestarted":      "dsh-superagent restarted",
	"stopUnknown":        "Stop target is unknown or already ended",
	"stopNotExplicit":    "A stop needs an explicit request from a sender on the list",
	"directionUnsettled": "Implementation intent or target direction is not established, or a product/design decision is still required",
	"noRuleMatched":      "No enabled admission rule matched",
	"ignoredFromCard":    "Ignored from its card, without a rule assessment",
	"observed":           "Observe mode: would accept ({text})",
}

// Reason is a reason code with template values; TriggerID identifies the trigger in a batch the reason applies to.
type Reason struct {
	Code      string            `json:"code"`
	Values    map[string]string `json:"values,omitempty"`
	TriggerID string            `json:"triggerId,omitempty"`
}

// Said is a reason in the words its assessor or person wrote.
func Said(text string) Reason { return Reason{Code: "said", Values: map[string]string{"text": text}} }

var placeholder = regexp.MustCompile(`\{(\w+)\}`)

func fillTemplate(template string, values map[string]string) string {
	return placeholder.ReplaceAllStringFunc(template, func(match string) string {
		if v, ok := values[match[1:len(match)-1]]; ok {
			return v
		}
		return match
	})
}

// ReasonText formats reasons in English, prefixed by TriggerID when present.
func ReasonText(reasons ...Reason) string {
	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		template, ok := Reasons[reason.Code]
		if !ok {
			template = reason.Code
		}
		text := fillTemplate(template, reason.Values)
		if reason.TriggerID != "" {
			text = reason.TriggerID + ": " + text
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "; ")
}
