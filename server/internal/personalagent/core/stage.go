package core

// TicketStage is the high-level execution stage of a ticket (SA stage.ts).
type TicketStage struct {
	Stage  string `json:"stage"`
	Action string `json:"action,omitempty"`
}

// StageOf classifies a ticket's stage; the first matching row of SA docs/003 wins.
func StageOf(ticket *Ticket) TicketStage {
	if ticket.Ended != nil {
		return TicketStage{Stage: "ended"}
	}
	if ticket.Delivery != nil {
		return TicketStage{Stage: "person", Action: "delivery"}
	}
	if ticket.Attention != nil {
		return TicketStage{Stage: "person", Action: "answer"}
	}
	assignment := LatestAssignment(ticket)
	report := ""
	if assignment != nil && assignment.Report != nil {
		report = assignment.Report.State
	}
	if report == "ready" {
		return TicketStage{Stage: "person", Action: "review"}
	}
	if (report == "blocked" || report == "failed") && WaitsForPerson(ticket) {
		return TicketStage{Stage: "person", Action: report}
	}
	if assignment == nil || assignment.State == "pending" {
		return TicketStage{Stage: "working", Action: "queued"}
	}
	if report == "waiting" || assignment.State == "ended" {
		return TicketStage{Stage: "idle"}
	}
	return TicketStage{Stage: "working"}
}

// CanRetry reports whether an open ticket is stalled on a blocked/failed report or a failed input delivery.
func CanRetry(ticket *Ticket) bool {
	return ticket.Ended == nil && (WaitsForPerson(ticket) || ticket.Delivery != nil)
}
