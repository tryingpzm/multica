package views_test

// Seam tests of the panel's views and writes against the real store: an annotation becomes its owner's
// explicit Trigger on the annotated Thread, a reply outside auto mode waits on the overview until confirmed, and a
// settings save edited from an older version is refused.

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/personalagent/core"
	"github.com/multica-ai/multica/server/internal/personalagent/host"
	"github.com/multica-ai/multica/server/internal/personalagent/sources"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/multica"
	"github.com/multica-ai/multica/server/internal/personalagent/sources/panel"
	"github.com/multica-ai/multica/server/internal/personalagent/store"
	"github.com/multica-ai/multica/server/internal/personalagent/store/storetest"
	"github.com/multica-ai/multica/server/internal/personalagent/views"
)

func uuid() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func open(t *testing.T) *host.Agent {
	t.Helper()
	pool := storetest.Open(t)
	ctx := context.Background()
	row := store.CreateAgent(store.New(ctx, pool, ""), uuid(), uuid())
	if _, err := pool.Exec(ctx, "UPDATE personal_agent SET enabled = true WHERE id = $1", row.ID); err != nil {
		t.Fatal(err)
	}
	a, err := host.New(pool, host.Ports{}, host.Options{}).Open(ctx, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func view(a *host.Agent) *views.View { return views.New(a, sources.ChannelLinks{}, time.Now()) }

func TestAnnotationIsTheOwnersExplicitTriggerOnTheAnnotatedThread(t *testing.T) {
	a := open(t)
	thread := core.ThreadRef{Source: multica.Source, ID: "issue-1"}
	id := a.Store.AddAnnotation("", core.ThreadKey(thread), a.UserID, "这个先别做，等产品确认")
	if ok, err := host.Annotate(a, panel.Annotation{ID: id, Thread: thread, Text: "这个先别做，等产品确认", At: time.Now()}); err != nil || !ok {
		t.Fatalf("annotate = %v, %v", ok, err)
	}
	page := view(a).Thread("multica:issue-1", "", 50, "")
	if page == nil {
		t.Fatal("the annotated Thread has no page")
	}
	timeline := page["timeline"].([]any)
	if len(timeline) != 1 {
		t.Fatalf("timeline = %d triggers, want 1", len(timeline))
	}
	got := timeline[0].(views.TriggerView)
	if got.ID != panel.TriggerID(id) || got.Source != "panel" || !got.Human || got.Actor.ID != a.UserID || got.Text != "这个先别做，等产品确认" {
		t.Fatalf("trigger = %+v", got)
	}
	if !got.Disposition.Queued {
		t.Fatalf("an explicit annotation waits for Eval, got %+v", got.Disposition)
	}
}

func TestReplyOutsideAutoWaitsOnTheOverviewUntilConfirmed(t *testing.T) {
	a := open(t)
	a.Outbox.Send(core.OutboundMessage{ID: "m1", Kind: "reply", Text: "已修复，请验证", Channel: core.ChannelRef{Source: multica.Source,
		Thread: "multica:issue-1", Address: map[string]string{"issueId": "issue-1"}}})
	confirm := view(a).Overview()["confirm"].([]any)
	if len(confirm) != 1 || confirm[0].(map[string]any)["text"] != "已修复，请验证" {
		t.Fatalf("confirm = %v", confirm)
	}
	if !a.Outbox.Confirm("m1") {
		t.Fatal("the waiting reply was not confirmed")
	}
	if confirm := view(a).Overview()["confirm"].([]any); len(confirm) != 0 {
		t.Fatalf("a confirmed reply still waits: %v", confirm)
	}
	if got := a.Outbox.Receipt("m1"); got != "pending" {
		t.Fatalf("receipt = %q, want pending (released to send)", got)
	}
}

func TestSettingsSaveEditedFromAnOlderVersionIsRefused(t *testing.T) {
	a := open(t)
	revision := a.Store.Revision()
	var next int
	a.Store.Atomic(func() { next = a.Store.SaveEval(revision, "auto", 120) })
	if next != revision+1 {
		t.Fatalf("revision = %d, want %d", next, revision+1)
	}
	defer func() {
		r := recover()
		if err, ok := r.(error); !ok || !errors.Is(err, store.ErrSettingsChanged) {
			t.Fatalf("stale save = %v, want ErrSettingsChanged", r)
		}
		settings, _ := a.Store.Settings()
		if settings.Eval.Mode != "auto" {
			t.Fatalf("mode = %q, the refused save changed it", settings.Eval.Mode)
		}
	}()
	a.Store.Atomic(func() { a.Store.SaveEval(revision, "off", 120) })
}

func TestSourceSaveKeepsTheAdmissionOfTheRowsThatStay(t *testing.T) {
	a := open(t)
	rev := a.Store.Revision()
	a.Store.Atomic(func() {
		rev = a.Store.SaveSource(rev, "github", true, sources.Config{"rows": []any{map[string]any{"row": "g1", "listen": "all"},
			map[string]any{"row": "g2", "listen": "mention"}}})
	})
	a.Store.Atomic(func() {
		rev = a.Store.SavePolicy(rev, nil, map[string]map[string]any{"github": {"allowed": []any{map[string]any{"id": "alice"}},
			"admission": map[string]any{"g1": map[string]any{"listedUnaddressed": "ignore"}}}})
	})
	a.Store.Atomic(func() {
		a.Store.SaveSource(rev, "github", true, sources.Config{"rows": []any{map[string]any{"row": "g1", "listen": "all"}}})
	})
	settings, err := a.Store.Settings()
	if err != nil {
		t.Fatal(err)
	}
	part := settings.Policy.Sources["github"]
	if got := part.Admission["g1"].ListedUnaddressed; got != "ignore" {
		t.Fatalf("g1 admission = %q, want ignore", got)
	}
	if _, ok := part.Admission["g2"]; ok || len(sources.Rows(settings.Sources["github"].Config)) != 1 {
		t.Fatalf("the removed row g2 is still there: %+v", settings.Sources["github"])
	}
	if len(part.Allowed) != 1 || part.Allowed[0].ID != "alice" {
		t.Fatalf("allowed = %+v", part.Allowed)
	}
}
