package handler

import (
	"fmt"
	"os"
	"strings"
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
	if _, parked := missionWaitingUnit(n, "done", nil, MissionHand{}, task, changeStub{}, time.Now(), time.Now()); parked {
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

	unit, parked := missionWaitingUnit(n, "in_review", nil, hand, db.ListMissionLatestTasksRow{}, change, now, now)
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

	unit, parked := missionWaitingUnit(n, "blocked", nil, MissionHand{}, db.ListMissionLatestTasksRow{}, change, updated, now)
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
	if _, parked := missionWaitingUnit(n, "backlog", nil, MissionHand{}, db.ListMissionLatestTasksRow{}, changeStub{}, time.Now(), time.Now()); parked {
		t.Error("a plain backlog child must not appear in the waiting list")
	}
}

// missionStalled is the narrow one. The loose version of this test is "list
// every backlog child", which floods the only list worth opening, so each
// refusal below is load-bearing.

func stalledNode(id string, stage *int32, status string, terminal bool) MissionNode {
	return MissionNode{
		ID: id, Identifier: id, ParentID: missionTestRoot(),
		Stage: stage, Status: status, Terminal: terminal,
	}
}

func noTerminalAt(string) (time.Time, bool) { return time.Time{}, false }

func TestMissionStalled_PromotedStageIsReported(t *testing.T) {
	now := time.Now()
	closed := now.Add(-6 * 24 * time.Hour)
	nodes := []MissionNode{
		{ID: "root"},
		stalledNode("s1", stageOf(1), "done", true),
		stalledNode("s2a", stageOf(2), "backlog", false),
		stalledNode("s2b", stageOf(2), "backlog", false),
	}
	terminalAt := func(id string) (time.Time, bool) {
		if id == "s1" {
			return closed, true
		}
		return time.Time{}, false
	}

	out := missionStalled(nodes, map[string]struct{}{}, terminalAt,
		func(string) time.Time { return now }, now)

	if len(out) != 2 {
		t.Fatalf("stalled = %d, want both stage-2 children", len(out))
	}
	if out[0].Reason != waitingStageNotPromoted {
		t.Errorf("reason = %q, want %q", out[0].Reason, waitingStageNotPromoted)
	}
	// The clock is the barrier's, not the child's: six days ready to start.
	if !out[0].Since.Equal(closed) || !out[0].SinceExact {
		t.Errorf("since = %v exact=%v, want the barrier close %v exact", out[0].Since, out[0].SinceExact, closed)
	}
	if out[0].WaitedSecs < 5*24*3600 {
		t.Errorf("waited = %ds, want ~6 days measured from the barrier", out[0].WaitedSecs)
	}
}

func TestMissionStalled_FrontierAtStageOneHasNotStalled(t *testing.T) {
	// Nothing below stage 1 ever closed, so nothing was left un-promoted. A
	// mission that has not started is not a mission that stopped.
	now := time.Now()
	nodes := []MissionNode{
		{ID: "root"},
		stalledNode("s1a", stageOf(1), "backlog", false),
		stalledNode("s2a", stageOf(2), "backlog", false),
	}
	out := missionStalled(nodes, map[string]struct{}{}, noTerminalAt,
		func(string) time.Time { return now }, now)
	if len(out) != 0 {
		t.Errorf("stalled = %+v, want none when the frontier is the first stage", out)
	}
}

func TestMissionStalled_UnstagedSetHasNoPromotionToMiss(t *testing.T) {
	now := time.Now()
	nodes := []MissionNode{
		{ID: "root"},
		stalledNode("a", nil, "done", true),
		stalledNode("b", nil, "backlog", false),
	}
	out := missionStalled(nodes, map[string]struct{}{}, noTerminalAt,
		func(string) time.Time { return now }, now)
	if len(out) != 0 {
		t.Errorf("stalled = %+v, want none in an unstaged set", out)
	}
}

func TestMissionStalled_SkipsStartedAndAlreadyWaiting(t *testing.T) {
	now := time.Now()
	nodes := []MissionNode{
		{ID: "root"},
		stalledNode("s1", stageOf(1), "done", true),
		// Promoted and in flight — the stage was not missed.
		stalledNode("s2a", stageOf(2), "in_progress", false),
		// Backlog, but it already names a better reason in the waiting list.
		stalledNode("s2b", stageOf(2), "backlog", false),
	}
	waitingIDs := map[string]struct{}{"s2b": {}}

	out := missionStalled(nodes, waitingIDs, noTerminalAt,
		func(string) time.Time { return now }, now)
	if len(out) != 0 {
		t.Errorf("stalled = %+v, want none: one is in flight and one is already waiting", out)
	}
}

// missionReferential now reads a real catalog key. The cases that matter are
// the two ways a key can fail to resolve, because the diagnostic is a count and
// a silently dropped hand makes a referential look healthier than it is.

func TestMissionReferential_ResolvesCatalogName(t *testing.T) {
	labels := map[string]string{"design_system": "Design system"}
	key, label := missionReferential(MissionHand{Referential: "design_system"}, labels)
	if key != "design_system" || label != "Design system" {
		t.Errorf("got (%q, %q), want (design_system, Design system)", key, label)
	}
}

func TestMissionReferential_UnknownKeyResolvesToItself(t *testing.T) {
	// raised_hand.referential_key is deliberately not an FK so a hand outlives
	// a renamed or archived catalog row. Losing the hand here would undo that.
	key, label := missionReferential(MissionHand{Referential: "retired_thing"}, map[string]string{})
	if key != "retired_thing" || label != "retired_thing" {
		t.Errorf("got (%q, %q), want the key echoed back", key, label)
	}
}

func TestMissionReferential_UnrecordedIsNotUnclassified(t *testing.T) {
	// "raised before the column existed" and "the raiser could not tell" are
	// different facts. The second is a finding about the raiser; folding the
	// first into it would invent one.
	key, _ := missionReferential(MissionHand{}, map[string]string{})
	if key != missionReferentialUnrecorded {
		t.Errorf("key = %q, want %q", key, missionReferentialUnrecorded)
	}
	if key == "unclassified" {
		t.Error("an unrecorded hand must not be counted as unclassified")
	}
}

func TestMissionReferentials_CountsAndOrdersByTallestBar(t *testing.T) {
	labels := map[string]string{"design_system": "Design system", "api_contract": "API contract"}
	hands := []MissionHand{
		{ID: "1", Referential: "api_contract"},
		{ID: "2", Referential: "design_system"},
		{ID: "3", Referential: "design_system"},
		{ID: "4", Referential: "design_system"},
	}
	out := missionReferentials(hands, labels)
	if len(out) != 2 {
		t.Fatalf("groups = %d, want 2", len(out))
	}
	// The whole diagnostic is "which referential cannot answer on its own", so
	// the tallest bar leads.
	if out[0].Key != "design_system" || out[0].Count != 3 {
		t.Errorf("first group = %s/%d, want design_system/3", out[0].Key, out[0].Count)
	}
	if out[1].Key != "api_contract" || out[1].Count != 1 {
		t.Errorf("second group = %s/%d, want api_contract/1", out[1].Key, out[1].Count)
	}
}

func TestMissionReferential_StandInFlagIsOff(t *testing.T) {
	// The flag is the client's only way to know whether it is looking at a real
	// grouping or a proxy. It must track the field, not be forgotten next to it.
	if missionReferentialStandIn {
		t.Error("the grouping reads a real referential_key; the stand-in flag must be false")
	}
	if missionReferentialField != "referential_key" {
		t.Errorf("field = %q, want referential_key", missionReferentialField)
	}
}

// The recipient split decides what the primary list contains, so the two ways a
// hand can be "not on you" both need pinning.

func leadHand(id string) *MissionHand {
	return &MissionHand{ID: id, RecipientType: recipientLead, LeadName: "Obi-Wan"}
}

func humanHand(id string) *MissionHand {
	return &MissionHand{ID: id, RecipientType: recipientHuman}
}

func TestMissionRecipient_LeadHandLeavesThePrimaryList(t *testing.T) {
	// The whole point of a recipient: a hand with a lead is not an interruption
	// the human owes an answer to, so it must not be counted as one.
	units := []MissionWaitingUnit{
		{IssueID: "a", Reason: waitingHandRaised, Hand: leadHand("h1")},
		{IssueID: "b", Reason: waitingHandRaised, Hand: humanHand("h2")},
		{IssueID: "c", Reason: waitingBlocked},
	}

	withLead := make([]MissionWaitingUnit, 0)
	onHuman := make([]MissionWaitingUnit, 0)
	for _, unit := range units {
		if unit.Hand != nil && unit.Hand.RecipientType == recipientLead {
			withLead = append(withLead, unit)
			continue
		}
		onHuman = append(onHuman, unit)
	}

	if len(withLead) != 1 || withLead[0].IssueID != "a" {
		t.Errorf("withLead = %+v, want the lead-addressed hand only", withLead)
	}
	if len(onHuman) != 2 {
		t.Fatalf("onHuman = %d, want 2", len(onHuman))
	}
	// A non-hand reason has no recipient and always stays on the human: nothing
	// else in the model has a lead to delegate to.
	if onHuman[1].IssueID != "c" {
		t.Errorf("blocked unit = %q, want it to stay on the human", onHuman[1].IssueID)
	}
}

func TestMissionRecipient_ConstantsAreDistinctFromLevels(t *testing.T) {
	// recipient is WHERE a hand was sent; level is WHO answered it. They share
	// their two words and mean different things, and the answer path stamps the
	// level from the answerer rather than from the recipient precisely so a
	// human answering a lead-addressed hand is not counted as a lead closure.
	if recipientLead != levelLead || recipientHuman != levelHuman {
		t.Error("the two vocabularies are expected to share their words")
	}
}

func TestMissionStalled_ExcludesAHandThatIsWithALead(t *testing.T) {
	// Regression: the exclusion set was built from `waiting` alone, and a hand
	// addressed to a lead had already left that list in the recipient split. The
	// same issue then rendered twice — with-a-lead AND stalled. A unit in two
	// lists at once is a list nobody trusts.
	now := time.Now()
	nodes := []MissionNode{
		{ID: "root"},
		stalledNode("s1", stageOf(1), "done", true),
		stalledNode("s2a", stageOf(2), "backlog", false),
	}
	withLeadIDs := map[string]struct{}{"s2a": {}}

	out := missionStalled(nodes, withLeadIDs, noTerminalAt,
		func(string) time.Time { return now }, now)
	if len(out) != 0 {
		t.Errorf("stalled = %+v, want none: the unit is already listed with a lead", out)
	}
}

// ── The barrier, named ──────────────────────────────────────────────────────
//
// Multica records no "blocked by" link (issue_dependency is a dead table with
// no writer), so the barrier IS the blocking relation. These pin down what the
// view is allowed to claim from it.

func blockerIDs(blockers []MissionBlocker) []string {
	out := make([]string, 0, len(blockers))
	for _, b := range blockers {
		out = append(out, b.IssueID)
	}
	return out
}

func TestMissionBlockers_OnlyTheFrontierBlocks(t *testing.T) {
	// Stage 1 open, 2 and 3 behind it. A unit in stage 3 is transitively behind
	// stage 2 as well, but the thing to go and look at is the open stage.
	nodes := []MissionNode{
		{ID: "root"},
		node("s1a", stageOf(1), false),
		node("s1b", stageOf(1), true),
		node("s2a", stageOf(2), false),
		node("s3a", stageOf(3), false),
	}
	stages, _ := missionStages(nodes, "root")
	blockers := missionBlockers(nodes, stages, "root")

	for _, id := range []string{"s2a", "s3a"} {
		got := blockerIDs(blockers[id])
		if len(got) != 1 || got[0] != "s1a" {
			t.Errorf("%s blocked by %v, want [s1a] — the frontier's open unit only", id, got)
		}
	}
	// A unit IN the frontier is not behind it.
	if _, ok := blockers["s1a"]; ok {
		t.Error("a frontier unit must not be reported as blocked by its own stage")
	}
	// A terminal unit in the frontier has finished, so it blocks nobody.
	for _, b := range blockers["s2a"] {
		if b.IssueID == "s1b" {
			t.Error("a terminal unit is still named as a blocker")
		}
	}
}

func TestMissionBlockers_EverythingClosedBlocksNothing(t *testing.T) {
	nodes := []MissionNode{
		{ID: "root"},
		node("s1a", stageOf(1), true),
		node("s2a", stageOf(2), true),
	}
	stages, _ := missionStages(nodes, "root")
	if got := missionBlockers(nodes, stages, "root"); len(got) != 0 {
		t.Errorf("blockers = %v, want none when every stage is closed", got)
	}
}

func TestMissionBlockers_UnstagedChildIsBehindNothing(t *testing.T) {
	// The barrier ignores an unstaged child entirely, so it cannot be behind
	// one — the same rule TestMissionStages_UnstagedChildInStagedSetGatesNothing
	// pins from the other side.
	nodes := []MissionNode{
		{ID: "root"},
		node("s1a", stageOf(1), false),
		node("loose", nil, false),
	}
	stages, _ := missionStages(nodes, "root")
	blockers := missionBlockers(nodes, stages, "root")
	if _, ok := blockers["loose"]; ok {
		t.Error("an unstaged child is reported as blocked by the barrier that ignores it")
	}
}

func TestMissionBlockers_ImplicitStageBlocksNothing(t *testing.T) {
	// An unstaged sibling set is ONE implicit stage with a nil ordinal. Nothing
	// is below it, so nothing is behind it.
	nodes := []MissionNode{
		{ID: "root"},
		node("a", nil, false),
		node("b", nil, false),
	}
	stages, _ := missionStages(nodes, "root")
	if got := missionBlockers(nodes, stages, "root"); len(got) != 0 {
		t.Errorf("blockers = %v, want none for an implicit stage", got)
	}
}

// ── The vocabulary ──────────────────────────────────────────────────────────
//
// Campaign → Mission → Objective → Task. Only the last three are depths in this
// tree; the campaign is Multica's project, an entity that already existed.

func TestMissionLevel_StepIsTheFloor(t *testing.T) {
	// Five names against an unbounded tree. Step absorbs depth 4 and below,
	// which is not the repetition it looks like: a step's own children sit
	// inside a FOLDED step, so a reader never meets the name twice in one
	// column. Without a floor the scheme needs a noun per depth and runs out at
	// "sub-sub-task".
	//
	// The floor is the one boundary with a hard argument behind it: dispatch
	// never looks at depth or parentage, the stage barrier is computed per
	// parent at every level, and a hand can be raised anywhere.
	//
	// That argument is honest enough to cut both ways, so say so here: it also
	// means NOTHING distinguishes an objective from a task in the server. The
	// objective rung is a writing discipline people keep, not a rule this code
	// enforces — see MissionNode.Level. Only the floor is load-bearing, which
	// is why only the floor is pinned by a test.
	for depth, want := range map[int32]string{
		0: levelCampaign,
		1: levelMission,
		2: levelObjective,
		3: levelTask,
		4: levelStep,
		5: levelStep,
	} {
		if got := missionLevel(depth); got != want {
			t.Errorf("depth %d = %q, want %q", depth, got, want)
		}
	}
}

// The depth→rung CASE lives twice: here in Go, and inline in the board's SQL
// (CountIssuesByLevel / ListIssuesAtLevel), because shipping every issue in a
// workspace to Go to be labelled would be worse. This pins the two together —
// if the ladder changes and the SQL does not, this fails.
func TestMissionLevel_MatchesTheBoardSQL(t *testing.T) {
	sql, err := os.ReadFile("../../pkg/db/queries/mission.sql")
	if err != nil {
		t.Fatalf("read queries: %v", err)
	}
	for depth := int32(0); depth <= 4; depth += 1 {
		want := missionLevel(depth)
		clause := fmt.Sprintf("WHEN w.depth = %d THEN '%s'", depth, want)
		if depth == 4 {
			clause = fmt.Sprintf("ELSE '%s'", want)
		}
		if !strings.Contains(string(sql), clause) {
			t.Errorf("missionLevel(%d) = %q, but the board SQL has no %q", depth, want, clause)
		}
	}
}
