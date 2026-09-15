// SPIKE (not upstream): the mission view.
//
// The board answers "what is the state of each ticket". A mission is a tree —
// a parent carrying a goal, children grouped into ordered stages, and their own
// children below — and no view reads it that way. This endpoint answers the two
// questions the board cannot: where is the mission, and what is waiting on me.
//
// READ ONLY, and that is a hard property rather than a current fact. There is
// no mutation in this file, no new table behind it, and nothing it reports is
// stored: every number is derived from rows the product already writes, on
// every request. If a future change here needs to write something, it is not
// this view any more.
package handler

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// missionMaxDepth bounds the recursive walk.
//
// A payload guard, not a cycle guard: the issue tree is acyclic by
// construction. Four levels covers the shape the design note describes —
// mission, sub-mission, feature, ticket — with one spare. A tree deeper than
// this comes back truncated and says so, rather than silently losing its
// bottom.
const missionMaxDepth = 5

// missionWaitingReason is why a unit is parked. Each value is a state the
// product already records; none is inferred from absence.
type missionWaitingReason string

const (
	// waitingHandRaised: an agent stopped and asked for a decision it may not
	// take. The only reason that carries the question itself.
	waitingHandRaised missionWaitingReason = "hand_raised"
	// waitingBlocked: the issue is in `blocked`. The product's own word; what
	// blocks it is not recorded anywhere, so the view does not guess.
	waitingBlocked missionWaitingReason = "blocked"
	// waitingReview: the issue is in `in_review`. Waiting on a reviewer is what
	// the status means.
	waitingReview missionWaitingReason = "in_review"
	// waitingRunFailed: the most recent run ended in failure and nothing has
	// re-run since. The failure_reason travels with it.
	waitingRunFailed missionWaitingReason = "run_failed"
	// waitingStageNotPromoted: the stage below this one closed and nobody
	// promoted this stage. It carries its own clock — how long the barrier has
	// been open — and it is the one way a mission stops without anything
	// looking wrong. It is NOT in the waiting list: nothing asked for a
	// decision, so it is reported separately.
	waitingStageNotPromoted missionWaitingReason = "stage_not_promoted"
)

// MissionNode is one issue in the tree.
//
// assignee_type / assignee_id travel as ids, not names, matching IssueResponse.
// The client already holds the agent, member and squad catalogs for the
// workspace; resolving names here would duplicate that and add queries to an
// endpoint whose whole point is a fixed query budget.
type MissionNode struct {
	ID             string  `json:"id"`
	ParentID       *string `json:"parent_id"`
	Identifier     string  `json:"identifier"`
	Number         int32   `json:"number"`
	Title          string  `json:"title"`
	Status         string  `json:"status"`
	StatusCategory string  `json:"status_category,omitempty"`
	Priority       string  `json:"priority"`
	AssigneeType   *string `json:"assignee_type"`
	AssigneeID     *string `json:"assignee_id"`
	Stage          *int32  `json:"stage"`
	Depth          int32   `json:"depth"`
	Terminal       bool    `json:"terminal"`

	// WaitingBelow is true when this node, or anything beneath it, needs a
	// human — either in the waiting list or stalled at an un-promoted barrier.
	// It is what lets a collapsed branch still say that something inside it is
	// on you.
	WaitingBelow bool `json:"waiting_below"`

	// Usage is absent when the issue has no metered run — which is different
	// from a run that cost nothing, and the client renders the two differently.
	// cost_usd_ticks is the same unit every other usage endpoint emits; the
	// view neither converts nor recomputes it.
	Usage *MissionNodeUsage `json:"usage,omitempty"`
}

type MissionNodeUsage struct {
	TotalInputTokens      int64 `json:"total_input_tokens"`
	TotalOutputTokens     int64 `json:"total_output_tokens"`
	TotalCacheReadTokens  int64 `json:"total_cache_read_tokens"`
	TotalCacheWriteTokens int64 `json:"total_cache_write_tokens"`
	TotalCostUsdTicks     int64 `json:"total_cost_usd_ticks"`
	TaskCount             int32 `json:"task_count"`
}

// MissionStage is one barrier group among a parent's direct children.
//
// The rule is the product's, not the view's: see stageBarrierClosed in
// issue_child_done.go. A sibling set where no child carries a stage is ONE
// implicit stage. In a staged set, unstaged children are ignored by the
// frontier entirely — which is why UnstagedIgnored exists below.
type MissionStage struct {
	// Stage is nil for the implicit single stage of an unstaged sibling set.
	Stage *int32 `json:"stage"`
	// Total and Terminal count only the children this stage actually gates.
	Total    int `json:"total"`
	Terminal int `json:"terminal"`
	// Closed mirrors the barrier: every child in this stage AND every stage
	// below it is terminal. A stage can have all its own children done and
	// still be open, because the barrier is cumulative.
	Closed bool `json:"closed"`
	// Frontier marks the lowest stage that is not closed — the one the parent
	// is actually waiting on. At most one stage carries it.
	Frontier bool     `json:"frontier"`
	IssueIDs []string `json:"issue_ids"`
}

// MissionWaitingUnit is one parked unit, anywhere in the tree.
type MissionWaitingUnit struct {
	IssueID    string               `json:"issue_id"`
	Identifier string               `json:"identifier"`
	Title      string               `json:"title"`
	Status     string               `json:"status"`
	Depth      int32                `json:"depth"`
	Reason     missionWaitingReason `json:"reason"`
	// Detail is the reason in the system's own words — the raised hand's
	// question, or a failure_reason. Empty when the status IS the whole reason.
	Detail string `json:"detail,omitempty"`
	// Since is when the wait started, and SinceExact says whether that is the
	// real transition time or a fallback. A duration the view is not sure of is
	// labelled rather than rounded into looking certain.
	Since      time.Time `json:"since"`
	SinceExact bool      `json:"since_exact"`
	WaitedSecs int64     `json:"waited_seconds"`
	// Hand is present only for a hand_raised row.
	Hand *MissionHand `json:"hand,omitempty"`
}

type MissionHand struct {
	ID             string       `json:"id"`
	IssueID        string       `json:"issue_id"`
	Question       string       `json:"question"`
	Options        []handOption `json:"options"`
	Recommendation string       `json:"recommendation,omitempty"`
	AgentID        string       `json:"agent_id,omitempty"`
	AgentName      string       `json:"agent_name,omitempty"`
	CreatedAt      time.Time    `json:"created_at"`
}

// MissionReferential is a group of open hands that interrogate the same body of
// knowledge. Twelve on the design system and none on architecture says which
// referential is too thin to answer on its own — that is the whole point of the
// view, and the reason the waiting list is a measurement and not just a queue.
type MissionReferential struct {
	Key   string        `json:"key"`
	Label string        `json:"label"`
	Count int           `json:"count"`
	Hands []MissionHand `json:"hands"`
}

type MissionResponse struct {
	Root    MissionNode          `json:"root"`
	Nodes   []MissionNode        `json:"nodes"`
	Stages  []MissionStage       `json:"stages"`
	Waiting []MissionWaitingUnit `json:"waiting"`
	// Stalled is the silent failure the waiting list cannot catch: a stage
	// whose predecessor closed and which nobody promoted. Nothing is asking for
	// anything, every unit looks fine, and the mission has stopped. Kept out of
	// Waiting so that list stays "things that named a reason".
	Stalled []MissionWaitingUnit `json:"stalled"`

	Referentials []MissionReferential `json:"referentials"`
	// ReferentialStandIn is true while hands carry no referential of their own
	// and the grouping falls back to the raising agent. The UI must say so: a
	// diagnostic that looks authoritative while grouping by a proxy is worse
	// than no diagnostic.
	ReferentialStandIn bool   `json:"referential_stand_in"`
	ReferentialField   string `json:"referential_field"`

	// UnstagedIgnored lists children that carry no stage in a sibling set that
	// is otherwise staged. The barrier skips them entirely, so they can sit
	// under a parent forever without ever gating anything — invisible today,
	// and the view's job to surface.
	UnstagedIgnored []string `json:"unstaged_ignored"`

	Truncated bool `json:"truncated"`
	MaxDepth  int  `json:"max_depth"`
}

// missionReferentialField names the hand field the grouping reads.
//
// THIS IS THE SINGLE PLACE TO CHANGE when a real referential lands on the
// raised hand: point missionReferential at the new field and set
// referentialStandIn to false. Nothing else in the server or the client knows
// how the grouping is derived.
const missionReferentialField = "agent_id"

// missionReferentialStandIn is true while the grouping is a proxy.
const missionReferentialStandIn = true

// missionReferential returns the referential a hand interrogates.
//
// A raised hand does not carry one yet. The closest honest proxy is the agent
// that raised it: agents are roles here — a designer's questions land on the
// design system, a backend agent's on the API contract — so the raiser's
// identity approximates the body of knowledge that failed to answer. It is a
// proxy and the response says so; an agent that spans two referentials, or two
// agents sharing one, both defeat it.
func missionReferential(hand MissionHand) (key, label string) {
	if hand.AgentID == "" {
		return "unattributed", "Unattributed"
	}
	label = hand.AgentName
	if label == "" {
		label = "Agent " + hand.AgentID
	}
	return hand.AgentID, label
}

// GetMission returns the tree under one issue, what is parked in it, and which
// referential the open questions are interrogating.
func (h *Handler) GetMission(w http.ResponseWriter, r *http.Request) {
	root, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	maxDepth := missionMaxDepth
	if raw := r.URL.Query().Get("depth"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 1 && parsed <= missionMaxDepth {
			maxDepth = parsed
		}
	}

	// 1 — the tree.
	rows, err := h.Queries.ListMissionTree(r.Context(), db.ListMissionTreeParams{
		RootID:      root.ID,
		WorkspaceID: root.WorkspaceID,
		MaxDepth:    int32(maxDepth),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load mission tree")
		return
	}
	if len(rows) == 0 {
		writeError(w, http.StatusNotFound, "issue not found")
		return
	}

	issueIDs := make([]pgtype.UUID, 0, len(rows))
	for _, row := range rows {
		issueIDs = append(issueIDs, row.ID)
	}

	// 2-5 — everything else, each taking the whole id set. The budget is fixed:
	// a five-node mission and a five-hundred-node mission cost the same number
	// of round trips.
	usage, err := h.Queries.ListMissionIssueUsage(r.Context(), issueIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load mission usage")
		return
	}
	hands, err := h.Queries.ListMissionOpenHands(r.Context(), issueIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load raised hands")
		return
	}
	tasks, err := h.Queries.ListMissionLatestTasks(r.Context(), issueIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load mission runs")
		return
	}
	changes, err := h.Queries.ListMissionLatestStatusChanges(r.Context(), issueIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load status history")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), root.WorkspaceID)
	resolver := issuestatus.NewResolver(root.WorkspaceID)
	now := time.Now().UTC()

	usageByIssue := make(map[string]*MissionNodeUsage, len(usage))
	for _, u := range usage {
		usageByIssue[uuidToString(u.IssueID)] = &MissionNodeUsage{
			TotalInputTokens:      u.TotalInputTokens,
			TotalOutputTokens:     u.TotalOutputTokens,
			TotalCacheReadTokens:  u.TotalCacheReadTokens,
			TotalCacheWriteTokens: u.TotalCacheWriteTokens,
			TotalCostUsdTicks:     u.TotalCostUsdTicks,
			TaskCount:             u.TaskCount,
		}
	}

	handsByIssue := make(map[string]MissionHand, len(hands))
	allHands := make([]MissionHand, 0, len(hands))
	for _, row := range hands {
		var options []handOption
		_ = json.Unmarshal(row.Options, &options)
		hand := MissionHand{
			ID:        uuidToString(row.ID),
			IssueID:   uuidToString(row.IssueID),
			Question:  row.Question,
			Options:   options,
			CreatedAt: row.CreatedAt.Time.UTC(),
		}
		if row.Recommendation.Valid {
			hand.Recommendation = row.Recommendation.String
		}
		if row.AgentID.Valid {
			hand.AgentID = uuidToString(row.AgentID)
		}
		if row.AgentName.Valid {
			hand.AgentName = row.AgentName.String
		}
		// One open hand per issue is enforced by a partial unique index, so the
		// last write here cannot overwrite a second live question.
		handsByIssue[hand.IssueID] = hand
		allHands = append(allHands, hand)
	}

	tasksByIssue := make(map[string]db.ListMissionLatestTasksRow, len(tasks))
	for _, t := range tasks {
		tasksByIssue[uuidToString(t.IssueID)] = t
	}

	type statusChange struct {
		at time.Time
		to string
	}
	changeByIssue := make(map[string]statusChange, len(changes))
	for _, c := range changes {
		to := ""
		if c.ToStatus != nil {
			if s, ok := c.ToStatus.(string); ok {
				to = s
			}
		}
		changeByIssue[uuidToString(c.IssueID)] = statusChange{at: c.CreatedAt.Time.UTC(), to: to}
	}

	nodes := make([]MissionNode, 0, len(rows))
	nodeIndex := make(map[string]int, len(rows))
	waiting := make([]MissionWaitingUnit, 0)

	for _, row := range rows {
		id := uuidToString(row.ID)
		effective := resolver.Effective(r.Context(), h.Queries, row.Status)
		category := resolver.Category(r.Context(), h.Queries, row.Status)

		node := MissionNode{
			ID:             id,
			Identifier:     prefix + "-" + strconv.Itoa(int(row.Number)),
			Number:         row.Number,
			Title:          row.Title,
			Status:         row.Status,
			StatusCategory: issuestatus.WireCategory(row.Status, category),
			Priority:       row.Priority,
			Depth:          row.Depth,
			Terminal:       isTerminalChildStatus(effective),
			Usage:          usageByIssue[id],
		}
		if row.ParentIssueID.Valid {
			pid := uuidToString(row.ParentIssueID)
			node.ParentID = &pid
		}
		if row.AssigneeType.Valid {
			at := row.AssigneeType.String
			node.AssigneeType = &at
		}
		if row.AssigneeID.Valid {
			aid := uuidToString(row.AssigneeID)
			node.AssigneeID = &aid
		}
		if row.Stage.Valid {
			s := row.Stage.Int32
			node.Stage = &s
		}

		nodeIndex[id] = len(nodes)
		nodes = append(nodes, node)

		// The waiting list. Only reasons the system can name — the alternative
		// is listing every backlog child of a staged mission, most of which are
		// waiting their turn exactly as designed, and the list stops being the
		// thing you open first.
		if unit, parked := missionWaitingUnit(node, effective, handsByIssue[id], tasksByIssue[id], changeByIssue[id], row.UpdatedAt.Time.UTC(), now); parked {
			waiting = append(waiting, unit)
		}
	}

	// Longest wait first. Deliberately not by priority: the question this list
	// answers is "what has been sitting on me", and a low-priority unit parked
	// for six days is more interesting than a high-priority one parked for an
	// hour. Priority is on the board, which is one click away.
	sort.SliceStable(waiting, func(i, j int) bool {
		return waiting[i].WaitedSecs > waiting[j].WaitedSecs
	})

	stages, unstagedIgnored := missionStages(nodes, uuidToString(root.ID))

	// Stalled barriers, at EVERY level — a branch three deep whose stage 2 was
	// never promoted stops that branch just as dead as one at the root.
	waitingIDs := make(map[string]struct{}, len(waiting))
	for _, unit := range waiting {
		waitingIDs[unit.IssueID] = struct{}{}
	}
	stalled := missionStalled(nodes, waitingIDs, func(id string) (time.Time, bool) {
		if change, ok := changeByIssue[id]; ok && !change.at.IsZero() {
			return change.at, true
		}
		return time.Time{}, false
	}, func(id string) time.Time {
		if idx, ok := nodeIndex[id]; ok {
			return rows[idx].UpdatedAt.Time.UTC()
		}
		return now
	}, now)

	// Rolled up over both lists: a collapsed branch must say that something
	// inside it is on you, and a stalled barrier is on you exactly as much as a
	// raised hand is.
	markWaitingBelow(nodes, nodeIndex, append(append([]MissionWaitingUnit{}, waiting...), stalled...))

	resp := MissionResponse{
		Root:               nodes[0],
		Nodes:              nodes,
		Stages:             stages,
		Waiting:            waiting,
		Stalled:            stalled,
		Referentials:       missionReferentials(allHands),
		ReferentialStandIn: missionReferentialStandIn,
		ReferentialField:   missionReferentialField,
		UnstagedIgnored:    unstagedIgnored,
		Truncated:          missionTruncated(rows, maxDepth),
		MaxDepth:           maxDepth,
	}
	writeMeasuredJSON(w, http.StatusOK, resp)
}

// missionWaitingUnit decides whether one node is parked, and on what.
//
// The order is the order of certainty. A raised hand names its own question and
// its own start time, so it wins over anything derived from a status. A failed
// run is checked last because an issue can be in_review AND have a failed run,
// and the review is the thing a human is actually holding.
func missionWaitingUnit(
	node MissionNode,
	effective string,
	hand MissionHand,
	task db.ListMissionLatestTasksRow,
	change struct {
		at time.Time
		to string
	},
	updatedAt time.Time,
	now time.Time,
) (MissionWaitingUnit, bool) {
	unit := MissionWaitingUnit{
		IssueID:    node.ID,
		Identifier: node.Identifier,
		Title:      node.Title,
		Status:     node.Status,
		Depth:      node.Depth,
	}

	// A terminal unit is not waiting on anyone, whatever else is attached to it.
	if node.Terminal {
		return unit, false
	}

	switch {
	case hand.ID != "":
		unit.Reason = waitingHandRaised
		unit.Detail = hand.Question
		unit.Since = hand.CreatedAt
		unit.SinceExact = true
		handCopy := hand
		unit.Hand = &handCopy

	case effective == issuestatus.Blocked, effective == issuestatus.InReview:
		if effective == issuestatus.Blocked {
			unit.Reason = waitingBlocked
		} else {
			unit.Reason = waitingReview
		}
		// The transition row is the only honest answer to "since when". When the
		// latest logged change does not name the status the issue is actually
		// in, some path wrote the status without logging it; fall back to
		// updated_at and flag the duration as approximate rather than quietly
		// reporting a number that means something else.
		if !change.at.IsZero() && change.to == node.Status {
			unit.Since = change.at
			unit.SinceExact = true
		} else {
			unit.Since = updatedAt
			unit.SinceExact = false
		}

	case task.Status == "failed" && task.CompletedAt.Valid:
		unit.Reason = waitingRunFailed
		if task.FailureReason.Valid {
			unit.Detail = task.FailureReason.String
		}
		unit.Since = task.CompletedAt.Time.UTC()
		unit.SinceExact = true

	default:
		return unit, false
	}

	unit.WaitedSecs = int64(now.Sub(unit.Since).Seconds())
	if unit.WaitedSecs < 0 {
		unit.WaitedSecs = 0
	}
	return unit, true
}

// markWaitingBelow sets WaitingBelow on every ancestor of a parked unit.
//
// Walks child → parent, which terminates because the tree is acyclic and every
// parent id resolves to a node earlier in the breadth-first ordering.
func markWaitingBelow(nodes []MissionNode, index map[string]int, waiting []MissionWaitingUnit) {
	for _, unit := range waiting {
		cursor, ok := index[unit.IssueID]
		for ok {
			nodes[cursor].WaitingBelow = true
			parent := nodes[cursor].ParentID
			if parent == nil {
				break
			}
			cursor, ok = index[*parent]
		}
	}
}

// missionStages groups the root's direct children into barrier groups.
//
// The rule is stageBarrierClosed's, restated here because the view needs
// structured counts and that function returns a bool for one completion. Keep
// the two in step:
//
//   - no child carries a stage  -> one implicit stage, closed when all are
//     terminal;
//   - any child carries a stage -> a staged set, and unstaged children are
//     ignored by the frontier entirely;
//   - a stage is closed when every staged child at or below it is terminal.
//     The barrier is CUMULATIVE, so stage 2 can have all its own children done
//     and still be open because stage 1 does not.
func missionStages(nodes []MissionNode, rootID string) ([]MissionStage, []string) {
	children := make([]MissionNode, 0)
	for _, node := range nodes {
		if node.ParentID != nil && *node.ParentID == rootID {
			children = append(children, node)
		}
	}
	unstagedIgnored := make([]string, 0)
	if len(children) == 0 {
		return []MissionStage{}, unstagedIgnored
	}

	staged := false
	for _, child := range children {
		if child.Stage != nil {
			staged = true
			break
		}
	}

	if !staged {
		stage := MissionStage{IssueIDs: make([]string, 0, len(children))}
		for _, child := range children {
			stage.Total++
			if child.Terminal {
				stage.Terminal++
			}
			stage.IssueIDs = append(stage.IssueIDs, child.ID)
		}
		stage.Closed = stage.Terminal == stage.Total
		stage.Frontier = !stage.Closed
		return []MissionStage{stage}, unstagedIgnored
	}

	byStage := map[int32]*MissionStage{}
	order := make([]int32, 0)
	for _, child := range children {
		if child.Stage == nil {
			// Ignored by the barrier, so it gates nothing. Reported separately
			// rather than folded into a stage that would misstate its own total.
			unstagedIgnored = append(unstagedIgnored, child.ID)
			continue
		}
		s := *child.Stage
		entry, ok := byStage[s]
		if !ok {
			value := s
			entry = &MissionStage{Stage: &value, IssueIDs: make([]string, 0, 2)}
			byStage[s] = entry
			order = append(order, s)
		}
		entry.Total++
		if child.Terminal {
			entry.Terminal++
		}
		entry.IssueIDs = append(entry.IssueIDs, child.ID)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	out := make([]MissionStage, 0, len(order))
	cumulativeClosed := true
	frontierTaken := false
	for _, s := range order {
		entry := byStage[s]
		cumulativeClosed = cumulativeClosed && entry.Terminal == entry.Total
		entry.Closed = cumulativeClosed
		if !entry.Closed && !frontierTaken {
			entry.Frontier = true
			frontierTaken = true
		}
		out = append(out, *entry)
	}
	return out, unstagedIgnored
}

// missionReferentials groups open hands, largest group first.
func missionReferentials(hands []MissionHand) []MissionReferential {
	byKey := map[string]*MissionReferential{}
	order := make([]string, 0)
	for _, hand := range hands {
		key, label := missionReferential(hand)
		entry, ok := byKey[key]
		if !ok {
			entry = &MissionReferential{Key: key, Label: label, Hands: make([]MissionHand, 0, 2)}
			byKey[key] = entry
			order = append(order, key)
		}
		entry.Count++
		entry.Hands = append(entry.Hands, hand)
	}
	out := make([]MissionReferential, 0, len(order))
	for _, key := range order {
		out = append(out, *byKey[key])
	}
	// Largest first: the whole diagnostic is "which referential cannot answer
	// on its own", and that is the tallest bar.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	return out
}

// missionTruncated reports whether the walk stopped at the depth bound with
// children still below. A node AT the bound may or may not have children; the
// query cannot tell, so the flag means "possibly incomplete", and the response
// carries max_depth so the client can say which level it stopped at.
func missionTruncated(rows []db.ListMissionTreeRow, maxDepth int) bool {
	for _, row := range rows {
		if int(row.Depth) >= maxDepth {
			return true
		}
	}
	return false
}

// missionStalled finds stages whose predecessor closed and which nobody
// promoted, anywhere in the tree.
//
// This is the failure the waiting list structurally cannot catch. Every unit in
// a stalled stage looks fine — backlog, assigned, no failure, nothing asking
// for anything — and the mission has simply stopped. The product detects the
// barrier and wakes the parent assignee; whether anything then promotes the
// next stage is nobody's job, and when it does not happen there is no trace.
//
// The test is deliberately narrow, because the loose version of it is just
// "list every backlog child" and that floods the only list worth opening:
//
//   - the sibling set must be STAGED. An unstaged set has one implicit stage,
//     so there is no promotion step to miss;
//   - the frontier must NOT be the lowest stage. A mission whose stage 1 has
//     not started has not stalled, it has not begun;
//   - the child must be unstarted. A frontier child in progress means the stage
//     was promoted and is simply not finished;
//   - and it must not already be in the waiting list, which names a better
//     reason than this one.
//
// The clock is the barrier's, not the child's: how long this stage has been
// ready to start, measured from the last predecessor to go terminal. "Waiting
// six days" is the honest number; the child's own updated_at would report the
// day it was created.
func missionStalled(
	nodes []MissionNode,
	waitingIDs map[string]struct{},
	terminalAt func(issueID string) (time.Time, bool),
	updatedAt func(issueID string) time.Time,
	now time.Time,
) []MissionWaitingUnit {
	byParent := map[string][]MissionNode{}
	for _, node := range nodes {
		if node.ParentID == nil {
			continue
		}
		byParent[*node.ParentID] = append(byParent[*node.ParentID], node)
	}

	out := make([]MissionWaitingUnit, 0)
	for parentID, children := range byParent {
		stages, _ := missionStages(nodes, parentID)
		if len(stages) == 0 || stages[0].Stage == nil {
			continue // unstaged set: no promotion step to miss
		}
		frontierIdx := -1
		for i := range stages {
			if stages[i].Frontier {
				frontierIdx = i
				break
			}
		}
		// No frontier means every stage closed. A frontier at index 0 means
		// nothing below it ever closed, so nothing was left un-promoted.
		if frontierIdx <= 0 {
			continue
		}
		frontier := stages[frontierIdx]

		// When the barrier opened: the last predecessor to reach terminal.
		barrierAt := time.Time{}
		exact := true
		for i := 0; i < frontierIdx; i++ {
			for _, id := range stages[i].IssueIDs {
				at, ok := terminalAt(id)
				if !ok {
					exact = false
					continue
				}
				if at.After(barrierAt) {
					barrierAt = at
				}
			}
		}

		for _, child := range children {
			if child.Stage == nil || *child.Stage != *frontier.Stage {
				continue
			}
			if child.Terminal {
				continue
			}
			if _, already := waitingIDs[child.ID]; already {
				continue
			}
			// Unstarted only. `backlog` is the product's parking lot and the
			// only status that means "never promoted"; anything else means the
			// stage was promoted and is in flight.
			if child.Status != issuestatus.Backlog {
				continue
			}

			since := barrierAt
			sinceExact := exact && !barrierAt.IsZero()
			if since.IsZero() {
				since = updatedAt(child.ID)
				sinceExact = false
			}
			waited := int64(now.Sub(since).Seconds())
			if waited < 0 {
				waited = 0
			}
			out = append(out, MissionWaitingUnit{
				IssueID:    child.ID,
				Identifier: child.Identifier,
				Title:      child.Title,
				Status:     child.Status,
				Depth:      child.Depth,
				Reason:     waitingStageNotPromoted,
				Detail:     "stage " + strconv.Itoa(int(*frontier.Stage)) + " was never promoted",
				Since:      since,
				SinceExact: sinceExact,
				WaitedSecs: waited,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].WaitedSecs != out[j].WaitedSecs {
			return out[i].WaitedSecs > out[j].WaitedSecs
		}
		return out[i].Identifier < out[j].Identifier
	})
	return out
}
