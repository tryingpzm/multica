package core

// UserErrors maps user-facing error codes to their category ('missing' or 'refused') and English message template.
var UserErrors = map[string][2]string{
	"unknownTicket":        {"missing", "Unknown ticket"},
	"unknownProblem":       {"missing", "Unavailable Eval not found"},
	"unknownTrigger":       {"missing", "Unknown trigger"},
	"unknownAssignment":    {"missing", "Unknown assignment"},
	"ticketEnded":          {"refused", "Ticket has ended"},
	"assignmentEnded":      {"refused", "Assignment has ended; a newer assignment owns this ticket"},
	"evalFinished":         {"refused", "Eval {eval} has finished or a later eval replaced it; decide the trigger's current eval"},
	"notAdmissible":        {"refused", "Trigger is not admissible: {reason}"},
	"notExplicit":          {"refused", "An accept through multica ticket route is for explicit requests from allowed senders; assess proactive candidates with multica ticket eval and multica ticket decide"},
	"ticketStopped":        {"refused", "A person stopped this ticket; new work needs an accept through multica ticket route"},
	"mandateRevoked":       {"refused", "The current policy no longer holds this ticket's mandate; new work needs an accept through multica ticket route"},
	"notRetryable":         {"refused", "Only an open ticket can be retried, and only when its worker last reported failed or blocked and no person has sent a message since, or when delivering an Input to its worker failed"},
	"sourceThread":         {"refused", "This is the thread the ticket started on; it stays subscribed"},
	"followedThread":       {"refused", "This thread is the ticket's own, such as its pull request's; it stays subscribed"},
	"notFollowing":         {"refused", "This ticket does not follow that thread"},
	"questionWithResult":   {"refused", "A question for a person cannot also be a ready result or an answer; report it separately"},
	"askerWithoutQuestion": {"refused", "The message that raised a question needs the question it raised"},
	"notAsker":             {"refused", "A question must name the person's message that raised it, which reached this ticket"},
	"notAnswerable":        {"refused", "An answer must name the person's question it answers, which reached this ticket"},
	"ackEnded":             {"refused", "Ticket ended; do not acknowledge historical requests"},
	"notReached":           {"refused", "Trigger does not belong to this worker"},
	"handedBack":           {"refused", "This ticket handed that trigger back already"},
	"handedBackHere":       {"refused", "The worker of this ticket handed that trigger back as beyond it: accept it as new work, attach it to another ticket, or ignore it"},
	"workerDecides":        {"refused", "This message reached ticket {tickets} on its thread: its worker decides whether it belongs there and hands back what does not, which then arrives as a candidate. Only a stop goes through multica ticket route, with kind stop"},
	"ackNoChannel":         {"refused", "Acknowledgement requires an assigned human source message"},
	"ackNotPerson":         {"refused", "Use the original human trigger, not a bot, CI or worker report"},
	"notReady":             {"refused", "Ticket is not ready for delivery. Keep progress internal and wait for a verified ready report; do not relabel progress as an answer or send it through another channel."},
	"notAttention":         {"refused", "Attention requires the ticket's current attention question in human_action. Routine unfinished work stays internal."},
	"notQuestion":          {"refused", "An answer must target the original human question, not a worker report or automated trigger"},
	"noAnswer":             {"refused", "No worker answer is ready for this human trigger. Wait for the worker's report, or request one with multica ticket request-status, which acts only on a worker that reported waiting; ordinary progress cannot be relabelled as an answer."},
	"noChannel":            {"refused", "No reply channel for this ticket or trigger"},
}

// UserError is the structured error Engine parts return when a request is invalid or references missing state.
type UserError struct {
	Code    string
	Values  map[string]string
	Missing bool
	message string
}

// NewUserError builds the UserError for code with its template values.
func NewUserError(code string, values map[string]string) *UserError {
	entry, ok := UserErrors[code]
	if !ok {
		entry = [2]string{"refused", code}
	}
	return &UserError{Code: code, Values: values, Missing: entry[0] == "missing", message: fillTemplate(entry[1], values)}
}

func (e *UserError) Error() string { return e.message }

// refuse throws a UserError inside an Engine command; Engine turns it into a returned error after rolling back.
func refuse(code string, values ...string) {
	m := map[string]string{}
	for i := 0; i+1 < len(values); i += 2 {
		m[values[i]] = values[i+1]
	}
	panic(NewUserError(code, m))
}
