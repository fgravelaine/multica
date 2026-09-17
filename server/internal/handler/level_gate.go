package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SPIKE (not upstream): what has the right to say no at a rung, and who accepts
// the return. The two of Galactics' five level parameters that had nowhere to
// live in Multica.
//
// THE FIRST REFUSAL IN THIS SPIKE. Everything before it added a field, a table
// or a readout; nothing changed whether a write was allowed. This does, on a
// path humans and agents both use every day, so the rules below are narrow on
// purpose and each one says what it would cost to get wrong.

const (
	ratifierHuman = "human"
	ratifierAgent = "agent"
	// ratifierCheck is the majority case in Galactics' own PR gate — 9 of its
	// 13 checks are scripts and 2 more are CI. Nobody is asked; a script already
	// answered, and this reads the answer.
	ratifierCheck = "check"

	// actorMember is what resolveActor calls a person. Named here so the
	// mismatch with ratifierHuman is visible at the point it matters.
	actorMember = "member"
)

// gateWalk is the ordered pipeline a rung declares, plus where a given status
// sits in it. Position is 1-based; 0 means "not a gate".
type gateWalk struct {
	gates []db.LevelGate
}

func (g gateWalk) positionOf(statusKey string) int {
	for _, gate := range g.gates {
		if gate.StatusKey == statusKey {
			return int(gate.Position)
		}
	}
	return 0
}

func (g gateWalk) at(position int) (db.LevelGate, bool) {
	for _, gate := range g.gates {
		if int(gate.Position) == position {
			return gate, true
		}
	}
	return db.LevelGate{}, false
}

// last is the highest declared position, which is the only one `done` may be
// reached from.
func (g gateWalk) last() int {
	high := 0
	for _, gate := range g.gates {
		if int(gate.Position) > high {
			high = int(gate.Position)
		}
	}
	return high
}

// checkLevelGate decides whether this actor may move this issue to this status.
// It returns an empty string to allow, or the refusal to render as a 409.
//
// The four rules, and what each one is worth:
//
//  1. NO SKIPPING FORWARD. Gate k is reachable only from gate k-1, and a `done`
//     status only from the last gate. Without this the gates are decoration —
//     anything could move straight to done and the rung would still read as
//     configured.
//
//  2. ONLY THE RATIFIER RELEASES A GATE FORWARD. This is "who accepts the
//     return", and it is the half that makes rule 1 mean something: an ordered
//     path anyone may walk is a longer path, not a gate.
//
//  3. BACKWARDS IS ALWAYS OPEN, and this is the rule most likely to be argued
//     with. Galactics has AP-5 move a failing issue out to in_progress, which
//     reads as "rejection is the ratifier's too". It is not enforced here,
//     because the cost is asymmetric: a privileged rejection means a unit sits
//     in a gate until exactly one actor appears, with no escape and no way for
//     the author to withdraw. Sending work back is not an acceptance — it
//     costs a redo, and a redo is cheap. Being unable to move at all is not.
//
//  4. A CLOSED STATUS IS ALWAYS REACHABLE. Cancelling must never be gated, for
//     the same reason: a gate that can trap a unit forever is worse than no
//     gate. `done` is NOT in this exemption — done is the thing being ratified.
//
// A workspace with no gates on the rung gets an empty string on every call, so
// this is inert until somebody declares one.
func (h *Handler) checkLevelGate(ctx context.Context, issue db.Issue, targetStatus, actorType, actorID string) string {
	if targetStatus == "" || targetStatus == issue.Status {
		return ""
	}

	level, ok := h.resolveIssueLevel(ctx, issue)
	if !ok {
		// The rung is unknown, so which gates apply is unknown. Allowing is the
		// only safe direction: refusing would block a write because a COUNT
		// failed, and a lookup failure must not look like a rejected review.
		return ""
	}

	gates, err := h.Queries.ListLevelGates(ctx, db.ListLevelGatesParams{
		WorkspaceID: issue.WorkspaceID,
		Level:       level,
	})
	if err != nil || len(gates) == 0 {
		return ""
	}
	walk := gateWalk{gates: gates}

	current := walk.positionOf(issue.Status)
	target := walk.positionOf(targetStatus)

	// One catalog read, reused by rules 4 and 3. A custom status is only
	// classifiable through the workspace's own catalog, so this cannot be
	// answered from the key alone.
	targetCategory := issuestatus.Category(ctx, h.Queries, issue.WorkspaceID, targetStatus)

	// Rule 4, before anything else: a unit can always be cancelled out of a
	// gate. `closed` is the category Multica gives cancelled; `done` is its own
	// category and is deliberately not exempt here.
	if targetCategory == issuestatus.CategoryClosed {
		return ""
	}

	// Rule 3: anything that is not forward progress through the pipeline.
	// Leaving the pipeline entirely (back to in_progress) or dropping to an
	// earlier gate both land here.
	if target == 0 && targetCategory != issuestatus.CategoryDone {
		return ""
	}
	if target > 0 && target <= current {
		return ""
	}

	// From here the move is forward. Rule 1 first, because "you skipped a gate"
	// is a more useful message than "you are not the ratifier of a gate you
	// were never at".
	if target > 0 && target != current+1 {
		if expected, ok := walk.at(current + 1); ok {
			return fmt.Sprintf(
				"this rung (%s) gates %s before %s: move it to %q first",
				level, expected.StatusKey, targetStatus, expected.StatusKey)
		}
		return fmt.Sprintf("this rung (%s) declares gates that must be walked in order", level)
	}
	if target == 0 && current != walk.last() {
		next := current + 1
		expected, ok := walk.at(next)
		if !ok {
			return fmt.Sprintf("this rung (%s) declares gates that must be walked in order", level)
		}
		return fmt.Sprintf(
			"this rung (%s) has not passed %s: %s cannot be reached until it does",
			level, expected.StatusKey, targetStatus)
	}

	// Rule 2. Only reached when the unit is standing at a gate and moving
	// forward off it, which is exactly the moment of ratification.
	if current == 0 {
		return ""
	}
	gate, ok := walk.at(current)
	if !ok {
		return ""
	}
	// Verdicts first, when the gate wants them. "Is the evidence there" is a
	// more useful refusal than "you are not the ratifier" — and a ratifier who
	// IS allowed to release the gate still must not release it over criteria
	// nobody ruled on. That is the whole point of asking both.
	if gate.RequiresVerdicts {
		if refusal := h.verdictRefusal(ctx, issue, gate); refusal != "" {
			return refusal
		}
	}
	if gate.RatifierType == ratifierCheck {
		return h.scriptedRefusal(ctx, issue, gate)
	}
	return ratifierRefusal(gate, actorType, actorID)
}

// verdictRefusal is T4 read back: a verdict per criterion, with its evidence.
//
// It enforces three sentences that are already in agents/ap5.md and were, until
// now, things an agent had to remember:
//
//	"Each acceptance criterion gets its own line and its own verdict."
//	"No criteria, no verdict ... you do not invent them and you do not pass it."
//	"Evidence or it did not happen."  (the NOT NULL on criterion_verdict.evidence)
//
// The no-criteria case is NOT a pass. An issue with no acceptance criteria is
// AP-5's own first finding, and "every criterion passed" is true over an empty
// set — the same vacuous truth the scripted gate refuses.
func (h *Handler) verdictRefusal(ctx context.Context, issue db.Issue, gate db.LevelGate) string {
	rows, err := h.Queries.ListLatestVerdictsForIssue(ctx, issue.ID)
	if err != nil {
		// Fail closed, like the scripted gate and unlike the rest of this file.
		// A gate that reads evidence is worth having only if an unreadable
		// table cannot be mistaken for evidence.
		return gate.StatusKey + " could not be verified: the verdicts could not be read"
	}
	if len(rows) == 0 {
		return gate.StatusKey + " has no acceptance criteria: there is nothing to verify, " +
			"and an issue with none goes back to whoever scoped it"
	}

	var unruled, failed []string
	for _, row := range rows {
		switch {
		// RuledAt is the two-state answer; Passed is coalesced and means
		// nothing on its own. See the query — reading Passed without this check
		// would turn "nobody looked" into "it failed".
		case !row.RuledAt.Valid:
			unruled = append(unruled, fmt.Sprintf("%d", row.Ordinal))
		case !row.Passed:
			failed = append(failed, fmt.Sprintf("%d", row.Ordinal))
		}
	}
	// Failures first: a criterion that was checked and failed is a different
	// message from one nobody has looked at, and the first is actionable now.
	if len(failed) > 0 {
		return fmt.Sprintf("%s: criterion %s failed",
			gate.StatusKey, strings.Join(failed, ", "))
	}
	if len(unruled) > 0 {
		return fmt.Sprintf("%s: criterion %s has no verdict yet",
			gate.StatusKey, strings.Join(unruled, ", "))
	}
	return ""
}

// scriptedRefusal is the gate that asks nobody.
//
// It reads what GitHub already told Multica: `check_run`, `check_suite` and
// `status` webhooks land in github_pull_request_check_run, one row per check,
// with its name, status and conclusion. Every check in Galactics' PR gate
// already arrives here. Until now nothing was allowed to refuse on it.
//
// Three refusals, and they are deliberately distinct because they need
// different actions from whoever reads them:
//
//   - no change proposal attached — there is nothing to check. A scripted gate
//     with no PR must not pass by vacuous truth, which is what "every required
//     check succeeded" evaluates to over an empty set.
//   - a required check has not reported on this head — pending, or the name is
//     wrong. Both are "wait or fix the name", not "your code is bad".
//   - a required check failed — the only one that means what it says.
func (h *Handler) scriptedRefusal(ctx context.Context, issue db.Issue, gate db.LevelGate) string {
	required := gate.RequiredChecks
	if len(required) == 0 {
		// The CHECK constraint makes this unreachable. If it is ever reached, a
		// migration got ahead of this file, and a gate that verifies nothing
		// must not silently open.
		return gate.StatusKey + " is gated on checks, but names none"
	}

	rows, err := h.Queries.ListIssueCheckRuns(ctx, issue.ID)
	if err != nil {
		// FAIL CLOSED. Everywhere else in this file a lookup failure widens what
		// is allowed, because refusing on a failed COUNT would look like a
		// rejected review. Here the opposite: the whole value of a scripted gate
		// is that it cannot be talked past, and "the database was briefly
		// unavailable" is not evidence that nine scripts passed.
		return gate.StatusKey + " could not be verified: the check results could not be read"
	}
	if len(rows) == 0 {
		return gate.StatusKey + " is gated on " + strings.Join(required, ", ") +
			", and no change proposal is attached to this issue"
	}

	// Per PR, because every attached proposal has to pass. A green PR next to a
	// red one is not a passing gate.
	type verdict struct{ status, conclusion string }
	byPR := map[string]map[string]verdict{}
	prNumber := map[string]int32{}
	for _, row := range rows {
		key := uuidToString(row.PrID)
		prNumber[key] = row.PrNumber
		if _, ok := byPR[key]; !ok {
			byPR[key] = map[string]verdict{}
		}
		if !row.CheckName.Valid {
			// The LEFT JOIN's empty side: a PR at a head with no checks yet.
			continue
		}
		byPR[key][row.CheckName.String] = verdict{
			status:     row.CheckStatus.String,
			conclusion: row.CheckConclusion.String,
		}
	}

	for prID, checks := range byPR {
		for _, name := range required {
			got, reported := checks[name]
			if !reported {
				return fmt.Sprintf("%s is gated on %q, which has not reported on PR #%d's current head",
					gate.StatusKey, name, prNumber[prID])
			}
			if got.status != "completed" {
				return fmt.Sprintf("%s is gated on %q, which is still running on PR #%d",
					gate.StatusKey, name, prNumber[prID])
			}
			if got.conclusion != "success" {
				return fmt.Sprintf("%s is gated on %q, which concluded %q on PR #%d",
					gate.StatusKey, name, got.conclusion, prNumber[prID])
			}
		}
	}
	return ""
}

// ratifierRefusal is the "who accepts the return" half, split out so the rule is
// readable on its own.
//
// `human` tests the KIND, not the identity: the floor Galactics states is that
// a person passed it, and naming which person is a staffing decision this table
// does not hold. `agent` tests identity, because an agent ratifier is a named
// check — AP-5 against the criteria — and any-agent would let the agent that
// did the work accept its own return.
func ratifierRefusal(gate db.LevelGate, actorType, actorID string) string {
	switch gate.RatifierType {
	case ratifierHuman:
		// The column says `human` because that is Galactics' word for it and
		// this spike does not rename the doctrine to match the product. Multica
		// calls the same actor a MEMBER — resolveActor returns "member" or
		// "agent", and there is no "human". The translation lives here, on one
		// line, rather than being smuggled into the schema.
		if actorType == actorMember {
			return ""
		}
		return fmt.Sprintf(
			"%s is ratified by a human: an agent cannot move a unit out of it",
			gate.StatusKey)
	case ratifierCheck:
		// Handled by scriptedRefusal, which needs the database. Reaching here
		// means a caller bypassed checkLevelGate; refusing everyone is the safe
		// direction for the one ratifier kind that cannot be argued with.
		return gate.StatusKey + " is gated on checks, and they were not read"
	case ratifierAgent:
		if actorType == ratifierAgent && gate.RatifierID.Valid &&
			strings.EqualFold(actorID, uuidToString(gate.RatifierID)) {
			return ""
		}
		return fmt.Sprintf(
			"%s is ratified by one named agent, and this is not it",
			gate.StatusKey)
	}
	// An unknown ratifier type cannot be satisfied by anyone, so it must not
	// refuse everyone either. The CHECK constraint makes this unreachable; if it
	// is ever reached, a migration got ahead of this file.
	return ""
}

// writeLevelGateRefusal renders a refusal as 409, not 400. The request is
// well-formed and the status exists; what failed is that the unit is not
// allowed to be there YET, which is a conflict with the issue's current state.
func writeLevelGateRefusal(w http.ResponseWriter, refusal string) {
	writeError(w, http.StatusConflict, refusal)
}

// LevelGateEntry is one gate on one rung, as the API renders it.
type LevelGateEntry struct {
	Level            string   `json:"level"`
	Position         int32    `json:"position"`
	StatusKey        string   `json:"status_key"`
	RatifierType     string   `json:"ratifier_type"`
	RatifierID       *string  `json:"ratifier_id,omitempty"`
	RequiredChecks   []string `json:"required_checks,omitempty"`
	RequiresVerdicts bool     `json:"requires_verdicts"`
}

func levelGateToEntry(gate db.LevelGate) LevelGateEntry {
	entry := LevelGateEntry{
		Level:        gate.Level,
		Position:     gate.Position,
		StatusKey:    gate.StatusKey,
		RatifierType: gate.RatifierType,
	}
	if gate.RatifierID.Valid {
		id := uuidToString(gate.RatifierID)
		entry.RatifierID = &id
	}
	entry.RequiredChecks = gate.RequiredChecks
	entry.RequiresVerdicts = gate.RequiresVerdicts
	return entry
}

// ListLevelGates returns every declared gate, ordered by rung then position.
func (h *Handler) ListLevelGates(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	gates, err := h.Queries.ListAllLevelGates(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list level gates")
		return
	}
	entries := make([]LevelGateEntry, 0, len(gates))
	for _, gate := range gates {
		entries = append(entries, levelGateToEntry(gate))
	}
	writeJSON(w, http.StatusOK, map[string]any{"gates": entries})
}

// SetLevelGate declares (or moves) one gate on one rung. An empty status_key
// deletes the gate at that position, for the same reason SetLevelPolicy treats
// three empty fields as a clear: "remove this gate" is a reasonable thing to
// ask and should not come back as a malformed request.
func (h *Handler) SetLevelGate(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	level := chi.URLParam(r, "level")
	if !slices.Contains(missionRungs, level) {
		writeError(w, http.StatusBadRequest, "unknown level: "+strings.Join(missionRungs, ", "))
		return
	}

	var req struct {
		Position         int32    `json:"position"`
		StatusKey        string   `json:"status_key"`
		RatifierType     string   `json:"ratifier_type"`
		RatifierID       string   `json:"ratifier_id"`
		RequiredChecks   []string `json:"required_checks"`
		RequiresVerdicts bool     `json:"requires_verdicts"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Position < 1 {
		writeError(w, http.StatusBadRequest, "position must be 1 or more — it is the order the gates are walked in")
		return
	}

	if strings.TrimSpace(req.StatusKey) == "" {
		if err := h.Queries.DeleteLevelGate(r.Context(), db.DeleteLevelGateParams{
			WorkspaceID: wsUUID,
			Level:       level,
			Position:    req.Position,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear level gate")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"cleared": true})
		return
	}

	// The status has to exist in THIS workspace's catalog. status_key is not an
	// FK (see migration 488 for why), so this is the check that replaces it —
	// and without it a typo produces a gate nothing can ever be standing at,
	// which reads as "no gate" and silently stops refusing anything.
	statusKey, ok := h.resolveIssueStatusKey(w, r, wsUUID, req.StatusKey)
	if !ok {
		return
	}

	ratifierType := strings.TrimSpace(req.RatifierType)
	if ratifierType == "" {
		ratifierType = ratifierHuman
	}
	if ratifierType != ratifierHuman && ratifierType != ratifierAgent && ratifierType != ratifierCheck {
		writeError(w, http.StatusBadRequest,
			"ratifier_type is one of "+ratifierHuman+", "+ratifierAgent+", "+ratifierCheck+
				" — `lead` is deliberately absent until somebody says what it means for a gate")
		return
	}

	var requiredChecks []string
	if ratifierType == ratifierCheck {
		for _, name := range req.RequiredChecks {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				requiredChecks = append(requiredChecks, trimmed)
			}
		}
		if len(requiredChecks) == 0 {
			// A check gate naming nothing verifies nothing and opens for
			// anyone, while reading as configured. That is the worst failure
			// available here, so it is refused at declaration rather than
			// discovered at the gate.
			writeError(w, http.StatusBadRequest,
				"a check ratifier needs required_checks: a gate that names no check verifies nothing")
			return
		}
	}

	var ratifierID pgtype.UUID
	if ratifierType == ratifierAgent {
		if strings.TrimSpace(req.RatifierID) == "" {
			writeError(w, http.StatusBadRequest,
				"an agent ratifier needs ratifier_id: any-agent would let the agent that did the work accept its own return")
			return
		}
		parsed, ok := parseUUIDOrBadRequest(w, req.RatifierID, "ratifier_id")
		if !ok {
			return
		}
		agent, err := h.Queries.GetAgent(r.Context(), parsed)
		if err != nil || uuidToString(agent.WorkspaceID) != uuidToString(wsUUID) {
			writeError(w, http.StatusBadRequest, "ratifier_id is not an agent in this workspace")
			return
		}
		ratifierID = parsed
	}

	gate, err := h.Queries.SetLevelGate(r.Context(), db.SetLevelGateParams{
		WorkspaceID:      wsUUID,
		Level:            level,
		Position:         req.Position,
		StatusKey:        statusKey,
		RatifierType:     ratifierType,
		RatifierID:       ratifierID,
		RequiredChecks:   requiredChecks,
		RequiresVerdicts: req.RequiresVerdicts,
	})
	if err != nil {
		// The unique index on (workspace, level, status_key) is the likely
		// failure: two gates on one status makes "which gate is this unit at"
		// unanswerable, and the whole walk depends on that lookup.
		writeError(w, http.StatusConflict,
			"failed to set level gate — a rung cannot gate the same status twice")
		return
	}
	writeJSON(w, http.StatusOK, levelGateToEntry(gate))
}
