package sources

import (
	"context"
	"errors"
)

// WindowEntry is one selected message of a thread window with its 1-based position and the messages skipped before it.
type WindowEntry struct {
	Message       ContextMessage `json:"message"`
	Position      int            `json:"position"`
	OmittedBefore int            `json:"omitted_before"`
}

// Window is the bounded message window around a trigger (SA threadWindow).
type Window struct {
	Entries        []WindowEntry `json:"entries"`
	SourceFound    bool          `json:"source_found"`
	LoadedMessages int           `json:"loaded_messages"`
	OmittedAfter   int           `json:"omitted_after"`
}

// ThreadWindow selects the first five messages plus five on either side of sourceID, counting the gaps.
func ThreadWindow(messages []ContextMessage, sourceID string) Window {
	trigger := -1
	for i, m := range messages {
		if m.ID == sourceID {
			trigger = i
			break
		}
	}
	w := Window{Entries: []WindowEntry{}, SourceFound: trigger >= 0, LoadedMessages: len(messages)}
	previous := -1
	for i, m := range messages {
		near := trigger >= 0 && abs(i-trigger) <= 5
		if i < 5 || near {
			w.Entries = append(w.Entries, WindowEntry{Message: m, Position: i + 1, OmittedBefore: i - previous - 1})
			previous = i
		}
	}
	w.OmittedAfter = len(messages) - previous - 1
	return w
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// AdmissionContextResult is the loaded snapshot an assessment reads; counts describe only what was loaded.
type AdmissionContextResult struct {
	Messages []ContextMessage `json:"messages"`
	Window   Window           `json:"window"`
	Complete bool             `json:"complete"`
	Next     string           `json:"next,omitempty"`
	Status   string           `json:"status,omitempty"`
}

// ErrRepeatedCursor is returned when a thread's pagination repeats a cursor.
var ErrRepeatedCursor = errors.New("Thread pagination repeated a cursor; context is incomplete")

// AdmissionContext reads up to maxPages of a trigger's thread and builds the window around the trigger (SA admissionContext).
func AdmissionContext(ctx context.Context, tc *TriggerContext, maxPages int) (AdmissionContextResult, error) {
	messages := []ContextMessage{}
	seen := map[string]bool{}
	cursors := map[string]bool{}
	next, status := "", ""
	more := true
	for page := 0; more && page < maxPages; page++ {
		part, err := tc.Read(ctx, PartThread, next)
		if err != nil {
			return AdmissionContextResult{}, err
		}
		for _, m := range part.Messages {
			if !seen[m.ID] {
				seen[m.ID] = true
				messages = append(messages, m)
			}
		}
		if status == "" {
			status = part.Status
		}
		next = part.Next
		more = next != ""
		if more && cursors[next] {
			return AdmissionContextResult{}, ErrRepeatedCursor
		}
		cursors[next] = true
	}
	result := AdmissionContextResult{Messages: messages, Window: ThreadWindow(messages, tc.MessageID), Complete: !more, Status: status}
	if more {
		result.Next = next
	}
	return result, nil
}
