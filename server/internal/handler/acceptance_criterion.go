package handler

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SPIKE (not upstream): T4's output, as objects.
//
// A criterion and a verdict, because Galactics' verify beat declares exactly
// that — "a verdict per criterion, with its evidence" — and because AP-5's file
// already describes the work in those terms. Nothing here is a new process.

// criteriaHeading matches the section Galactics' own issue template writes:
//
//	## Acceptance Criteria
//	- [ ] Criterion 1
//	- [ ] Criterion 2
//
// Parsing it rather than asking people to retype the list is the difference
// between a feature and a chore. The checkbox is ALREADY a per-criterion object
// in embryo; the only thing missing was something that reads it.
var criteriaHeading = regexp.MustCompile(`(?im)^#{1,6}\s*acceptance\s+criteria\s*$`)

// criteriaItem matches one bullet, with or without a checkbox, and keeps the
// statement. `- [x]` counts: a criterion someone ticked by hand is still a
// criterion, and silently dropping it would make the imported list shorter than
// the one in front of them.
var criteriaItem = regexp.MustCompile(`^\s*[-*]\s*(?:\[[ xX]?\]\s*)?(.+?)\s*$`)

// parseCriteriaFromDescription pulls the statements out of an issue body.
//
// It stops at the next heading of any level: everything under `## Agent Notes`
// is not a criterion, and reading to the end of the document would turn the
// whole template into acceptance criteria.
func parseCriteriaFromDescription(description string) []string {
	loc := criteriaHeading.FindStringIndex(description)
	if loc == nil {
		return nil
	}
	rest := description[loc[1]:]

	var out []string
	for _, line := range strings.Split(rest, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			break
		}
		m := criteriaItem.FindStringSubmatch(line)
		if m == nil {
			// A prose line inside the section is not a criterion. Skipping
			// rather than stopping: a sentence of context above the bullets is
			// common and should not truncate the list.
			continue
		}
		if statement := strings.TrimSpace(m[1]); statement != "" {
			out = append(out, statement)
		}
	}
	return out
}

// AcceptanceCriterionEntry is one criterion and the last thing anyone ruled
// about it.
type AcceptanceCriterionEntry struct {
	ID        string `json:"id"`
	Ordinal   int32  `json:"ordinal"`
	Statement string `json:"statement"`
	// Ruled is false when nobody has looked. Passed means nothing unless Ruled
	// is true — the two are separate for the same reason the query coalesces:
	// "nobody looked" must never render as "it failed".
	Ruled    bool   `json:"ruled"`
	Passed   bool   `json:"passed"`
	Evidence string `json:"evidence,omitempty"`
	RuledAt  string `json:"ruled_at,omitempty"`
}

// ListIssueCriteria returns the criteria with their latest verdicts.
func (h *Handler) ListIssueCriteria(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	rows, err := h.Queries.ListLatestVerdictsForIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list acceptance criteria")
		return
	}
	entries := make([]AcceptanceCriterionEntry, 0, len(rows))
	for _, row := range rows {
		entry := AcceptanceCriterionEntry{
			ID:        uuidToString(row.CriterionID),
			Ordinal:   row.Ordinal,
			Statement: row.Statement,
			Ruled:     row.RuledAt.Valid,
		}
		if row.RuledAt.Valid {
			entry.Passed = row.Passed
			entry.Evidence = row.Evidence
			entry.RuledAt = row.RuledAt.Time.UTC().Format("2006-01-02T15:04:05Z")
		}
		entries = append(entries, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"criteria": entries})
}

// SetIssueCriteria replaces an issue's criteria.
//
// REPLACE, not append. The criteria are what the unit says it must do, and they
// live in one list; appending on every call would silently duplicate the list
// each time somebody re-imported it from the description.
//
// Replacing drops the verdicts with the criteria they were about (ON DELETE
// CASCADE), and that is correct rather than lossy: a verdict is evidence about
// a SPECIFIC statement, and a statement that has been rewritten has not been
// verified. Carrying the old verdict forward would be the one thing AP-5's file
// refuses — a pass nobody can reproduce.
func (h *Handler) SetIssueCriteria(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req struct {
		Statements []string `json:"statements"`
		// FromDescription parses the issue's own `## Acceptance Criteria`
		// section instead of taking a list.
		FromDescription bool `json:"from_description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	statements := req.Statements
	if req.FromDescription {
		statements = parseCriteriaFromDescription(issue.Description.String)
		if len(statements) == 0 {
			writeError(w, http.StatusBadRequest,
				"no `## Acceptance Criteria` section with bullets was found in this issue's description")
			return
		}
	}

	cleaned := make([]string, 0, len(statements))
	for _, s := range statements {
		if trimmed := strings.TrimSpace(s); trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}

	if err := h.Queries.DeleteAcceptanceCriteria(r.Context(), issue.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to clear acceptance criteria")
		return
	}
	for i, statement := range cleaned {
		if _, err := h.Queries.AddAcceptanceCriterion(r.Context(), db.AddAcceptanceCriterionParams{
			WorkspaceID: issue.WorkspaceID,
			IssueID:     issue.ID,
			Ordinal:     int32(i + 1),
			Statement:   statement,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to add acceptance criterion")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(cleaned)})
}

// RecordVerdict rules on one criterion.
func (h *Handler) RecordVerdict(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	var req struct {
		Ordinal  int32  `json:"ordinal"`
		Passed   *bool  `json:"passed"`
		Evidence string `json:"evidence"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Passed == nil {
		writeError(w, http.StatusBadRequest, "passed is required: a verdict is pass or fail, never absent")
		return
	}
	// "Evidence or it did not happen. Steps to reproduce, expected, observed."
	// Required on a PASS as much as a fail: a pass with no evidence is the
	// aggregate "works fine" AP-5's file refuses by name, wearing a
	// per-criterion shape.
	if strings.TrimSpace(req.Evidence) == "" {
		writeError(w, http.StatusBadRequest,
			"evidence is required: steps to reproduce, expected, observed — a verdict nobody can reproduce is an incomplete verdict")
		return
	}

	criteria, err := h.Queries.ListAcceptanceCriteria(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load acceptance criteria")
		return
	}
	var target db.AcceptanceCriterion
	for _, c := range criteria {
		if c.Ordinal == req.Ordinal {
			target = c
			break
		}
	}
	if !target.ID.Valid {
		writeError(w, http.StatusNotFound, "this issue has no criterion at that position")
		return
	}

	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	var authorID pgtype.UUID
	_ = authorID.Scan(actorID)

	if _, err := h.Queries.RecordCriterionVerdict(r.Context(), db.RecordCriterionVerdictParams{
		WorkspaceID: issue.WorkspaceID,
		CriterionID: target.ID,
		Passed:      *req.Passed,
		Evidence:    strings.TrimSpace(req.Evidence),
		AuthorType:  actorType,
		AuthorID:    authorID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to record the verdict")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ordinal": req.Ordinal, "passed": *req.Passed})
}
