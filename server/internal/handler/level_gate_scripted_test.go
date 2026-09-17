package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// SPIKE (not upstream): the gate that asks nobody.
//
// 9 of Galactics' 13 PR-gate checks are scripts and 2 more are CI. Before this
// the gate could express only the 2 agent reviews — the minority. levels.md's
// N4 names the majority outright: "lint, tests, contract diff — ZERO TOKENS".
//
// The dangerous direction here is the OPPOSITE of the human/agent gate. There,
// a bug refuses work a team should be able to move. Here, a bug LETS WORK
// THROUGH on checks that did not pass, silently, while the gate still reads as
// configured. So the vacuous-truth and stale-head cases below matter more than
// the happy path.

func seedPRWithChecks(t *testing.T, issueID, headSHA string, checks [][3]string) string {
	t.Helper()
	prID := "11111111-1111-4111-8111-1111111111aa"
	dbfx.Exec(t, `
		INSERT INTO github_pull_request
			(id, workspace_id, installation_id, repo_owner, repo_name, pr_number,
			 title, state, html_url, pr_created_at, pr_updated_at, head_sha, snapshot_head_sha)
		VALUES ($1, $2, 1, 'veezeet', 'api', 777, 'test', 'open',
			'https://example.invalid/pr/777', now(), now(), $3, $3)
		ON CONFLICT (id) DO UPDATE SET head_sha = EXCLUDED.head_sha`,
		prID, testWorkspaceID, headSHA)
	dbfx.Exec(t, `INSERT INTO issue_pull_request (issue_id, pull_request_id, close_intent)
		VALUES ($1, $2, true) ON CONFLICT DO NOTHING`, issueID, prID)
	for i, c := range checks {
		var conclusion any
		if c[2] != "" {
			conclusion = c[2]
		}
		dbfx.Exec(t, `
			INSERT INTO github_pull_request_check_run (pr_id, head_sha, ordinal, name, status, conclusion)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (pr_id, ordinal) DO UPDATE
			SET head_sha = EXCLUDED.head_sha, name = EXCLUDED.name,
			    status = EXCLUDED.status, conclusion = EXCLUDED.conclusion`,
			prID, headSHA, i+1, c[0], c[1], conclusion)
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request_check_run WHERE pr_id = $1`, prID)
		testPool.Exec(context.Background(), `DELETE FROM issue_pull_request WHERE pull_request_id = $1`, prID)
		testPool.Exec(context.Background(), `DELETE FROM github_pull_request WHERE id = $1`, prID)
	})
	return prID
}

func declareCheckGate(t *testing.T, level string, position int, statusKey string, checks []string) {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/level-gates/"+level+"?workspace_id="+testWorkspaceID, map[string]any{
		"position": position, "status_key": statusKey,
		"ratifier_type": "check", "required_checks": checks,
	})
	req = withURLParam(req, "level", level)
	testHandler.SetLevelGate(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("declare check gate: %d: %s", w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		testPool.Exec(context.Background(),
			`DELETE FROM level_gate WHERE workspace_id = $1 AND level = $2 AND position = $3`,
			testWorkspaceID, level, position)
	})
}

func TestScriptedGate_NoChangeProposalIsRefused(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// THE VACUOUS-TRUTH CASE, and the most important test in this file.
	// "every required check succeeded" is TRUE over an empty set. A naive
	// implementation passes an issue with no PR at all, which means the gate
	// reads as configured and gates nothing.
	qa := qaStatus(t)
	declareCheckGate(t, "objective", 1, qa, []string{"composition-guard"})
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "scripted: nothing to check", "objective")
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("entering the gate: %d: %s", w.Code, w.Body.String())
	}
	w := moveTo(t, id, "in_review", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("an issue with no PR passed a scripted gate: %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "no change proposal") {
		t.Fatalf("refusal does not say why: %s", body)
	}
}

func TestScriptedGate_StaleGreenRunsDoNotCount(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// Check runs accumulate per commit. Without the head_sha join a green run
	// from three commits ago releases the gate on a head nothing has checked —
	// the exact failure a scripted gate exists to prevent, and silent.
	qa := qaStatus(t)
	declareCheckGate(t, "objective", 1, qa, []string{"composition-guard"})
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "scripted: stale head", "objective")
	seedPRWithChecks(t, id, "abc123", [][3]string{{"composition-guard", "completed", "success"}})
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("entering the gate: %d: %s", w.Code, w.Body.String())
	}
	// Green on the current head: it opens.
	if w := moveTo(t, id, "in_review", nil); w.Code != http.StatusOK {
		t.Fatalf("a green check did not open the gate: %d: %s", w.Code, w.Body.String())
	}

	// A new commit lands. The green run is now on an old head.
	dbfx.Exec(t, `UPDATE github_pull_request SET head_sha = 'def456' WHERE id = $1`,
		"11111111-1111-4111-8111-1111111111aa")
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("re-entering the gate: %d: %s", w.Code, w.Body.String())
	}
	w := moveTo(t, id, "in_review", nil)
	if w.Code != http.StatusConflict {
		t.Fatalf("a check from an old commit released the gate: %d: %s", w.Code, w.Body.String())
	}
	if body := w.Body.String(); !strings.Contains(body, "current head") {
		t.Fatalf("refusal does not say the head moved: %s", body)
	}
}

func TestScriptedGate_RunningAndFailingAreDifferentRefusals(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// They need different actions from whoever reads them — "wait" versus "fix
	// your code" — so collapsing both into "checks did not pass" would send a
	// developer looking for a bug that is not there yet.
	qa := qaStatus(t)
	declareCheckGate(t, "objective", 1, qa, []string{"composition-guard", "frontmatter-lint"})
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "scripted: running vs failing", "objective")
	seedPRWithChecks(t, id, "abc123", [][3]string{
		{"composition-guard", "completed", "success"},
		{"frontmatter-lint", "in_progress", ""},
	})
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("entering the gate: %d: %s", w.Code, w.Body.String())
	}

	w := moveTo(t, id, "in_review", nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "still running") {
		t.Fatalf("a running check should refuse as running: %d: %s", w.Code, w.Body.String())
	}

	dbfx.Exec(t, `UPDATE github_pull_request_check_run SET status='completed', conclusion='failure'
		WHERE name = 'frontmatter-lint'`)
	w = moveTo(t, id, "in_review", nil)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "failure") {
		t.Fatalf("a failed check should name its conclusion: %d: %s", w.Code, w.Body.String())
	}

	dbfx.Exec(t, `UPDATE github_pull_request_check_run SET conclusion='success'
		WHERE name = 'frontmatter-lint'`)
	if w := moveTo(t, id, "in_review", nil); w.Code != http.StatusOK {
		t.Fatalf("all green did not open the gate: %d: %s", w.Code, w.Body.String())
	}
}

func TestScriptedGate_UnreportedCheckIsRefused(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A gate naming a check that never reports must FAIL CLOSED. Names are free
	// text — a check is whatever the workflow calls its job, and it can be
	// renamed upstream without warning — so the failure mode for a typo has to
	// be a gate that never opens, which is loud, rather than one that opens
	// because nothing contradicted it.
	qa := qaStatus(t)
	declareCheckGate(t, "objective", 1, qa, []string{"a-check-nobody-runs"})
	declareGate(t, "objective", 2, "in_review", "human", "")

	id := gateIssue(t, "scripted: name nobody reports", "objective")
	seedPRWithChecks(t, id, "abc123", [][3]string{{"composition-guard", "completed", "success"}})
	if w := moveTo(t, id, qa, nil); w.Code != http.StatusOK {
		t.Fatalf("entering the gate: %d: %s", w.Code, w.Body.String())
	}
	if w := moveTo(t, id, "in_review", nil); w.Code != http.StatusConflict {
		t.Fatalf("an unreported check name opened the gate: %d: %s", w.Code, w.Body.String())
	}
}

func TestSetLevelGate_CheckRatifierNeedsNames(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A check gate naming nothing verifies nothing and opens for anyone, while
	// reading as configured — the worst failure available here. Refused at
	// declaration rather than discovered at the gate.
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/level-gates/objective?workspace_id="+testWorkspaceID, map[string]any{
		"position": 9, "status_key": "in_review", "ratifier_type": "check",
	})
	req = withURLParam(req, "level", "objective")
	testHandler.SetLevelGate(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}
