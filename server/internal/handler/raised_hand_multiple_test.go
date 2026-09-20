package handler

// SPIKE (not upstream): several open hands on one unit.
//
// Migration 491 dropped the unique index that allowed one. What replaces it is
// not permissiveness — it is the promotion rule, and it is STRICTER than the
// index was. The claims under test, in order of how expensive each would be to
// get wrong:
//
//  1. answering one of several does NOT put the unit back on the board;
//  2. answering the last one does, and to the status the unit actually held;
//  3. the second hand does not overwrite where the unit came from — by the time
//     it is raised the unit is already parked, so `issue.Status` is the park;
//  4. an answer that does not name which hand is refused when several are open,
//     because picking the oldest would apply a decision to a question nobody
//     chose.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func handFixtureIssue(t *testing.T, status string) string {
	t.Helper()
	ctx := context.Background()
	var id string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO issue (workspace_id, title, status, number, creator_type, creator_id)
		 VALUES ($1, 'multi-hand probe', $2,
		         (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
		         'member', $3)
		 RETURNING id`,
		testWorkspaceID, status, testUserID,
	).Scan(&id); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM raised_hand WHERE issue_id = $1`, id)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, id)
	})
	return id
}

func raise(t *testing.T, issueID, question string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	req := withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/hands", map[string]any{
		"question":    question,
		"trigger":     "block",
		"referential": "unclassified",
		"options": []map[string]any{
			{"key": "a", "label": "A", "cost": "cost of A"},
			{"key": "b", "label": "B", "cost": "cost of B"},
		},
	}), "id", issueID)
	testHandler.RaiseHand(rec, req)
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("raise %q = %d: %s", question, rec.Code, rec.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode raise: %v", err)
	}
	id, _ := out["id"].(string)
	if id == "" {
		t.Fatalf("raise returned no id: %s", rec.Body.String())
	}
	// The counter the probe found write-only: required on the way in, and until
	// now emitted by no read path at all.
	if out["trigger"] != "block" {
		t.Errorf("trigger = %v, want it echoed back — the counter cannot be built otherwise", out["trigger"])
	}
	return id
}

func answerHand(t *testing.T, issueID, handID, option string) (int, map[string]any) {
	t.Helper()
	body := map[string]any{"chosen_option": option}
	if handID != "" {
		body["hand_id"] = handID
	}
	rec := httptest.NewRecorder()
	testHandler.AnswerHand(rec,
		withURLParam(newRequest(http.MethodPost, "/api/issues/"+issueID+"/hands/answer", body), "id", issueID))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func issueStatus(t *testing.T, issueID string) string {
	t.Helper()
	var status string
	if err := testPool.QueryRow(context.Background(),
		`SELECT status FROM issue WHERE id = $1`, issueID).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	return status
}

func TestRaisedHand_UnitResumesOnTheLastAnswerNotTheFirst(t *testing.T) {
	issueID := handFixtureIssue(t, "todo")

	first := raise(t, issueID, "First question?")
	second := raise(t, issueID, "Second question?")
	if first == second {
		t.Fatal("the two hands share an id")
	}
	if got := issueStatus(t, issueID); got != "backlog" {
		t.Fatalf("after raising, status = %q, want the unit parked", got)
	}

	// Claim 3: the second hand must not record the park as where the unit came
	// from. Both rows have to say `todo`.
	rows, err := testPool.Query(context.Background(),
		`SELECT status_before FROM raised_hand WHERE issue_id = $1`, issueID)
	if err != nil {
		t.Fatalf("read status_before: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var before string
		if err := rows.Scan(&before); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if before != "todo" {
			t.Errorf("status_before = %q, want todo — the second hand recorded the park", before)
		}
	}

	// Claim 4: no id, two open, refused.
	if code, _ := answerHand(t, issueID, "", "a"); code != http.StatusBadRequest {
		t.Errorf("answer with no hand_id and two open = %d, want 400", code)
	}

	// Claim 1: answering one leaves the unit parked.
	code, out := answerHand(t, issueID, first, "a")
	if code != http.StatusOK {
		t.Fatalf("answer first = %d", code)
	}
	if out["resumed"] != false {
		t.Errorf("resumed = %v, want false — one of two answered", out["resumed"])
	}
	if got, _ := out["still_open"].(float64); got != 1 {
		t.Errorf("still_open = %v, want 1", out["still_open"])
	}
	if got := issueStatus(t, issueID); got != "backlog" {
		t.Errorf("status after one of two = %q, want the unit still parked", got)
	}

	// Claim 2: answering the last one resumes, to the status actually held.
	code, out = answerHand(t, issueID, second, "b")
	if code != http.StatusOK {
		t.Fatalf("answer second = %d", code)
	}
	if out["resumed"] != true {
		t.Errorf("resumed = %v, want true — the last hand was answered", out["resumed"])
	}
	if got := issueStatus(t, issueID); got != "todo" {
		t.Errorf("status after the last answer = %q, want todo", got)
	}
}

// One open hand keeps working without an id — every call written before 491.
func TestRaisedHand_SingleHandNeedsNoID(t *testing.T) {
	issueID := handFixtureIssue(t, "todo")
	raise(t, issueID, "Only question?")

	code, out := answerHand(t, issueID, "", "a")
	if code != http.StatusOK {
		t.Fatalf("answer with no hand_id and one open = %d", code)
	}
	if out["resumed"] != true {
		t.Errorf("resumed = %v, want true", out["resumed"])
	}
	if got := issueStatus(t, issueID); got != "todo" {
		t.Errorf("status = %q, want todo", got)
	}
}
