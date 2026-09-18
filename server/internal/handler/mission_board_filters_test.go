package handler

// SPIKE (not upstream): the board's status and project filters.
//
// The claim under test is not "the filter narrows the rows" — that one is hard
// to get wrong. It is that the COUNTS ABOVE THE BOARD DO NOT MOVE. A filtered
// board that also filtered its own totals could never tell you how much it was
// hiding, and the tabs would read as if the work had disappeared rather than
// been excluded.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// boardFixture puts four units on the `task` rung: two projects, two statuses.
// Every one is parentless, so the derived rung is `campaign` and the DECLARED
// rung is what puts them on `task` — which also makes all four orphans, and the
// orphan count is one of the numbers that must not move.
func boardFixture(t *testing.T) (projectA, projectB string) {
	t.Helper()
	ctx := context.Background()

	for _, p := range []*string{&projectA, &projectB} {
		if err := testPool.QueryRow(ctx,
			`INSERT INTO project (workspace_id, title) VALUES ($1, $2) RETURNING id`,
			testWorkspaceID, fmt.Sprintf("board-filter-%d", len(*p)+len(projectA)),
		).Scan(p); err != nil {
			t.Fatalf("seed project: %v", err)
		}
	}

	seed := []struct {
		title   string
		status  string
		project string
	}{
		{"filter A todo", "todo", projectA},
		{"filter A doing", "in_progress", projectA},
		{"filter B todo", "todo", projectB},
		{"filter B doing", "in_progress", projectB},
	}
	for _, s := range seed {
		var id string
		if err := testPool.QueryRow(ctx,
			`INSERT INTO issue (workspace_id, title, status, project_id, level, number,
			                    creator_type, creator_id)
			 VALUES ($1, $2, $3, $4, 'task',
			         (SELECT COALESCE(MAX(number), 0) + 1 FROM issue WHERE workspace_id = $1),
			         'member', $5)
			 RETURNING id`,
			testWorkspaceID, s.title, s.status, s.project, testUserID,
		).Scan(&id); err != nil {
			t.Fatalf("seed issue %q: %v", s.title, err)
		}
		t.Cleanup(func() {
			_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE id = $1`, id)
		})
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM project WHERE id = ANY($1)`,
			[]string{projectA, projectB})
	})
	return projectA, projectB
}

func board(t *testing.T, query string) MissionBoardResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	testHandler.ListMissions(rec, newRequest(http.MethodGet, "/api/missions"+query, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/missions%s = %d: %s", query, rec.Code, rec.Body.String())
	}
	var out MissionBoardResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode board: %v", err)
	}
	return out
}

// countTitled counts only the rows this fixture created, so other tests'
// leftovers in the shared database cannot make an assertion pass or fail.
func countTitled(rows []MissionBoardRow, prefix string) int {
	n := 0
	for _, r := range rows {
		if len(r.Title) >= len(prefix) && r.Title[:len(prefix)] == prefix {
			n++
		}
	}
	return n
}

func TestMissionBoardFilters_NarrowRowsNotCounts(t *testing.T) {
	projectA, projectB := boardFixture(t)

	unfiltered := board(t, "?level=task")
	if got := countTitled(unfiltered.Rows, "filter "); got != 4 {
		t.Fatalf("unfiltered rows = %d, want the 4 seeded", got)
	}

	// The counts the tabs render, captured before any filter is applied.
	var wantTotal, wantOrphans int
	for _, l := range unfiltered.Levels {
		if l.Level == "task" {
			wantTotal, wantOrphans = l.Total, l.Orphans
		}
	}
	if wantTotal == 0 {
		t.Fatal("task rung reported a total of 0 with four seeded units")
	}

	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"one status", "?level=task&status=todo", 2},
		{"one project", "?level=task&project=" + projectA, 2},
		{"both", "?level=task&status=todo&project=" + projectA, 1},
		{"both, other project", "?level=task&status=in_progress&project=" + projectB, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := board(t, tc.query)
			if n := countTitled(got.Rows, "filter "); n != tc.want {
				t.Errorf("rows = %d, want %d", n, tc.want)
			}
			for _, l := range got.Levels {
				if l.Level != "task" {
					continue
				}
				if l.Total != wantTotal {
					t.Errorf("filtered total = %d, want the unfiltered %d — the counts are the whole workspace",
						l.Total, wantTotal)
				}
				if l.Orphans != wantOrphans {
					t.Errorf("filtered orphans = %d, want the unfiltered %d", l.Orphans, wantOrphans)
				}
			}
		})
	}
}

// A status that is not in this workspace's catalog is a 400, not an empty
// board. An empty board reads as "nothing on this rung", which is the more
// expensive thing to be wrong about.
func TestMissionBoardFilters_UnknownStatusIsRefused(t *testing.T) {
	boardFixture(t)

	rec := httptest.NewRecorder()
	testHandler.ListMissions(rec, newRequest(http.MethodGet,
		"/api/missions?level=task&status=not_a_status", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown status = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}
