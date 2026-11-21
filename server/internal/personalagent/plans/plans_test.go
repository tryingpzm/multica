package plans_test

// Seam tests of MUL-13553 「需要我处理」 plans: the owner's executor proposes one plan per issue and status; it goes
// stale when the issue moves on; the owner executes it action by action as themselves, or rejects it with a reason;
// under auto the safe actions run on their own and merging, releasing and answering a clarify always wait for the
// owner. The actions' target (Multica, channels) is the system boundary and is faked.

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/multica-ai/multica/server/internal/personalagent/plans"
	"github.com/multica-ai/multica/server/internal/personalagent/store/storetest"
)

type actor struct {
	done []string
	fail string
}

func (a *actor) Do(_ context.Context, p plans.Plan, act plans.Action) (string, error) {
	ownerID := p.OwnerID
	if act.Type == a.fail {
		return "", errors.New("boom")
	}
	a.done = append(a.done, ownerID+":"+act.Type+":"+act.Target+":"+act.Body)
	return "ok", nil
}

func setup(t *testing.T) (*plans.Plans, *actor) {
	pool := storetest.Open(t)
	a := &actor{}
	return plans.New(pool, a), a
}

var mergeAndComment = plans.Proposal{AgentID: "pa1", OwnerID: "u1", IssueID: "i1", IssueStatus: "in_review", Conclusion: "可以合并",
	Reason: "review 通过、CI 绿", Actions: []plans.Action{{Type: "comment", Target: "i1", Body: "LGTM，合并"}, {Type: "merge_mr", Target: "proj!12"}}}

func TestAnIssueStatusGetsOnePlan(t *testing.T) {
	p, _ := setup(t)
	ctx := context.Background()
	first, created, err := p.Propose(ctx, mergeAndComment, "observe")
	if err != nil || !created {
		t.Fatalf("first %v %v", created, err)
	}
	again, created, err := p.Propose(ctx, mergeAndComment, "observe")
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("second %v %v %v", again.ID, created, err)
	}
	if got := p.Pending(ctx, "pa1"); len(got) != 1 {
		t.Fatalf("pending %d", len(got))
	}
}

func TestAPlanGoesStaleWhenTheIssueMovesOn(t *testing.T) {
	p, _ := setup(t)
	ctx := context.Background()
	plan, _, _ := p.Propose(ctx, mergeAndComment, "observe")
	p.IssueChanged(ctx, "done", "uuid-1", "i1")
	if got := p.Get(ctx, plan.ID); got.State != "stale" {
		t.Fatalf("state %s", got.State)
	}
	if len(p.Pending(ctx, "pa1")) != 0 {
		t.Fatal("stale plan still pending")
	}
	// The same status again keeps it.
	other := mergeAndComment
	other.IssueID = "i2"
	kept, _, _ := p.Propose(ctx, other, "observe")
	p.IssueChanged(ctx, "in_review", "i2")
	if p.Get(ctx, kept.ID).State != "pending" {
		t.Fatal("unchanged status made the plan stale")
	}
}

func TestUnderAutoOnlySafeActionsRunAndMergingWaits(t *testing.T) {
	p, a := setup(t)
	plan, _, _ := p.Propose(context.Background(), mergeAndComment, "auto")
	if !reflect.DeepEqual(a.done, []string{"u1:comment:i1:LGTM，合并" + plans.OnBehalf}) {
		t.Fatalf("auto ran %v", a.done)
	}
	if got := p.Get(context.Background(), plan.ID); got.State != "pending" {
		t.Fatalf("state %s, the merge still waits for the owner", got.State)
	}
}

func TestUnderObserveNothingRunsOnItsOwn(t *testing.T) {
	p, a := setup(t)
	p.Propose(context.Background(), mergeAndComment, "observe")
	if len(a.done) != 0 {
		t.Fatalf("observe ran %v", a.done)
	}
}

func TestExecutingRunsEachActionAsTheOwnerAndMergingNeedsConfirmation(t *testing.T) {
	p, a := setup(t)
	ctx := context.Background()
	plan, _, _ := p.Propose(ctx, mergeAndComment, "observe")
	if _, err := p.Execute(ctx, plan.ID, nil, false); !errors.Is(err, plans.ErrConfirm) {
		t.Fatalf("unconfirmed merge: %v", err)
	}
	if len(a.done) != 0 {
		t.Fatalf("ran before confirmation %v", a.done)
	}
	got, err := p.Execute(ctx, plan.ID, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"u1:comment:i1:LGTM，合并" + plans.OnBehalf, "u1:merge_mr:proj!12:"}
	if !reflect.DeepEqual(a.done, want) || got.State != "executed" {
		t.Fatalf("ran %v state %s", a.done, got.State)
	}
}

func TestExecutingAModifiedPlanRunsTheOwnersActions(t *testing.T) {
	p, a := setup(t)
	ctx := context.Background()
	plan, _, _ := p.Propose(ctx, mergeAndComment, "observe")
	if _, err := p.Execute(ctx, plan.ID, []plans.Action{{Type: "comment", Target: "i1", Body: "先别合"}}, false); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.done, []string{"u1:comment:i1:先别合" + plans.OnBehalf}) {
		t.Fatalf("ran %v", a.done)
	}
}

func TestRejectingRecordsTheReason(t *testing.T) {
	p, a := setup(t)
	ctx := context.Background()
	plan, _, _ := p.Propose(ctx, mergeAndComment, "observe")
	if err := p.Reject(ctx, plan.ID, "MR 还有冲突"); err != nil {
		t.Fatal(err)
	}
	got := p.Get(ctx, plan.ID)
	if got.State != "rejected" || got.RejectReason != "MR 还有冲突" || len(a.done) != 0 {
		t.Fatalf("%s %q %v", got.State, got.RejectReason, a.done)
	}
	if _, err := p.Execute(ctx, plan.ID, nil, true); err == nil {
		t.Fatal("a rejected plan executed")
	}
}
