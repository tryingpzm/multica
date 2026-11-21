package store

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
)

// Outbox is the core.Outbox port backed by pa_outbox receipts (SA src/app/outbox.ts). In auto mode a queued message is
// 'pending' and goes out; otherwise it is 'pending_confirm' until a person confirms it in the panel.
type Outbox struct {
	s    *Store
	auto func() bool
}

// NewOutbox returns the Outbox of a Store; auto reports whether the agent's accept mode sends without confirmation.
func NewOutbox(s *Store, auto func() bool) *Outbox { return &Outbox{s: s, auto: auto} }

// Send queues a message idempotently; an id queued before keeps its first message.
func (o *Outbox) Send(m core.OutboundMessage) {
	status := "pending_confirm"
	if o.auto() {
		status = "pending"
	}
	_, err := o.s.q().Exec(o.s.ctx, `INSERT INTO pa_outbox (agent_id, id, ticket_id, status, value) VALUES ($1, $2, NULLIF($3, ''), $4, $5)
		ON CONFLICT DO NOTHING`, o.s.agentID, m.ID, m.TicketID, status, encode(m))
	check(err)
}

// Receipt returns a message's stored status: ” when it was never queued, 'discarded' when it was withdrawn.
func (o *Outbox) Receipt(id string) string {
	var status string
	err := o.s.q().QueryRow(o.s.ctx, "SELECT status FROM pa_outbox WHERE agent_id = $1 AND id = $2", o.s.agentID, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	check(err)
	return status
}

// Status is the core's view: a message waiting for confirmation is pending.
func (o *Outbox) Status(id string) string {
	if s := o.Receipt(id); s != "pending_confirm" {
		return s
	}
	return "pending"
}

// Withdraw withdraws a message that has not gone out; it stays on record as discarded and is never delivered.
func (o *Outbox) Withdraw(id string) {
	_, err := o.s.q().Exec(o.s.ctx, "UPDATE pa_outbox SET status = 'discarded', updated_at = now() WHERE agent_id = $1 AND id = $2 AND status IN ('pending', 'pending_confirm')", o.s.agentID, id)
	check(err)
}

// Revise replaces the text of a reply still waiting for confirmation (edit before sending).
func (o *Outbox) Revise(id, text string) bool {
	tag, err := o.s.q().Exec(o.s.ctx, "UPDATE pa_outbox SET value = jsonb_set(value, '{text}', to_jsonb($3::text)), updated_at = now() WHERE agent_id = $1 AND id = $2 AND status = 'pending_confirm'", o.s.agentID, id, text)
	check(err)
	return tag.RowsAffected() == 1
}

// Confirm releases a message waiting for a person; false when none waits under id.
func (o *Outbox) Confirm(id string) bool {
	tag, err := o.s.q().Exec(o.s.ctx, "UPDATE pa_outbox SET status = 'pending', last_error = '', updated_at = now() WHERE agent_id = $1 AND id = $2 AND status = 'pending_confirm'", o.s.agentID, id)
	check(err)
	return tag.RowsAffected() == 1
}

// Error is why a message's last send failed, "" when none did.
func (o *Outbox) Error(id string) string {
	var msg string
	err := o.s.q().QueryRow(o.s.ctx, "SELECT last_error FROM pa_outbox WHERE agent_id = $1 AND id = $2", o.s.agentID, id).Scan(&msg)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	check(err)
	return msg
}

// Messages lists the messages in a status, oldest first.
func (o *Outbox) Messages(status string) []core.OutboundMessage {
	return many[core.OutboundMessage](o.s, "SELECT value FROM pa_outbox WHERE agent_id = $1 AND status = $2 ORDER BY seq", o.s.agentID, status)
}

// Send results of a Channel.
const (
	Sent    = "sent"
	Gone    = "gone"
	Waiting = "waiting"
)

// Deliver sends the pending messages in order through send, unless processing is paused. A result of Waiting (the
// Source is not installed or off) or an error keeps a message pending for a later pass, and holds the later messages of
// its ticket to the same conversation behind it; an error hands it back to its owner to confirm again, with the error
// , so a failing Channel is seen on the panel instead of retried silently; Gone settles it as dropped, since its conversation can never receive it.
func (o *Outbox) Deliver(paused bool, send func(core.OutboundMessage) (string, error)) error {
	if paused {
		return nil
	}
	held := map[string]bool{}
	var first error
	for _, m := range o.Messages("pending") {
		key, _ := json.Marshal([]any{m.TicketID, m.Channel.Source, m.Channel.Thread})
		if m.TicketID != "" && held[string(key)] {
			continue
		}
		result, err := send(m)
		if err != nil || result == Waiting {
			held[string(key)] = true
			if err != nil {
				if first == nil {
					first = err
				}
				if _, uerr := o.s.q().Exec(o.s.ctx, "UPDATE pa_outbox SET status = 'pending_confirm', last_error = $3, updated_at = now() WHERE agent_id = $1 AND id = $2",
					o.s.agentID, m.ID, err.Error()); uerr != nil {
					return uerr
				}
			}
			continue
		}
		status := "sent"
		if result == Gone {
			status = "dropped"
		}
		_, err = o.s.q().Exec(o.s.ctx, "UPDATE pa_outbox SET status = $3, last_error = '', updated_at = now() WHERE agent_id = $1 AND id = $2", o.s.agentID, m.ID, status)
		if err != nil {
			return err
		}
	}
	return first
}
