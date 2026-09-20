package handler

// SPIKE (not upstream): a status that refuses a unit carrying no question.
//
// The status this exists for is `needs discussion`, and the defect it closes is
// that the status said nothing about WHAT had to be discussed. Five issues sat
// in it carrying zero questions between them.
//
// The flag is on the status, not in this file, so the test sets it the way a
// workspace would rather than reaching for a key the product should not know.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func statusRequiringAQuestion(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	key := fmt.Sprintf("consult_%d", testUniqueSuffix())
	var id string
	if err := testPool.QueryRow(ctx,
		`INSERT INTO issue_status (workspace_id, key, name, category, color, requires_question)
		 VALUES ($1, $2, 'Consultation', 'unstarted', '#7d8a91', true)
		 RETURNING key`,
		testWorkspaceID, key,
	).Scan(&id); err != nil {
		t.Fatalf("seed status: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM issue_status WHERE workspace_id = $1 AND key = $2`,
			testWorkspaceID, key)
	})
	return key
}

func moveIssueStatus(t *testing.T, issueID, status string) (int, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	testHandler.UpdateIssue(rec,
		withURLParam(newRequest(http.MethodPut, "/api/issues/"+issueID,
			map[string]any{"status": status}), "id", issueID))
	return rec.Code, rec.Body.String()
}

func TestStatusRequiresQuestion_RefusesAUnitWithNoHand(t *testing.T) {
	status := statusRequiringAQuestion(t)
	issueID := handFixtureIssue(t, "todo")

	code, body := moveIssueStatus(t, issueID, status)
	if code == http.StatusOK {
		t.Fatalf("move with no open hand succeeded, want a refusal: %s", body)
	}

	// Ask, then move — the order the message prescribes, because the raise
	// parks the unit and a unit moved in first would be pulled straight out.
	raise(t, issueID, "What has to be settled first?")
	if code, body := moveIssueStatus(t, issueID, status); code != http.StatusOK {
		t.Fatalf("move with an open hand = %d, want it allowed: %s", code, body)
	}
}

// Leaving is always open. A unit that turns out not to need the consultation
// must not need one to escape it — rule 3 of the rung's gates, same shape.
func TestStatusRequiresQuestion_LeavingIsAlwaysOpen(t *testing.T) {
	status := statusRequiringAQuestion(t)
	issueID := handFixtureIssue(t, "todo")

	raise(t, issueID, "Settle this?")
	if code, body := moveIssueStatus(t, issueID, status); code != http.StatusOK {
		t.Fatalf("setup move = %d: %s", code, body)
	}
	// Answer nothing; just walk out.
	if code, body := moveIssueStatus(t, issueID, "todo"); code != http.StatusOK {
		t.Fatalf("leaving = %d, want it always allowed: %s", code, body)
	}
}

// A status without the flag is untouched — every status that exists today.
func TestStatusRequiresQuestion_UnflaggedStatusIsUnaffected(t *testing.T) {
	issueID := handFixtureIssue(t, "todo")
	if code, body := moveIssueStatus(t, issueID, "in_progress"); code != http.StatusOK {
		t.Fatalf("move to an unflagged status = %d: %s", code, body)
	}
}

func testUniqueSuffix() int64 {
	var n int64
	_ = testPool.QueryRow(context.Background(),
		`SELECT (EXTRACT(EPOCH FROM clock_timestamp()) * 1000000)::bigint`).Scan(&n)
	return n
}

var _ = json.Marshal
