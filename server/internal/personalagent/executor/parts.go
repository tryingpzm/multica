// Package executor runs tickets' Assignments on Multica (SA src/executor/dsh.ts): a ticket is a Multica issue
// assigned to the personal agent's Worker agent, an Assignment's launch and each delivery of Inputs is a comment that
// starts a run, and stop cancels the run. A run's end is the end of the worker's turn; one that reported nothing is
// reported waiting with the worker's last words (design: "Report"), and what a worker reports reaches the Router.
package executor

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/effects"
	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

// MaxWorkers is SA's default number of worker slots of a personal agent.
const MaxWorkers = 3

// TransientTries is how many passing failures a launch or run may meet before its ticket fails.
const TransientTries = 3

var passing = regexp.MustCompile(`(?i)ECONNRESET|ECONNREFUSED|ETIMEDOUT|EAI_AGAIN|ENOTFOUND|EPIPE|UND_ERR|socket hang up|fetch failed|network error|overloaded|rate limit|\b(?:408|429|50[0-4])\b`)

// StatusError is an error that carries an HTTP status.
type StatusError interface{ StatusCode() int }

// Transient reports whether an error is a passing network or provider failure: an HTTP 408, 429 or 5xx, or a
// connection refused, reset, timed out or unresolved.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	var status StatusError
	if errors.As(err, &status) {
		code := status.StatusCode()
		return code == 408 || code == 429 || code >= 500
	}
	return passing.MatchString(err.Error())
}

// Slots tracks the worker slots of one personal agent: a ticket holds one while its worker runs and while a launch or
// wake starts it. A wake for a person's message may take one slot beyond the limit.
type Slots struct {
	mu       sync.Mutex
	starting map[string]string
	running  func() []string
	max      func() int
}

// NewSlots returns Slots over the tickets whose worker runs and the limit.
func NewSlots(running func() []string, max func() int) *Slots {
	return &Slots{starting: map[string]string{}, running: running, max: max}
}

func (s *Slots) used(running []string) int {
	seen := map[string]bool{}
	for _, id := range running {
		seen[id] = true
	}
	for id := range s.starting {
		seen[id] = true
	}
	return len(seen)
}

// Used is the slots running workers and starting reservations take.
func (s *Slots) Used() int {
	running := s.running()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.used(running)
}

// Launches is how many launches may run at once: the slots no running worker or wake holds.
func (s *Slots) Launches() int {
	running := s.running()
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := map[string]bool{}
	for _, id := range running {
		seen[id] = true
	}
	for id, by := range s.starting {
		if by != "launch" {
			seen[id] = true
		}
	}
	return s.max() - len(seen)
}

// Reserve takes a starting slot for a ticket by "launch", "wake" or "person"; it returns a Later error when none is free.
func (s *Slots) Reserve(ticketID, by string) error {
	running := s.running()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.starting[ticketID]; ok {
		return nil
	}
	extra := 0
	if by == "person" {
		extra = 1
	}
	if s.used(running) >= s.max()+extra {
		return effects.NewLater("No worker slot is free")
	}
	s.starting[ticketID] = by
	return nil
}

// Free releases a ticket's starting slot.
func (s *Slots) Free(ticketID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.starting, ticketID)
}

// MandateInstructions are the limits an inferred mandate sets, for the worker's instructions; "" for an explicit one.
func MandateInstructions(m core.Mandate) string {
	if m.Strength != "inferred" {
		return ""
	}
	return "This ticket has limited proactive authorization. Independently verify that it is a small change " +
		"internal to one module within the accepted rule scope. If it requires public-interface or behavior changes, new interaction, product " +
		"decisions or clarification, stop and report dropped; do not expand scope."
}

// commentOnIssue is how a Worker carries out a procedure that comments on an existing issue and @s its executor
// (修正补充 branch A Workers dropped or blocked instead, held back by the template's "do not reply in issues").
const commentOnIssue = "\nWhen the procedure says to comment on an existing issue (<原单>) and @ its executor, do it yourself; the template's " +
	"\"不在 issue 里回复别人\" does not apply to this comment. Find the executor with \"$MULTICA_CLI_PATH\" issue get <原单> --output json " +
	"(assignee_type agent → assignee_id; a squad → its leader agent). Write the comment to ./supplement.md in your working directory, " +
	"starting with [@<执行方名>](mention://agent/<assignee_id>), then the correction, the source message and its screenshots; post it with " +
	"\"$MULTICA_CLI_PATH\" issue comment add <原单> --content-file ./supplement.md --output json, and put the returned comment id in your report."

var worktreeName = regexp.MustCompile(`^[a-z]+(?:-[a-z0-9]+){0,3}$`)

// TicketExecution is a newly accepted ticket's Execution: its name and the assignment text. A ticket runs on a Multica
// issue, so it has no directory of its own. The source Trigger goes with the assignment, so the text does not repeat it.
func TicketExecution(ticket core.Ticket, outcome core.Outcome, rules []policy.Rule) (core.Execution, error) {
	if len(outcome.Name) > 32 || !worktreeName.MatchString(outcome.Name) {
		return core.Execution{}, fmt.Errorf("Use 1–4 short English words in lowercase kebab-case, at most 32 characters")
	}
	id := ticket.ID
	if len(id) > 12 {
		id = id[:12]
	}
	note := ""
	if outcome.Note != "" {
		note = "\nRouting note (not the source request): " + outcome.Note
	}
	ids := ticket.Mandate.Basis.Rules
	matched := ""
	for _, r := range rules {
		for _, want := range ids {
			if r.ID == want {
				matched += "\n- " + r.Name + " (" + r.ID + "): " + r.Instruction
			}
		}
	}
	precedence := ""
	if matched != "" {
		precedence = "\nMatched rules; follow their procedure for this ticket: it takes precedence over the default worker behaviour (implementing and opening a PR yourself, reporting dropped, not commenting on or creating issues) and over the default acknowledgement and messaging policy:" + matched + commentOnIssue
	}
	assignment := strings.TrimSpace(note)
	if ticket.Mandate.Strength == "inferred" && len(ids) > 0 {
		assignment = "Proactively accepted under " + strings.Join(ids, ", ") + ". Scope: " + outcome.Scope + "." + precedence + note
	} else if precedence != "" {
		// personal-agent an explicit ticket the Router matched to rules follows their procedure too
		// (修正补充 finds the in-progress issue and comments there instead of starting work of its own).
		assignment = strings.TrimSpace(note + precedence)
	}
	var target core.Target
	for _, want := range ids {
		for _, r := range rules {
			if target.ID == "" && r.ID == want && r.Target.ID != "" {
				target = core.Target{Type: r.Target.Type, ID: r.Target.ID}
			}
		}
	}
	return core.Execution{Name: outcome.Name + "-" + id, Assignment: assignment, Target: target}, nil
}
