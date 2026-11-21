package policy_test

// Seam tests of a personal agent's admission: no list of who may originate work is needed; anyone who @s
// the owner asks explicitly, unaddressed messages are inferred candidates only in rows that receive all messages, and
// bots — @ or not — are inferred candidates at most.

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/policy"
)

func personal(t *testing.T, mode string) policy.Settings {
	return settings(t, m{"personal": true, "eval": m{"mode": mode},
		"sources": m{"feishu": feishuSource([]m{{"chatId": CHAT, "listen": "mentions"}, {"chatId": OBSERVED, "listen": "all"}}, false)}})
}

func TestPersonalAnyoneWhoAtsTheOwnerAsksExplicitly(t *testing.T) {
	gateIs(t, pol(personal(t, "observe")).Gate(feishu(from("ou_stranger")), thread(), nil),
		want{true, "", "explicit", m{"via": "admission", "cell": "unlistedAddressed", "addressed": "mention"}})
}

func TestPersonalUnaddressedIsInferredOnlyInAnAllRow(t *testing.T) {
	p := pol(personal(t, "observe"))
	gateIs(t, p.Gate(unaddressed(from("ou_stranger"), metaOf(quietIn(OBSERVED))), thread(), nil),
		want{true, "", "inferred", m{"via": "admission", "cell": "unlistedUnaddressed", "row": OBSERVED, "observation": m{"mode": "observe"}}})
	gateIs(t, p.Gate(unaddressed(from("ou_stranger"), metaOf(quietIn(CHAT))), thread(), nil), want{false, "not_a_member", "", nil})
}

func TestPersonalABotThatAtsTheOwnerIsAnInferredCandidate(t *testing.T) {
	alert := feishu(from("cli_alerts"), machine())
	gateIs(t, pol(personal(t, "observe")).Gate(alert, thread(), nil),
		want{true, "", "inferred", m{"via": "bot", "addressed": "mention", "observation": m{"mode": "observe"}}})
	if pol(personal(t, "observe")).AdmitsInferred([]string{"copy"}) {
		t.Fatal("observe accepts")
	}
	gateIs(t, pol(personal(t, "auto")).Gate(alert, thread(), nil), want{true, "", "inferred", m{"via": "bot", "observation": m{"mode": "auto"}}})
	if !pol(personal(t, "auto")).AdmitsInferred([]string{"copy"}) {
		t.Fatal("auto refuses")
	}
	gateIs(t, pol(personal(t, "auto")).Gate(unaddressed(from("cli_alerts"), machine(), metaOf(quietIn(CHAT))), thread(), nil), want{false, "not_human", "", nil})
}

func TestNotPersonalKeepsSAsListRequirement(t *testing.T) {
	s := personal(t, "observe")
	s.Personal = false
	gateIs(t, pol(s).Gate(feishu(from("ou_stranger")), thread(), nil), want{false, "not_a_member", "", nil})
	gateIs(t, pol(s).Gate(feishu(from("cli_alerts"), machine()), thread(), nil), want{false, "not_human", "", nil})
}

// MUL-13553: a 「需要我处理」 ticket only proposes a plan the owner acts on in the panel, so observe admits it inferred;
// any other rule alongside it still needs auto.
func TestObserveAdmitsAnInferredNeedsMeTicketOnly(t *testing.T) {
	s := personal(t, "observe")
	s.Policy.Rules = append(s.Policy.Rules, policy.Rule{ID: policy.NeedsMeRule, Name: "需要我处理", Enabled: true, Instruction: "x"})
	if !pol(s).AdmitsInferred([]string{policy.NeedsMeRule}) {
		t.Fatal("observe refuses a needs-me ticket")
	}
	if pol(s).AdmitsInferred([]string{policy.NeedsMeRule, "copy"}) {
		t.Fatal("observe admits another rule alongside needs-me")
	}
	off := personal(t, "off")
	off.Policy.Rules = s.Policy.Rules
	if pol(off).AdmitsInferred([]string{policy.NeedsMeRule}) {
		t.Fatal("off admits")
	}
}
