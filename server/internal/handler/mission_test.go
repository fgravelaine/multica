package handler

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SPIKE (not upstream): the mission view.
//
// missionStages restates a rule that lives in issue_child_done.go, and the two
// must not drift. These cases are the three ways the barrier surprises a reader
// who assumes a stage is "done when its own children are done".

func stageOf(n int32) *int32 { return &n }

func missionTestRoot() *string { root := "root"; return &root }

func node(id string, stage *int32, terminal bool) MissionNode {
	return MissionNode{ID: id, ParentID: missionTestRoot(), Stage: stage, Terminal: terminal}
}

func TestMissionStages_UnstagedSetIsOneImplicitStage(t *testing.T) {
	nodes := []MissionNode{
		{ID: "root"},
		node("a", nil, true),
		node("b", nil, false),
	}
	stages, ignored := missionStages(nodes, "root")

	if len(stages) != 1 {
		t.Fatalf("stages = %d, want 1 implicit stage", len(stages))
	}
	if stages[0].Stage != nil {
		t.Errorf("implicit stage carries an ordinal %v, want nil", *stages[0].Stage)
	}
	if stages[0].Total != 2 || stages[0].Terminal != 1 {
		t.Errorf("counts = %d/%d, want 1/2", stages[0].Terminal, stages[0].Total)
	}
	if stages[0].Closed {
		t.Error("implicit stage closed with a non-terminal child")
	}
	if !stages[0].Frontier {
		t.Error("the only open stage must be the frontier")
	}
	// An unstaged set has no ignored children: every child belongs to the one
	// implicit stage.
	if len(ignored) != 0 {
		t.Errorf("ignored = %v, want none in an unstaged set", ignored)
	}
}

func TestMissionStages_BarrierIsCumulative(t *testing.T) {
	// Stage 2 is entirely done and stage 1 is not. The product's barrier is
	// cumulative, so stage 2 is NOT closed — the surprise this test pins down.
	nodes := []MissionNode{
		{ID: "root"},
		node("s1a", stageOf(1), true),
		node("s1b", stageOf(1), false),
		node("s2a", stageOf(2), true),
		node("s2b", stageOf(2), true),
	}
	stages, _ := missionStages(nodes, "root")

	if len(stages) != 2 {
		t.Fatalf("stages = %d, want 2", len(stages))
	}
	if stages[0].Closed {
		t.Error("stage 1 closed with a non-terminal child")
	}
	if !stages[0].Frontier {
		t.Error("stage 1 is the lowest unfinished stage and must be the frontier")
	}
	if stages[1].Terminal != stages[1].Total {
		t.Fatalf("stage 2 counts = %d/%d, want all terminal", stages[1].Terminal, stages[1].Total)
	}
	if stages[1].Closed {
		t.Error("stage 2 must stay open while stage 1 is open — the barrier is cumulative")
	}
	if stages[1].Frontier {
		t.Error("only the lowest open stage carries the frontier")
	}
}

func TestMissionStages_UnstagedChildInStagedSetGatesNothing(t *testing.T) {
	// The barrier skips an unstaged child in a staged set entirely. It must not
	// be counted into any stage, and the view has to name it — a child that can
	// never close a stage or wake the parent is invisible in the product today.
	nodes := []MissionNode{
		{ID: "root"},
		node("s1", stageOf(1), true),
		node("loose", nil, false),
	}
	stages, ignored := missionStages(nodes, "root")

	if len(stages) != 1 || stages[0].Total != 1 {
		t.Fatalf("stages = %+v, want one stage counting only the staged child", stages)
	}
	if !stages[0].Closed {
		t.Error("stage 1 has only terminal staged children and must be closed despite the loose child")
	}
	if len(ignored) != 1 || ignored[0] != "loose" {
		t.Errorf("ignored = %v, want [loose]", ignored)
	}
}

// missionWaitingUnit decides what the primary list contains, so its ordering of
// reasons and its refusals both matter.

type changeStub = struct {
	at time.Time
	to string
}

func TestMissionWaitingUnit_TerminalIsNeverWaiting(t *testing.T) {
	n := MissionNode{ID: "x", Status: "done", Terminal: true}
	// A terminal issue with a failed run attached is still not waiting on anyone.
	task := db.ListMissionLatestTasksRow{
		Status:      "failed",
		CompletedAt: pgtype.Timestamptz{Time: time.Now().Add(-time.Hour), Valid: true},
	}
	if _, parked := missionWaitingUnit(n, "done", MissionHand{}, task, changeStub{}, time.Now(), time.Now()); parked {
		t.Error("a terminal unit must never appear in the waiting list")
	}
}

func TestMissionWaitingUnit_HandBeatsStatus(t *testing.T) {
	// An issue can be in_review AND carry an open hand. The hand wins: it names
	// its own question and its own start time, so it is the more certain reason.
	now := time.Now()
	raised := now.Add(-2 * time.Hour)
	n := MissionNode{ID: "x", Status: "in_review"}
	hand := MissionHand{ID: "h1", Question: "which card?", CreatedAt: raised}
	change := changeStub{at: now.Add(-30 * time.Minute), to: "in_review"}

	unit, parked := missionWaitingUnit(n, "in_review", hand, db.ListMissionLatestTasksRow{}, change, now, now)
	if !parked {
		t.Fatal("expected the unit to be parked")
	}
	if unit.Reason != waitingHandRaised {
		t.Errorf("reason = %q, want %q", unit.Reason, waitingHandRaised)
	}
	if !unit.Since.Equal(raised) {
		t.Errorf("since = %v, want the hand's own timestamp %v", unit.Since, raised)
	}
	if unit.WaitedSecs < 7100 {
		t.Errorf("waited = %ds, want ~2h measured from the hand", unit.WaitedSecs)
	}
}

func TestMissionWaitingUnit_StaleStatusChangeIsFlaggedNotTrusted(t *testing.T) {
	// The latest logged change names a status the issue is no longer in, so some
	// path wrote the status without logging it. The duration falls back to
	// updated_at and must be labelled approximate rather than reported as fact.
	now := time.Now()
	n := MissionNode{ID: "x", Status: "blocked"}
	change := changeStub{at: now.Add(-10 * time.Hour), to: "todo"}
	updated := now.Add(-15 * time.Minute)

	unit, parked := missionWaitingUnit(n, "blocked", MissionHand{}, db.ListMissionLatestTasksRow{}, change, updated, now)
	if !parked {
		t.Fatal("expected the unit to be parked")
	}
	if unit.SinceExact {
		t.Error("since_exact must be false when the logged change does not match the current status")
	}
	if !unit.Since.Equal(updated) {
		t.Errorf("since = %v, want the updated_at fallback %v", unit.Since, updated)
	}
}

func TestMissionWaitingUnit_NothingToSayIsNotWaiting(t *testing.T) {
	// A backlog child with no hand, no failure and a non-parking status is
	// working as designed — it is waiting its turn, not waiting on a human.
	// Including it is what would flood the list.
	n := MissionNode{ID: "x", Status: "backlog"}
	if _, parked := missionWaitingUnit(n, "backlog", MissionHand{}, db.ListMissionLatestTasksRow{}, changeStub{}, time.Now(), time.Now()); parked {
		t.Error("a plain backlog child must not appear in the waiting list")
	}
}
