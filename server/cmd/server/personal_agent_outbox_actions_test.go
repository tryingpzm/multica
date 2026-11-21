package main

import (
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

// MUL-13584 (E-11): a reply waiting for confirmation is only confirmed or withdrawn; there is no edit-then-send.
func TestE11OutboxOffersOnlyConfirmAndWithdraw(t *testing.T) {
	routes := personalAgentPanel(nil).(chi.Routes)
	got := []string{}
	_ = chi.Walk(routes, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.HasPrefix(route, "/outbox") {
			got = append(got, method+" "+route)
		}
		return nil
	})
	sort.Strings(got)
	want := "[POST /outbox/{id}/confirm POST /outbox/{id}/withdraw]"
	if s := strings.Join([]string{"[", strings.Join(got, " "), "]"}, ""); s != want {
		t.Fatalf("outbox actions %s, want %s", s, want)
	}
}
