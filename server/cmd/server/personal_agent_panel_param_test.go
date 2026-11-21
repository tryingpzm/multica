package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/go-chi/chi/v5"
)

// MUL-13380: a Feishu Thread key carries base64 ids with "/" and "+"; the panel client sends it
// encodeURIComponent'd, and the route must read back the very same key.
func TestPanelParamDecodesEscapedKeys(t *testing.T) {
	const key = "feishu:CiDFMOld5R1QD2Zh+3pMuk17RAB:CiDTMZtm1v/3vsZltdAZ"
	const trigger = "feishu:CiDFMOld+x:CiDT/y"
	got := map[string]string{}
	r := chi.NewRouter()
	r.Get("/threads/{key}", func(w http.ResponseWriter, r *http.Request) { got["thread"] = panelParam(r, "key") })
	r.Post("/threads/{key}/unsubscribe", func(w http.ResponseWriter, r *http.Request) { got["unsubscribe"] = panelParam(r, "key") })
	r.Post("/threads/{key}/annotate", func(w http.ResponseWriter, r *http.Request) { got["annotate"] = panelParam(r, "key") })
	r.Post("/triggers/{id}/requeue", func(w http.ResponseWriter, r *http.Request) { got["requeue"] = panelParam(r, "id") })
	r.Get("/tickets/{id}", func(w http.ResponseWriter, r *http.Request) { got["plain"] = panelParam(r, "id") })
	for _, c := range []struct{ method, path string }{
		{"GET", "/threads/" + url.PathEscape(key)},
		{"POST", "/threads/" + url.PathEscape(key) + "/unsubscribe"},
		{"POST", "/threads/" + url.PathEscape(key) + "/annotate"},
		{"POST", "/triggers/" + url.PathEscape(trigger) + "/requeue"},
		{"GET", "/tickets/plain-id"},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(c.method, c.path, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: status %d", c.method, c.path, w.Code)
		}
	}
	// encodeURIComponent writes "%2F" and "%2B" too.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/threads/a%3Ab%2Fc%2Bd", nil))
	want := map[string]string{"thread": "a:b/c+d", "unsubscribe": key, "annotate": key, "requeue": trigger, "plain": "plain-id"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}
}
