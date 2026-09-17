package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// SPIKE (not upstream): the two ends of the same finding.
//
// Multica already counted raised hands per referential. Galactics says that
// count only means something under two conditions, and neither was enforced:
//
//   - the trigger set is CLOSED (raised-hand.md, "Three triggers, and no
//     fourth"). An open set makes the raised hand the default exit, "and then
//     the counter measures how tired an agent is rather than where the
//     references are thin";
//   - an answer declares ITS LEVEL — a rule or a local choice. Without that,
//     twelve hands and twelve local choices is indistinguishable from twelve
//     hands that each thickened the reference, and the count says nothing about
//     whether anything was learned.
//
// These tests pin both. They are the reason the count is worth reading at all,
// so a regression here is silent and expensive: the number keeps rendering.

func raiseBody(trigger string) map[string]any {
	return map[string]any{
		"question":    "Do we compose, or build the component?",
		"referential": "design_system",
		"trigger":     trigger,
		"options": []map[string]any{
			{"key": "a", "label": "Compose with the existing card", "cost": "Nothing to maintain, poor rendering under 400px"},
			{"key": "b", "label": "A PlaceCard component", "cost": "One day of work, and it enters the catalogue for good"},
		},
		"recommendation": "b",
	}
}

func raiseOn(t *testing.T, issueID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/hand?workspace_id="+testWorkspaceID, body)
	req = withURLParam(req, "id", issueID)
	testHandler.RaiseHand(w, req)
	return w
}

func answerOn(t *testing.T, issueID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := newRequest("POST", "/api/issues/"+issueID+"/hand/answer?workspace_id="+testWorkspaceID, body)
	req = withURLParam(req, "id", issueID)
	testHandler.AnswerHand(w, req)
	return w
}

func handIssue(t *testing.T, title string) string {
	t.Helper()
	id := createTestIssue(t, title, "todo", "none")
	t.Cleanup(func() {
		testPool.Exec(context.Background(), `DELETE FROM referential_entry WHERE source_hand_id IN (SELECT id FROM raised_hand WHERE issue_id = $1)`, id)
		testPool.Exec(context.Background(), `DELETE FROM raised_hand WHERE issue_id = $1`, id)
		deleteTestIssue(t, id)
	})
	return id
}

func TestRaiseHand_TriggerSetIsClosed(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	// The three, and no fourth. `tired` is the exact shape of the fourth
	// trigger Galactics refuses: plausible, sympathetic, and it turns the
	// counter into a fatigue meter the first time it is accepted.
	for _, trigger := range []string{"block", "three_failures", "contradiction"} {
		t.Run("accepts "+trigger, func(t *testing.T) {
			id := handIssue(t, "closed trigger set: "+trigger)
			if w := raiseOn(t, id, raiseBody(trigger)); w.Code != http.StatusCreated && w.Code != http.StatusOK {
				t.Fatalf("trigger %q: got %d: %s", trigger, w.Code, w.Body.String())
			}
			var got string
			dbfx.QueryRow(t, `SELECT trigger FROM raised_hand WHERE issue_id = $1`, id).Scan(&got)
			if got != trigger {
				t.Fatalf("trigger recorded as %q, want %q", got, trigger)
			}
		})
	}

	for _, bad := range []string{"", "tired", "blocked", "BLOCK", "three failures"} {
		t.Run("refuses "+bad, func(t *testing.T) {
			id := handIssue(t, "refused trigger: "+bad)
			if w := raiseOn(t, id, raiseBody(bad)); w.Code != http.StatusBadRequest {
				t.Fatalf("trigger %q: expected 400, got %d: %s", bad, w.Code, w.Body.String())
			}
			// And nothing was written. A refused hand that still parks the unit
			// would be worse than accepting the trigger.
			var count int
			dbfx.QueryRow(t, `SELECT count(*) FROM raised_hand WHERE issue_id = $1`, id).Scan(&count)
			if count != 0 {
				t.Fatalf("refused hand still wrote %d row(s)", count)
			}
		})
	}
}

func TestAnswerHand_RuleThickensTheReferential(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	id := handIssue(t, "an answer that becomes a rule")
	if w := raiseOn(t, id, raiseBody("block")); w.Code >= 300 {
		t.Fatalf("raise: %d: %s", w.Code, w.Body.String())
	}

	statement := "Any surface that renders a place uses PlaceCard."
	w := answerOn(t, id, map[string]any{
		"chosen_option": "b",
		"scope":         "rule",
		"rule":          statement,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("answer: %d: %s", w.Code, w.Body.String())
	}

	var scope, key, got string
	dbfx.QueryRow(t, `SELECT answer_scope FROM raised_hand WHERE issue_id = $1`, id).Scan(&scope)
	if scope != "rule" {
		t.Fatalf("answer_scope is %q, want rule", scope)
	}
	// The entry carries the referential the HAND named, not one the answerer
	// picked. Otherwise a rule can land in a body of knowledge that never
	// failed to answer anything, and the count stops pointing at the thin one.
	dbfx.QueryRow(t, `
		SELECT e.referential_key, e.statement
		FROM referential_entry e
		JOIN raised_hand h ON h.id = e.source_hand_id
		WHERE h.issue_id = $1`, id).Scan(&key, &got)
	if key != "design_system" {
		t.Fatalf("entry landed in %q, want design_system", key)
	}
	if got != statement {
		t.Fatalf("statement is %q, want %q", got, statement)
	}
}

func TestAnswerHand_LocalChoiceWritesNothingUp(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	// The default, and it has to stay the default. Defaulting to `rule` is how
	// a reference fills with off-hand decisions and stops being trusted — at
	// which point the rules count is inflated and measures nothing again.
	id := handIssue(t, "an answer that binds only this unit")
	if w := raiseOn(t, id, raiseBody("contradiction")); w.Code >= 300 {
		t.Fatalf("raise: %d: %s", w.Code, w.Body.String())
	}
	if w := answerOn(t, id, map[string]any{"chosen_option": "a"}); w.Code != http.StatusOK {
		t.Fatalf("answer: %d: %s", w.Code, w.Body.String())
	}

	var scope string
	dbfx.QueryRow(t, `SELECT answer_scope FROM raised_hand WHERE issue_id = $1`, id).Scan(&scope)
	if scope != "local" {
		t.Fatalf("answer_scope is %q, want local", scope)
	}
	var entries int
	dbfx.QueryRow(t, `
		SELECT count(*) FROM referential_entry e
		JOIN raised_hand h ON h.id = e.source_hand_id
		WHERE h.issue_id = $1`, id).Scan(&entries)
	if entries != 0 {
		t.Fatalf("a local choice wrote %d referential entr(ies)", entries)
	}
}

func TestAnswerHand_RuleWithNoStatementIsRefused(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	// A rule with nothing written down is a local choice wearing a label. It
	// would raise the rules count without adding a line anyone can read, which
	// is the one failure mode this whole pair of columns exists to expose.
	id := handIssue(t, "a rule with nothing in it")
	if w := raiseOn(t, id, raiseBody("block")); w.Code >= 300 {
		t.Fatalf("raise: %d: %s", w.Code, w.Body.String())
	}
	if w := answerOn(t, id, map[string]any{"chosen_option": "a", "scope": "rule", "rule": "   "}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// And the hand is still open. Refusing the scope must not half-answer it.
	var status string
	dbfx.QueryRow(t, `SELECT status FROM raised_hand WHERE issue_id = $1`, id).Scan(&status)
	if status != "open" {
		t.Fatalf("hand status is %q after a refused answer, want open", status)
	}
}

func TestAnswerHand_UnknownScopeIsRefused(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	id := handIssue(t, "a scope outside the two")
	if w := raiseOn(t, id, raiseBody("block")); w.Code >= 300 {
		t.Fatalf("raise: %d: %s", w.Code, w.Body.String())
	}
	if w := answerOn(t, id, map[string]any{"chosen_option": "a", "scope": "guideline"}); w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestReferentialLoop_SeparatesRulesFromHands(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	// The readout's whole job: twelve hands and zero rules must not look like a
	// reference that is steadily improving. Two hands on the same referential,
	// one answered as a rule and one as a local choice, must come back as
	// distinct numbers rather than as a single count of two.
	ruled := handIssue(t, "loop: the one that became a rule")
	local := handIssue(t, "loop: the one that stayed local")

	before := loopCounts(t, "design_system")

	if w := raiseOn(t, ruled, raiseBody("block")); w.Code >= 300 {
		t.Fatalf("raise ruled: %d: %s", w.Code, w.Body.String())
	}
	if w := answerOn(t, ruled, map[string]any{
		"chosen_option": "b", "scope": "rule", "rule": "Places render through PlaceCard.",
	}); w.Code != http.StatusOK {
		t.Fatalf("answer ruled: %d: %s", w.Code, w.Body.String())
	}
	if w := raiseOn(t, local, raiseBody("three_failures")); w.Code >= 300 {
		t.Fatalf("raise local: %d: %s", w.Code, w.Body.String())
	}
	if w := answerOn(t, local, map[string]any{"chosen_option": "a"}); w.Code != http.StatusOK {
		t.Fatalf("answer local: %d: %s", w.Code, w.Body.String())
	}

	after := loopCounts(t, "design_system")
	if got := after.hands - before.hands; got != 2 {
		t.Fatalf("hands moved by %d, want 2", got)
	}
	if got := after.rules - before.rules; got != 1 {
		t.Fatalf("rules moved by %d, want 1", got)
	}
	if got := after.local - before.local; got != 1 {
		t.Fatalf("local choices moved by %d, want 1", got)
	}
}

type loopRow struct{ hands, rules, local int }

func loopCounts(t *testing.T, key string) loopRow {
	t.Helper()
	w := httptest.NewRecorder()
	testHandler.GetReferentialLoop(w, newRequest("GET", "/api/referentials/loop?workspace_id="+testWorkspaceID, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GetReferentialLoop: %d: %s", w.Code, w.Body.String())
	}
	var body struct {
		Referentials []struct {
			Key          string `json:"key"`
			Hands        int    `json:"hands"`
			Rules        int    `json:"rules"`
			LocalChoices int    `json:"local_choices"`
		} `json:"referentials"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode loop: %v", err)
	}
	for _, r := range body.Referentials {
		if r.Key == key {
			return loopRow{r.Hands, r.Rules, r.LocalChoices}
		}
	}
	return loopRow{}
}
