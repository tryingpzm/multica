package core

import (
	"strings"
	"testing"
)

// MUL-13449: the Router read "request one with orchestrator_ticket_request_status" in a refused reply, a tool that does
// not exist here; every message an agent reads names the multica ticket commands instead.
func TestRefusalsAndTheStatusRequestNameTheMulticaTicketCommandsNotSAsTools(t *testing.T) {
	for code, entry := range UserErrors {
		if strings.Contains(entry[1], "orchestrator_") {
			t.Errorf("%s: %s", code, entry[1])
		}
	}
	if !strings.Contains(UserErrors["noAnswer"][1], "multica ticket request-status") {
		t.Errorf("noAnswer: %s", UserErrors["noAnswer"][1])
	}
	if strings.Contains(statusRequest, "orchestrator_") || !strings.Contains(statusRequest, "multica ticket report") {
		t.Errorf("statusRequest: %s", statusRequest)
	}
}
