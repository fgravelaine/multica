package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SPIKE (not upstream): T4, and the four sentences it makes mechanical.
//
// Every rule tested here is already written in agents/ap5.md. None of it is a
// new process; what is new is that the product holds it instead of an agent
// remembering it. The tests are named after the sentences.

func setCriteria(t *testing.T, issueID string, statements ...string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/issues/"+issueID+"/criteria", map[string]any{"statements": statements})
	req = withURLParam(req, "id", issueID)
	testHandler.SetIssueCriteria(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("set criteria: %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM acceptance_criterion WHERE issue_id = $1`, issueID)
	})
}

func rule(t *testing.T, issueID string, ordinal int, passed bool, evidence string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/verdict", map[string]any{
		"ordinal": ordinal, "passed": passed, "evidence": evidence,
	})
	req = withURLParam(req, "id", issueID)
	testHandler.RecordVerdict(w, req)
	return w
}

func declareVerdictGate(t *testing.T, level string, position int, statusKey string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/level-gates/"+level+"?workspace_id="+testWorkspaceID, map[string]any{
		"position": position, "status_key": statusKey,
		"ratifier_type": "human", "requires_verdicts": true,
	})
	req = withURLParam(req, "level", level)
	testHandler.SetLevelGate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("declare verdict gate: %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM level_gate WHERE workspace_id = $1 AND level = $2 AND position = $3`,
			testWorkspaceID, level, position)
	})
}

// "No criteria, no verdict. If the issue has no acceptance criteria, you do not
// invent them and you do not pass it."
func TestVerify_NoCriteriaIsNotAPass(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// The same vacuous truth the scripted gate refuses: "every criterion
	// passed" is TRUE over an empty list. An issue nobody wrote criteria for
	// would sail through a gate that reads verdicts.
	qa := qaStatus(t)
	declareVerdictGate(t, "objective", 1, qa)
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "verify: no criteria", "objective")
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("entering the gate: %d: %s", w.Code, w.Body.String())
	}
	w := moveTo(t, id, "in_review", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("an issue with no criteria passed a verify gate: %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "no acceptance criteria") {
		t.Fatalf("refusal does not say why: %s", w.Body.String())
	}
}

// "Each acceptance criterion gets its own line and its own verdict. No
// aggregate 'works fine'."
func TestVerify_EveryCriterionNeedsItsOwnVerdict(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	qa := qaStatus(t)
	declareVerdictGate(t, "objective", 1, qa)
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "verify: one verdict each", "objective")
	setCriteria(t, id, "Copy conforms to the register", "List excludes churned users", "Send is scheduled")
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("entering the gate: %d: %s", w.Code, w.Body.String())
	}

	// Nobody has ruled: refused, and it says which.
	w := moveTo(t, id, "in_review", nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "no verdict yet") {
		t.Fatalf("unruled criteria should refuse as unruled: %d: %s", w.Code, w.Body.String())
	}

	// Two of three: still refused. This is the case an aggregate verdict hides.
	rule(t, id, 1, true, "register §4 matches")
	rule(t, id, 3, true, "scheduled_at set")
	w = moveTo(t, id, "in_review", nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "criterion 2") {
		t.Fatalf("a partially verified issue passed, or named the wrong criterion: %d: %s", w.Code, w.Body.String())
	}

	rule(t, id, 2, true, "export returned 0 churned users")
	if w := moveTo(t, id, "in_review", nil); w.Code != http.StatusOK {
		t.Fatalf("all criteria passing did not open the gate: %d: %s", w.Code, w.Body.String())
	}
}

// A failed criterion and an unruled one are DIFFERENT refusals.
func TestVerify_FailedAndUnruledAreDistinct(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// "nobody looked" and "looked and it failed" need different actions. The
	// query coalesces `passed`, so reading it without checking `ruled_at` would
	// report every unruled criterion as a failure — the exact confusion this
	// pins.
	qa := qaStatus(t)
	declareVerdictGate(t, "objective", 1, qa)
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "verify: failed vs unruled", "objective")
	setCriteria(t, id, "first", "second")
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("entering the gate: %d: %s", w.Code, w.Body.String())
	}

	rule(t, id, 1, false, "observed the opposite")
	w := moveTo(t, id, "in_review", nil)
	body := w.Body.String()
	if !strings.Contains(body, "criterion 1 failed") {
		t.Fatalf("a real failure should be reported as failed, not as unruled: %s", body)
	}
	if strings.Contains(body, "criterion 2 failed") {
		t.Fatalf("an unruled criterion was reported as a failure: %s", body)
	}
}

// "Evidence or it did not happen."
func TestVerify_EvidenceIsRequiredOnAPassToo(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A pass with no evidence is the aggregate "works fine" AP-5's file refuses
	// by name, wearing a per-criterion costume. Requiring it only on failures
	// would leave every green verdict unreproducible.
	id := gateIssue(t, "verify: evidence on a pass", "objective")
	setCriteria(t, id, "only")

	if w := rule(t, id, 1, true, "   "); w.Code != http.StatusBadRequest {
		t.Fatalf("a pass with blank evidence was accepted: %d: %s", w.Code, w.Body.String())
	}
	if w := rule(t, id, 1, false, ""); w.Code != http.StatusBadRequest {
		t.Fatalf("a fail with no evidence was accepted: %d: %s", w.Code, w.Body.String())
	}
	if w := rule(t, id, 1, true, "ran it, observed the expected output"); w.Code != http.StatusOK {
		t.Fatalf("a verdict with evidence was refused: %d: %s", w.Code, w.Body.String())
	}
}

// Verdicts accumulate; a re-check is a new row.
func TestVerify_VerdictsAccumulate(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Overwriting would make "criterion 2 failed twice before passing"
	// unaskable, and that sequence is the input to raised-hand.md's staffing
	// question — do the past answers predict the next ones.
	id := gateIssue(t, "verify: history", "objective")
	setCriteria(t, id, "only")

	rule(t, id, 1, false, "first look: wrong")
	rule(t, id, 1, false, "second look: still wrong")
	rule(t, id, 1, true, "after the fix: right")

	var count int
	dbfx.QueryRow(t, `
		SELECT count(*) FROM criterion_verdict v
		JOIN acceptance_criterion c ON c.id = v.criterion_id
		WHERE c.issue_id = $1`, id).Scan(&count)
	if count != 3 {
		t.Fatalf("kept %d verdicts, want 3 — a re-check must not overwrite", count)
	}

	// And the LATEST one is what the gate reads.
	w := httptest.NewRecorder()
	req := newRequest("GET", "/api/issues/"+id+"/criteria", nil)
	req = withURLParam(req, "id", id)
	testHandler.ListIssueCriteria(w, req)
	if !strings.Contains(w.Body.String(), "after the fix") {
		t.Fatalf("the latest verdict is not the one reported: %s", w.Body.String())
	}
}

// Rewriting the criteria drops their verdicts.
func TestVerify_RestatingCriteriaClearsTheirVerdicts(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A verdict is evidence about one exact statement. Carrying it onto a
	// rewritten statement would produce a pass nobody can reproduce, which is
	// the one thing AP-5's file calls an incomplete verdict.
	id := gateIssue(t, "verify: restated criteria", "objective")
	setCriteria(t, id, "the old wording")
	rule(t, id, 1, true, "verified the old wording")

	setCriteria(t, id, "the new wording")
	var count int
	dbfx.QueryRow(t, `
		SELECT count(*) FROM criterion_verdict v
		JOIN acceptance_criterion c ON c.id = v.criterion_id
		WHERE c.issue_id = $1`, id).Scan(&count)
	if count != 0 {
		t.Fatalf("%d verdicts survived a rewrite of the criteria they were about", count)
	}
}

func TestVerify_ParsesGalacticsIssueTemplate(t *testing.T) {
	// The template is the real one from docs/galactic-story/platform/reference.md.
	// The parser has to stop at the next heading: everything under `## Agent
	// Notes` is not a criterion, and reading to the end would turn the whole
	// template into acceptance criteria.
	description := `## Context
Why this work exists

## What
Send the incoming feature email.

## Acceptance Criteria
- [ ] Copy conforms to the brand register
- [x] The recipient list excludes churned users
- The send is scheduled, not immediate

## Agent Notes
- Discussed with: Padme, Cassian
- Architecture review: N/A`

	got := parseCriteriaFromDescription(description)
	want := []string{
		"Copy conforms to the brand register",
		"The recipient list excludes churned users",
		"The send is scheduled, not immediate",
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d criteria, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("criterion %d = %q, want %q", i+1, got[i], want[i])
		}
	}

	// A ticked box still counts. Dropping it would make the imported list
	// shorter than the one the person is looking at.
	if len(parseCriteriaFromDescription("## Acceptance Criteria\n- [x] done already")) != 1 {
		t.Error("a ticked checkbox was dropped")
	}
	// No section, nothing parsed — not an error, just no criteria.
	if got := parseCriteriaFromDescription("## What\nsomething"); got != nil {
		t.Errorf("parsed %v from a description with no criteria section", got)
	}
}
