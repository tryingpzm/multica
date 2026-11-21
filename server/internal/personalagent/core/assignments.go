package core

// WorkerReport is what an Assignment reports. Question: nil keeps the current one, a pointer to "" clears it (SA null),
// otherwise asks it.
type WorkerReport struct {
	State    ReportState
	Summary  string
	Question *string
	AskedBy  string
	Answers  string
	// Unreported marks the waiting report the host files for a run that ended without the worker reporting.
	Unreported bool
}

const recoveryNote = "The host restarted during this ticket. Inspect the current worktree, git history and GitHub state before continuing. Do not repeat an already completed external operation. Finish the original assignment and report its status."

// Assignments records what the Executor and its workers report about the Assignments that run tickets.
type Assignments struct {
	base
	tickets *Tickets
	replies *Replies
}

func (p *Assignments) started(ticketID, assignmentID string) {
	ticket := p.require(ticketID)
	var assignment *Assignment
	for _, a := range ticket.Assignments {
		if a.ID == assignmentID {
			assignment = a
		}
	}
	if assignment == nil || assignment.State == "ended" {
		return
	}
	assignment.State = "running"
	if assignment.StartedAt == "" {
		assignment.StartedAt = p.now()
	}
	ticket.UpdatedAt = p.now()
	p.repo().Tickets().Save(*ticket)
	if LiveTicket(*ticket) && len(ticket.Undelivered) > 0 {
		p.options.Executor.Deliver(*ticket, *assignment)
	}
	p.emit("assignment.started", map[string]any{"ticketId": ticketID, "assignmentId": assignmentID})
}

func (p *Assignments) delivered(ticketID, assignmentID string, inputIDs []string) {
	ticket := p.running(ticketID, assignmentID)
	if ticket == nil {
		return
	}
	ticket.Undelivered = without(ticket.Undelivered, inputIDs...)
	ticket.UpdatedAt = p.now()
	RunningAssignment(ticket).DeliveredAt = ticket.UpdatedAt
	for _, input := range ticket.Inputs {
		if input.Kind == "trigger" && contains(inputIDs, input.ID) {
			if input.ReadAt == nil {
				empty := ""
				input.ReadAt = &empty
			}
			input.DeliveredTo = assignmentID
		}
	}
	ticket.Delivery = nil
	p.repo().Tickets().Save(*ticket)
}

func (p *Assignments) read(ticketID string, inputIDs []string) {
	ticket := p.require(ticketID)
	if !LiveTicket(*ticket) {
		return
	}
	for _, input := range ticket.Inputs {
		if input.Kind == "trigger" && contains(inputIDs, input.ID) && (input.ReadAt == nil || *input.ReadAt == "") {
			now := p.now()
			input.ReadAt = &now
		}
	}
	p.repo().Tickets().Save(*ticket)
}

func (p *Assignments) turnEnded(ticketID, assignmentID string) {
	ticket := p.running(ticketID, assignmentID)
	if ticket == nil {
		return
	}
	RunningAssignment(ticket).IdleAt = p.now()
	p.repo().Tickets().Save(*ticket)
}

func (p *Assignments) deliveryFailed(ticketID, assignmentID, message string) {
	ticket := p.running(ticketID, assignmentID)
	if ticket == nil {
		return
	}
	if r := []rune(message); len(r) > 300 {
		message = string(r[:300])
	}
	ticket.Delivery = &Delivery{Error: message, At: p.now()}
	p.repo().Tickets().Save(*ticket)
}

func (p *Assignments) interrupted(ticketID, assignmentID string) {
	ticket := p.running(ticketID, assignmentID)
	if ticket == nil {
		return
	}
	running := RunningAssignment(ticket)
	if running == nil {
		return
	}
	report := running.Report
	busy := running.DeliveredAt != "" && report != nil && running.DeliveredAt >= report.At
	settled := report != nil && contains([]string{"waiting", "ready", "blocked", "failed"}, report.State) && !busy
	if !settled {
		p.tickets.restart(ticket, running, "hostRestarted", recoveryNote)
	} else if len(ticket.Undelivered) > 0 {
		p.options.Executor.Deliver(*ticket, *running)
	}
}

func (p *Assignments) report(ticketID string, report WorkerReport) Ticket {
	ticket := p.require(ticketID)
	if !LiveTicket(*ticket) {
		refuse("ticketEnded")
	}
	asks := report.Question != nil && *report.Question != ""
	if asks && (report.State == "ready" || report.Answers != "") {
		refuse("questionWithResult")
	}
	if report.AskedBy != "" && !asks {
		refuse("askerWithoutQuestion")
	}
	person := func(id string, channel bool) bool {
		trigger := p.repo().Triggers().Get(id)
		return trigger != nil && trigger.Human && (!channel || trigger.Channel != nil) && p.reaches(ticket, *trigger, false)
	}
	if report.AskedBy != "" && !person(report.AskedBy, true) {
		refuse("notAsker")
	}
	if report.Answers != "" && !person(report.Answers, false) {
		refuse("notAnswerable")
	}
	assignment := RunningAssignment(ticket)
	if assignment == nil {
		assignment = LatestAssignment(ticket)
	}
	if assignment == nil {
		refuse("unknownAssignment")
	}
	if assignment.State == "ended" {
		refuse("assignmentEnded")
	}
	at := p.now()
	previous := ticket.Attention
	asked := previous
	if asks && (previous == nil || *report.Question != previous.Question) {
		asked = &Attention{ID: Key("attention", ticketID, *report.Question, at), Question: *report.Question, TriggerID: report.AskedBy}
	}
	var attention *Attention
	cleared := report.Question != nil && *report.Question == ""
	if !cleared && report.State != "ready" {
		attention = asked
	}
	ticket.Attention = attention
	// A replaced or cleared attention question withdraws any pending unsent reply for the previous question.
	if previous != nil && (attention == nil || previous.ID != attention.ID) {
		p.replies.withdrawAttention(previous.ID)
	}
	assignment.Report = &Report{State: report.State, Summary: report.Summary, At: at, Unreported: report.Unreported}
	ticket.UpdatedAt = at
	if report.Answers != "" && !contains(ticket.Answers, report.Answers) {
		ticket.Answers = append(ticket.Answers, report.Answers)
	}
	if report.Answers != "" {
		p.takeUp(ticketID, report.Answers)
	}
	if report.State == "blocked" || report.State == "failed" {
		kept := []string{}
		for _, id := range ticket.Undelivered {
			if !automatic(p.tickets.findInput(ticket, id)) {
				kept = append(kept, id)
			}
		}
		ticket.Undelivered = kept
	}
	p.repo().Tickets().Save(*ticket)
	// A ticket that follows Threads ends with them: a ready report ends it only once followsDone holds, with that outcome.
	ready := "completed"
	if len(ticket.Follows) > 0 {
		ready = ""
		if done := p.tickets.followsDone(ticket); done != nil {
			ready = done.Outcome
		}
	}
	outcome := ""
	switch report.State {
	case "dropped":
		outcome = "dropped"
		if done := p.tickets.followsDone(ticket); done != nil {
			outcome = done.Outcome
		}
	case "ready":
		outcome = ready
	}
	if outcome != "" {
		p.tickets.end(ticketID, "executor", outcome, at, "")
	}
	return *p.require(ticketID)
}

// running returns the ticket while it is open and assignmentID runs it.
func (p *Assignments) running(ticketID, assignmentID string) *Ticket {
	ticket := p.require(ticketID)
	if r := RunningAssignment(ticket); LiveTicket(*ticket) && r != nil && r.ID == assignmentID {
		return ticket
	}
	return nil
}
