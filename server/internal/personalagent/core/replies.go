package core

// ReplyRequest is a message for a Channel, by Purpose: acknowledgement, answer, result, or attention.
type ReplyRequest struct {
	Purpose   string
	TicketID  string
	TriggerID string
	Question  string
	Text      string
}

// Reply is a queued message: its id, whether it went out, and whether an earlier request had queued it already.
type Reply struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Reused bool   `json:"reused"`
}

// Replies writes the Engine's messages to Channels through the Outbox port (SA replies.ts).
type Replies struct{ base }

func (p *Replies) reply(request ReplyRequest) Reply {
	if request.Purpose == "acknowledgement" {
		return p.acknowledge(request)
	}
	return p.answer(request)
}

// acknowledgeAccepted queues the acknowledgement an accept carries where the message that started the ticket was written.
func (p *Replies) acknowledgeAccepted(ticketID string, trigger Trigger, text string) {
	if trigger.Channel != nil && trigger.Human {
		p.options.Outbox.Send(OutboundMessage{ID: Key("ack", trigger.ID), Channel: *trigger.Channel, Text: text, Kind: "acknowledgement", TicketID: ticketID})
	}
}

// withdrawAttention withdraws a ticket's question for a person from the outbox, unless it went out already.
func (p *Replies) withdrawAttention(attentionID string) {
	p.options.Outbox.Withdraw(Key("attention-reply", attentionID))
}

func (p *Replies) status(id string) string {
	if s := p.options.Outbox.Status(id); s != "" {
		return s
	}
	return "pending"
}

func (p *Replies) acknowledge(request ReplyRequest) Reply {
	ticket := p.require(request.TicketID)
	if !LiveTicket(*ticket) {
		refuse("ackEnded")
	}
	trigger := p.repo().Triggers().Get(request.TriggerID)
	if trigger == nil {
		refuse("unknownTrigger")
	}
	// Context-only threads are read-only and cannot receive acknowledgements.
	if !p.reaches(ticket, *trigger, false) {
		refuse("notReached")
	}
	if trigger.Channel == nil {
		refuse("ackNoChannel")
	}
	id := Key("ack", trigger.ID)
	existing := p.options.Outbox.Status(id)
	if existing == "" && !trigger.Human {
		refuse("ackNotPerson")
	}
	if existing == "" {
		p.options.Outbox.Send(OutboundMessage{ID: id, Channel: *trigger.Channel, Text: request.Text, Kind: "acknowledgement", TicketID: ticket.ID})
	}
	p.takeUp(ticket.ID, trigger.ID)
	return Reply{ID: id, Status: p.status(id), Reused: existing != ""}
}

func (p *Replies) answer(request ReplyRequest) Reply {
	var trigger *Trigger
	if request.TriggerID != "" {
		trigger = p.repo().Triggers().Get(request.TriggerID)
		if trigger == nil {
			refuse("unknownTrigger")
		}
	}
	var ticket *Ticket
	if request.TicketID != "" {
		ticket = p.require(request.TicketID)
	} else if trigger != nil {
		for _, item := range p.repo().Tickets().List() {
			t := item
			if p.reaches(&t, *trigger, true) {
				ticket = &t
				break
			}
		}
	}
	if request.Purpose == "result" {
		outcome := ""
		reported := false
		if ticket != nil {
			outcome = TicketOutcomeOf(*ticket)
			latest := LatestAssignment(ticket)
			reported = LiveTicket(*ticket) && latest != nil && latest.Report != nil && latest.Report.State == "ready"
		}
		ready := outcome == "merged" || outcome == "completed" || outcome == "dropped" || reported
		if ticket == nil || !ready {
			refuse("notReady")
		}
	}
	var attention *Attention
	if request.Purpose == "attention" && ticket != nil {
		attention = ticket.Attention
	}
	// Attention replies must match the ticket's currently open question text.
	if request.Purpose == "attention" && (attention == nil || attention.Question != request.Question) {
		refuse("notAttention")
	}
	if request.Purpose == "answer" && (trigger == nil || !trigger.Human) {
		refuse("notQuestion")
	}
	var id string
	if attention != nil {
		id = Key("attention-reply", attention.ID)
	} else {
		subject := ""
		if trigger != nil {
			subject = trigger.ID
		} else if ticket != nil {
			subject = ticket.ID
		}
		id = Key("reply", subject, request.Text)
	}
	existing := p.options.Outbox.Status(id)
	ready := ticket == nil || trigger == nil || contains(ticket.Answers, trigger.ID)
	if request.Purpose == "answer" && !ready && existing == "" {
		refuse("noAnswer")
	}
	var channel *ChannelRef
	if request.Purpose == "answer" {
		channel = trigger.Channel
	} else {
		if attention != nil && attention.TriggerID != "" {
			if asker := p.repo().Triggers().Get(attention.TriggerID); asker != nil {
				channel = asker.Channel
			}
		}
		if channel == nil && ticket != nil {
			channel = p.sourceChannel(ticket)
		}
	}
	if channel == nil {
		refuse("noChannel")
	}
	message := OutboundMessage{ID: id, Channel: *channel, Text: request.Text, Kind: "reply"}
	if ticket != nil {
		message.TicketID = ticket.ID
	}
	p.options.Outbox.Send(message)
	// once the answer or result is out, an acknowledgement still waiting to go out says nothing more.
	if request.Purpose == "answer" || request.Purpose == "result" {
		if trigger != nil {
			p.options.Outbox.Withdraw(Key("ack", trigger.ID))
		}
		if ticket != nil {
			if src := p.source(ticket); src != nil {
				p.options.Outbox.Withdraw(Key("ack", src.ID))
			}
		}
	}
	if request.Purpose == "answer" && ticket != nil && trigger != nil && contains(ticket.Answers, trigger.ID) {
		ticket.Answers = without(ticket.Answers, trigger.ID)
		p.repo().Tickets().Save(*ticket)
	}
	return Reply{ID: id, Status: p.status(id), Reused: existing != ""}
}

// sourceChannel is the conversation a ticket started in: its origin Trigger's Channel, else its first Channel.
func (p *Replies) sourceChannel(ticket *Ticket) *ChannelRef {
	if src := p.source(ticket); src != nil && src.Channel != nil {
		return src.Channel
	}
	if len(ticket.Channels) > 0 {
		c := ticket.Channels[0]
		return &c
	}
	return nil
}
