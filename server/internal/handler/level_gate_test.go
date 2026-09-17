package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SPIKE (not upstream): the gate, and who ratifies it.
//
// These are the only tests in this spike that pin a REFUSAL. Everything before
// them added a field, a table or a readout; nothing changed whether a write was
// allowed. This does, on the path humans and agents both use every day — so the
// expensive failure is not "the gate did not refuse", it is "the gate refused
// something it should not have" and a team cannot move its work.
//
// Which is why more than half of what follows tests the ALLOWED direction.

func gateIssue(t *testing.T, title, level string) string {
	t.Helper()
	id := createTestIssue(t, title, "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, id) })
	dbfx.Exec(t, `UPDATE issue SET level = $1 WHERE id = $2`, level, id)
	return id
}

func declareGate(t *testing.T, level string, position int, statusKey, ratifierType, ratifierID string) {
	t.Helper()
	body := map[string]any{
		"position":      position,
		"status_key":    statusKey,
		"ratifier_type": ratifierType,
	}
	if ratifierID != "" {
		body["ratifier_id"] = ratifierID
	}
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/level-gates/"+level+"?workspace_id="+testWorkspaceID, body)
	req = withURLParam(req, "level", level)
	testHandler.SetLevelGate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("declare gate %s/%d: got %d: %s", level, position, w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM level_gate WHERE workspace_id = $1 AND level = $2 AND position = $3`,
			testWorkspaceID, level, position)
	})
}

// qaStatus adds a CUSTOM status to the workspace catalog. It matters that this
// is custom rather than a built-in: a custom status inherits only TERMINAL
// lifecycle semantics, never review — issuestatus.Effective returns the key
// itself — which is what made a unit parked at it invisible to the mission view.
func qaStatus(t *testing.T) string {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issue-statuses?workspace_id="+testWorkspaceID, map[string]any{
		"name": "Gate QA", "category": "started", "color": "#a855f7",
	})
	testHandler.CreateIssueStatus(w, req)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("create qa status: %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := json.NewDecoder(w.Body).Decode(&created); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM issue_status WHERE id = $1`, created.ID)
	})
	return created.Key
}

// moveTo drives the real UpdateIssue path — the same one the CLI and the board
// use — rather than calling checkLevelGate directly. A gate that refuses in a
// unit test and never runs in the handler would pass every test here.
func moveTo(t *testing.T, issueID, status string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID, map[string]any{"status": status})
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req = withURLParam(req, "id", issueID)
	testHandler.UpdateIssue(w, req)
	return w
}

// asAgent forges the headers resolveActor trusts for an agent. X-Actor-Source is
// server-set in production (the auth middleware re-stamps it from the token
// row), which is exactly why it can be set directly here.
func asAgent(agentID string) map[string]string {
	return map[string]string{"X-Actor-Source": "task_token", "X-Agent-ID": agentID}
}

func statusOf(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	dbfx.QueryRow(t, `SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status)
	return status
}

func TestLevelGate_NoGatesIsInert(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// The default, and the one that must never break. A workspace that has
	// never declared a gate has to behave exactly as it did before this table
	// existed — including the jump straight to done that gates exist to stop.
	id := gateIssue(t, "gate: no gates declared", "step")
	if w := moveTo(t, id, "done", nil); w.Code != http.StatusOK {
		t.Fatalf("ungated rung refused a move: %d: %s", w.Code, w.Body.String())
	}
	if got := statusOf(t, id); got != "done" {
		t.Fatalf("status is %q, want done", got)
	}
}

func TestLevelGate_ForwardSkipsAreRefused(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	qa := qaStatus(t)
	declareGate(t, "objective", 1, qa, "human", "")
	declareGate(t, "objective", 2, "in_review", "human", "")

	t.Run("straight to done", func(t *testing.T) {
		id := gateIssue(t, "gate: todo straight to done", "objective")
		w := moveTo(t, id, "done", nil)
		if w.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
		}
		// 409 and not 400: the request is well-formed and the status exists.
		// What failed is that the unit may not be there YET.
		if got := statusOf(t, id); got != "todo" {
			t.Fatalf("a refused move still wrote %q", got)
		}
	})

	t.Run("over the first gate", func(t *testing.T) {
		id := gateIssue(t, "gate: todo over gate one", "objective")
		if w := moveTo(t, id, "in_review", nil); w.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("done from the first gate", func(t *testing.T) {
		id := gateIssue(t, "gate: done from gate one", "objective")
		if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
			t.Fatalf("entering gate 1: %d: %s", w.Code, w.Body.String())
		}
		if w := moveTo(t, id, "done", nil); w.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestLevelGate_WalkingInOrderIsAllowed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	qa := qaStatus(t)
	declareGate(t, "objective", 1, qa, "human", "")
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "gate: the whole walk", "objective")
	for _, step := range []string{qa, "in_review", "done"} {
		if w := moveTo(t, id, step, nil); w.Code != http.StatusOK {
			t.Fatalf("move to %s: %d: %s", step, w.Code, w.Body.String())
		}
	}
	if got := statusOf(t, id); got != "done" {
		t.Fatalf("status is %q, want done", got)
	}
}

func TestLevelGate_BackwardsIsAlwaysOpen(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Rule 3, and the one most likely to be argued with. Galactics has AP-5
	// move a failing issue out to in_progress, which reads as "rejection is the
	// ratifier's too". Not enforced, because the cost is asymmetric: a
	// privileged rejection means a unit sits in a gate until exactly one actor
	// shows up, with no escape and no way for the author to withdraw.
	qa := qaStatus(t)
	declareGate(t, "objective", 1, qa, "human", "")
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "gate: sent back", "objective")
	for _, step := range []string{qa, "in_review"} {
		if w := moveTo(t, id, step, nil); w.Code != http.StatusOK {
			t.Fatalf("move to %s: %d: %s", step, w.Code, w.Body.String())
		}
	}
	// All the way out of the pipeline.
	if w := moveTo(t, id, "in_progress", nil); w.Code != http.StatusOK {
		t.Fatalf("sending back to in_progress was refused: %d: %s", w.Code, w.Body.String())
	}
	// And down to an earlier gate rather than out.
	for _, step := range []string{qa, "in_review"} {
		if w := moveTo(t, id, step, nil); w.Code != http.StatusOK {
			t.Fatalf("re-walking to %s: %d: %s", step, w.Code, w.Body.String())
		}
	}
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("dropping back to gate 1 was refused: %d: %s", w.Code, w.Body.String())
	}
}

func TestLevelGate_CancellingOutOfAGateIsAlwaysAllowed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Rule 4. A gate that can trap a unit forever is worse than no gate. `done`
	// is deliberately NOT exempt — done is the thing being ratified.
	qa := qaStatus(t)
	declareGate(t, "objective", 1, qa, "human", "")
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "gate: cancelled mid-walk", "objective")
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("entering gate 1: %d: %s", w.Code, w.Body.String())
	}
	if w := moveTo(t, id, "cancelled", nil); w.Code != http.StatusOK {
		t.Fatalf("cancelling out of a gate was refused: %d: %s", w.Code, w.Body.String())
	}
}

func TestLevelGate_OnlyTheRatifierReleasesAGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	var agentID, otherAgentID string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at LIMIT 1`, testWorkspaceID).Scan(&agentID)
	// Not through dbfx: a workspace with exactly one agent is a legitimate
	// fixture, and dbfx.QueryRow fails the test on no rows. The second agent is
	// an optional sub-case below, not a precondition for the rest.
	testPool.QueryRow(context.Background(),
		`SELECT id::text FROM agent WHERE workspace_id = $1 AND id <> $2 ORDER BY created_at LIMIT 1`,
		testWorkspaceID, agentID).Scan(&otherAgentID)
	if agentID == "" {
		t.Skip("workspace has no agent to ratify with")
	}

	qa := qaStatus(t)
	declareGate(t, "objective", 1, qa, "agent", agentID)
	declareGate(t, "objective", 2, "in_review", "human", "")

	t.Run("a member cannot release an agent gate", func(t *testing.T) {
		id := gateIssue(t, "gate: member at an agent gate", "objective")
		if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
			t.Fatalf("entering gate 1: %d: %s", w.Code, w.Body.String())
		}
		if w := moveTo(t, id, "in_review", nil); w.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
		}
	})

	t.Run("the named agent can", func(t *testing.T) {
		id := gateIssue(t, "gate: the named agent", "objective")
		if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
			t.Fatalf("entering gate 1: %d: %s", w.Code, w.Body.String())
		}
		if w := moveTo(t, id, "in_review", asAgent(agentID)); w.Code != http.StatusOK {
			t.Fatalf("the ratifier was refused: %d: %s", w.Code, w.Body.String())
		}
	})

	if otherAgentID != "" {
		t.Run("a different agent cannot", func(t *testing.T) {
			// The reason `agent` tests identity and `human` tests only the kind:
			// any-agent would let the agent that did the work accept its own
			// return, which is the one thing a named check exists to stop.
			id := gateIssue(t, "gate: the wrong agent", "objective")
			if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
				t.Fatalf("entering gate 1: %d: %s", w.Code, w.Body.String())
			}
			if w := moveTo(t, id, "in_review", asAgent(otherAgentID)); w.Code != http.StatusConflict {
				t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
			}
		})
	}

	t.Run("an agent cannot release a human gate", func(t *testing.T) {
		id := gateIssue(t, "gate: agent at a human gate", "objective")
		if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
			t.Fatalf("entering gate 1: %d: %s", w.Code, w.Body.String())
		}
		if w := moveTo(t, id, "in_review", asAgent(agentID)); w.Code != http.StatusOK {
			t.Fatalf("gate 1's ratifier was refused: %d: %s", w.Code, w.Body.String())
		}
		// Gate 2 is the human one, and this is the agent standing at it.
		if w := moveTo(t, id, "done", asAgent(agentID)); w.Code != http.StatusConflict {
			t.Fatalf("expected 409, got %d: %s", w.Code, w.Body.String())
		}
	})
}

func TestLevelGate_EnteringTheFirstGateNeedsNobody(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A ratifier releases a gate; it does not admit work into one. Requiring
	// the ratifier to also let the unit IN would mean an agent cannot submit
	// its own work for review, which is the normal case, not an exception.
	var agentID string
	dbfx.QueryRow(t, `SELECT id FROM agent WHERE workspace_id = $1 ORDER BY created_at LIMIT 1`, testWorkspaceID).Scan(&agentID)
	if agentID == "" {
		t.Skip("workspace has no agent")
	}
	qa := qaStatus(t)
	declareGate(t, "objective", 1, qa, "human", "")

	id := gateIssue(t, "gate: an agent submits its own work", "objective")
	if w := moveTo(t, id, qa, asAgent(agentID)); w.Code != http.StatusOK {
		t.Fatalf("an agent could not park its own work at a gate: %d: %s", w.Code, w.Body.String())
	}
}

func TestLevelGate_DeclaredLevelBeatsDepth(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// resolveIssueLevel is shared with applyLevelPolicy, and this is the rule
	// that makes an orphan expressible (§10). If the gate ever read depth in
	// preference to the declared rung, a unit's gates would come from one rung
	// while its model came from another.
	qa := qaStatus(t)
	declareGate(t, "objective", 1, qa, "human", "")

	// Depth 0 would be `campaign`, which declares no gate. The declared rung is
	// `objective`, which does.
	id := gateIssue(t, "gate: declared beats depth", "objective")
	if w := moveTo(t, id, "done", nil); w.Code != http.StatusConflict {
		t.Fatalf("the gate read depth instead of the declared rung: %d: %s", w.Code, w.Body.String())
	}

	// And the other way: clearing the declaration puts it back on campaign,
	// which is ungated.
	dbfx.Exec(t, `UPDATE issue SET level = NULL WHERE id = $1`, id)
	if w := moveTo(t, id, "done", nil); w.Code != http.StatusOK {
		t.Fatalf("an undeclared depth-0 issue was gated: %d: %s", w.Code, w.Body.String())
	}
}

func TestSetLevelGate_Validation(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	call := func(level string, body map[string]any) int {
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/level-gates/"+level+"?workspace_id="+testWorkspaceID, body)
		req = withURLParam(req, "level", level)
		testHandler.SetLevelGate(w, req)
		return w.Code
	}

	if got := call("sprint", map[string]any{"position": 1, "status_key": "in_review"}); got != http.StatusBadRequest {
		t.Fatalf("unknown rung: expected 400, got %d", got)
	}
	if got := call("objective", map[string]any{"position": 0, "status_key": "in_review"}); got != http.StatusBadRequest {
		t.Fatalf("position 0: expected 400, got %d", got)
	}
	// status_key is not an FK (migration 488 says why), so this validation is
	// what replaces it. Without it a typo makes a gate nothing can stand at,
	// which reads as "no gate" and silently stops refusing anything.
	if got := call("objective", map[string]any{"position": 1, "status_key": "in_reviewww"}); got != http.StatusBadRequest {
		t.Fatalf("unknown status: expected 400, got %d", got)
	}
	if got := call("objective", map[string]any{"position": 1, "status_key": "in_review", "ratifier_type": "lead"}); got != http.StatusBadRequest {
		t.Fatalf("lead ratifier: expected 400, got %d", got)
	}
	if got := call("objective", map[string]any{"position": 1, "status_key": "in_review", "ratifier_type": "agent"}); got != http.StatusBadRequest {
		t.Fatalf("agent with no id: expected 400, got %d", got)
	}
}

func TestLevelGate_MissionViewSeesAUnitParkedAtACustomGate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// The defect this found. A custom status does not inherit review behaviour:
	// Effective("qa") is "qa", which matches neither Blocked nor InReview, so
	// before gates existed a unit parked there fell through
	// missionWaitingUnit's default and rendered as WAITING ON NOBODY.
	// Galactics' own flow is `in review -> qa -> done`, so that is not a
	// hypothetical workspace.
	qa := qaStatus(t)
	declareGate(t, "objective", 1, qa, "human", "")

	gates := map[string]struct{}{qa: {}}
	node := MissionNode{ID: "x", Status: qa, Terminal: false}
	unit, parked := missionWaitingUnit(node, "in_progress", gates, MissionHand{},
		db.ListMissionLatestTasksRow{}, struct {
			at time.Time
			to string
		}{}, time.Now().UTC(), time.Now().UTC())
	if !parked {
		t.Fatal("a unit standing at a declared gate is not listed as waiting")
	}
	if unit.Reason != waitingReview {
		t.Fatalf("reason is %q, want %q", unit.Reason, waitingReview)
	}

	// And without the gate it is invisible again — which is the behaviour that
	// was wrong, pinned so the fix is not mistaken for something Effective does.
	if _, parked := missionWaitingUnit(node, "in_progress", nil, MissionHand{},
		db.ListMissionLatestTasksRow{}, struct {
			at time.Time
			to string
		}{}, time.Now().UTC(), time.Now().UTC()); parked {
		t.Fatal("an ungated custom status should not read as waiting on a reviewer")
	}
}
