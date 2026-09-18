package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// SPIKE (not upstream): every list path carries `level`.
//
// THE BUG THIS CATCHES ALREADY SHIPPED, and the existing house gates did not
// see it. `TestIssueToMap_KeysMatchIssueResponse` and the CLI's
// `validIssueFields` both check the SHAPE of the payload — that the field
// exists and is spelled the same everywhere. Neither checks that a given list
// path POPULATES it.
//
// ListIssues has three query paths: one sqlc query for `open_only=true`, and
// two hand-built SELECTs assembled with fmt.Sprintf for everything else. When
// migration 484 added issue.level, the sqlc query was updated and the two
// hand-built ones were not. The field came back present and null from the
// board's own endpoint, so the board could never see a gate — a whole feature
// silently inert, with no error anywhere.
//
// A hand-built SELECT is exactly where a new column goes missing, because
// nothing regenerates it.
func TestListIssues_CarriesLevelOnEveryPath(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}

	id := createTestIssue(t, "level on every list path", "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, id) })
	dbfx.Exec(t, `UPDATE issue SET level = 'objective' WHERE id = $1`, id)

	levelOf := func(t *testing.T, query string) (string, bool) {
		t.Helper()
		w := httptest.NewRecorder()
		testHandler.ListIssues(w, newRequest("GET", "/api/issues?workspace_id="+testWorkspaceID+query, nil))
		if w.Code != http.StatusOK {
			t.Fatalf("ListIssues%s: %d: %s", query, w.Code, w.Body.String())
		}
		var body struct {
			Issues []struct {
				ID    string  `json:"id"`
				Level *string `json:"level"`
			} `json:"issues"`
		}
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, issue := range body.Issues {
			if issue.ID == id {
				if issue.Level == nil {
					return "", true
				}
				return *issue.Level, true
			}
		}
		return "", false
	}

	// The default path — the one the board actually uses, and the one that was
	// broken. It goes through a hand-built SELECT.
	if level, found := levelOf(t, ""); !found {
		t.Error("the issue is missing from the default list")
	} else if level != "objective" {
		t.Errorf("default list path returned level %q, want objective", level)
	}

	// The sqlc path. This one was correct all along, which is exactly why the
	// bug was invisible: whichever path a developer checked by hand, there was
	// a one-in-three chance of checking the right one.
	if level, found := levelOf(t, "&open_only=true"); !found {
		t.Error("the issue is missing from the open_only list")
	} else if level != "objective" {
		t.Errorf("open_only list path returned level %q, want objective", level)
	}

	// The grouped path, the second hand-built SELECT.
	if level, found := levelOf(t, "&group_by=assignee"); found && level != "objective" {
		t.Errorf("grouped list path returned level %q, want objective", level)
	}

}

// TestHandBuiltIssueSelectsNameLevel is the guard that would have caught this
// on the day it was introduced.
//
// Three of Multica's issue list paths build their SELECT with fmt.Sprintf
// instead of sqlc. Nothing regenerates them, so a column added to the issue
// table reaches the sqlc queries and silently misses these. That is exactly
// what happened to `level`: the field came back present and null from the
// board's own endpoint, the board could never match a gate, and the whole
// feature was inert with no error anywhere.
//
// Asserting on the source is crude, and it is the right shape for this bug:
// the defect is not a wrong value, it is a column nobody remembered to type.
// The runtime test above covers the two paths whose request shape is cheap to
// build; this one covers all three without fighting a request builder.
func TestHandBuiltIssueSelectsNameLevel(t *testing.T) {
	files := []string{"issue.go", "issue_table_rows.go"}
	found := 0
	for _, name := range files {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		// Every hand-built issue SELECT anchors on this prefix.
		for _, chunk := range strings.Split(string(src), "SELECT i.id, i.workspace_id")[1:] {
			found++
			// The column list ends at FROM; looking past it would match an
			// i.level that belongs to a WHERE clause instead.
			head := chunk
			if idx := strings.Index(chunk, "FROM"); idx > 0 {
				head = chunk[:idx]
			}
			if !strings.Contains(head, "i.level") {
				t.Errorf("%s: a hand-built issue SELECT does not name i.level:\n%s", name, head)
			}
		}
	}
	// Three match this anchor. The fourth hand-built SELECT — the grouped
	// variant in ListIssues — starts its column list on a continuation line and
	// does not carry the prefix, so it is covered by the runtime test above
	// rather than here. Counting is what makes a moved or renamed query fail
	// loudly instead of quietly guarding nothing.
	if found < 3 {
		// If a SELECT is renamed or moved this test would quietly guard
		// nothing. Pin the count so that silence is itself a failure.
		t.Errorf("found %d hand-built issue SELECTs, expected at least 3 — did one move?", found)
	}
}
