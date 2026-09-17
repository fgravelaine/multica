// SPIKE (not upstream): the mission view.
//
// The board answers "what is the state of each ticket". A mission is a tree —
// a parent carrying a goal, children grouped into ordered stages, and their own
// children below — and no view reads it that way. This endpoint answers the two
// questions the board cannot: where is the mission, and what is waiting on me.
//
// THE VIEW IS READ ONLY, and that is a hard property rather than a current
// fact: GetMission and the board derive every number from rows the product
// already writes, on every request, and store nothing of their own.
//
// Two writers now share the file, and they are not part of the view — they are
// the vocabulary it needs to have something to read:
//
//	SetIssueLevel       declares what a unit is meant to be, which is the only
//	                    way a unit can disagree with its own parentage.
//	SetIssueDependency  declares that a unit waits on another, which is the
//	                    only way a wait can reach outside one parent.
//
// Both are single-column writes on their own endpoints. Neither is reachable
// from a read path. If a change makes GetMission or ListMissions write
// anything, it is not this view any more.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
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

// missionBoardDepth bounds the workspace-wide walk the board does.
//
// Deeper than the detail view's bound on purpose: the detail view truncates a
// payload and says so, but the board COUNTS, and a count that silently omits
// everything past depth 5 is a wrong number rather than a short list. Nothing
// realistic reaches 20, and a tree that does has a problem the board is not
// the place to report.
const missionBoardDepth = 20

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

	// Level is what this unit IS.
	//
	//   campaign  — a body of work with many missions in it.
	//   mission   — task + purpose. Carries the intent and the end state.
	//   objective — what must be taken and held for the mission to succeed.
	//               Decisive: you can tell whether you hold it.
	//   task      — what one unit is ordered to do AND REPORTS ON.
	//   step      — how it does it. Below the reporting line.
	//
	// READ THIS BEFORE BUILDING ON IT. The words are borrowed from military
	// usage, where each is precise about what a thing obliges. The ORDERING is
	// not — doctrine does not rank them. A squad has a mission; an objective is
	// assigned at any echelon; doctrine's hierarchy is units, not work items.
	// The ladder is a local convention and has to earn its place on what it does
	// here, not on borrowed authority.
	//
	// What it earns, boundary by boundary:
	//
	//   campaign/mission   SHAPE ONLY. A campaign is parent_issue_id IS NULL, so
	//                      it has no parent to wake. Nothing else differs.
	//   mission/objective  NOTHING. No code anywhere keys on depth.
	//   objective/task     NOTHING, likewise.
	//
	// A campaign is NOT Multica's `project`. That was an earlier mistake here:
	// project_id is a flat per-issue tag that is not inherited, so it groups
	// across the tree rather than sitting above it. Its right use is the
	// PRODUCT — every issue in Veezeet carries it, at any depth, as a filter.
	// The ladder is parentage, which is the one relation the product enforces.
	//   task/step          REAL, and the only rung this view itself creates:
	//                      a step is folded on the canvas until asked for.
	//                      That is what a step IS — below the reporting line.
	//
	// So objective/task is a WRITING DISCIPLINE, not a rule this server keeps:
	// a title at depth 1 has to answer "what does this promise, and can you tell
	// whether you hold it?". That is worth having — it is how "Empty state" was
	// caught sitting where an objective belongs — but people enforce it, not
	// this field. Do not add behaviour keyed on Level without first giving the
	// boundary a real consequence.
	//
	// The FLOOR is step, and it earns the name on the fold rather than on the
	// server: dispatch ignores depth and parentage, the barrier is per parent at
	// every level, and a hand can be raised anywhere — so nothing below task
	// behaves differently. What differs is what the MISSION tracks, and that is
	// a real difference this view makes and keeps. A step's own children are
	// inside a folded step, so the ladder a reader sees never repeats a name.
	//
	// A squad never appears here: a squad RECEIVES a task. Squad is who,
	// objective is what — and naming a level after its assignee would be a lie
	// the moment someone reassigns it, since assignee_type is editable at any
	// depth.
	Level string `json:"level"`

	// BlockedBy names the units this one is actually waiting on, when the thing
	// holding it is another unit rather than a person.
	//
	// Multica records no "X is blocked by Y" link. issue_dependency exists in
	// the schema with a blocks/blocked_by type, and is dead: no query, no API,
	// no client type, no UI that writes one, and no rows. The product's answer
	// to "the email needs the design validated" is the STAGE BARRIER — the
	// design in a lower stage, the email in a higher one — which is an ordering
	// rather than a link, and so cannot name a blocker across two parents or
	// two missions.
	//
	// So this is the barrier, named. A unit above the frontier is waiting on
	// exactly the frontier stage's open units, and those are the units to go
	// look at. Nothing here is invented: every entry is a sibling the product
	// already put in a lower stage and has not finished.
	BlockedBy []MissionBlocker `json:"blocked_by,omitempty"`

	// LastRun is the most recent run, absent when the issue has never run.
	//
	// The endpoint already reads this row — the waiting list is derived from it
	// — and was throwing it away for every node that is not waiting. Shipping
	// it costs nothing and is what lets a client open a unit's context without
	// a second request, which is the one rule this view may not break.
	LastRun *MissionNodeRun `json:"last_run,omitempty"`

	// StatusSince is when the issue entered the status it is in, from the
	// activity row that logged the transition. Exact is false when no such row
	// names the current status and this is issue.updated_at instead — the same
	// distinction the waiting list draws, for the same reason: a wrong duration
	// is worse than an admitted approximation.
	StatusSince      *time.Time `json:"status_since,omitempty"`
	StatusSinceExact bool       `json:"status_since_exact"`
}

// MissionBlocker is one unit standing between another and its turn.
type MissionBlocker struct {
	IssueID    string `json:"issue_id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	// Stage is the blocker's own stage, when the barrier is what produced this,
	// so a reader can see the ordering rather than take the claim on faith.
	Stage *int32 `json:"stage,omitempty"`
	// Relation is WHY it blocks, and the two are not interchangeable:
	//
	//   stage_barrier — an ordering among siblings. Cannot reach outside the
	//                   parent, so it can never explain a wait on another team.
	//   dependency    — a declared link. Reaches anywhere, including a unit in
	//                   another mission or another campaign entirely.
	//
	// A reader needs to tell them apart: a barrier clears itself when the stage
	// below closes, a dependency clears when somebody finishes a specific thing
	// that may not be on this board at all.
	Relation string `json:"relation"`
	// Outside is true when the blocker is not in this tree — the case the
	// barrier structurally cannot express, and the reason this field exists.
	Outside bool `json:"outside,omitempty"`
	// Who has the blocker, so "waiting on another team" is answerable without
	// leaving the view. Ids, resolved client-side from the workspace catalogs.
	AssigneeType *string `json:"assignee_type,omitempty"`
	AssigneeID   *string `json:"assignee_id,omitempty"`
}

const (
	blockerStageBarrier = "stage_barrier"
	blockerDependency   = "dependency"
)

const (
	levelCampaign  = "campaign"
	levelMission   = "mission"
	levelObjective = "objective"
	levelTask      = "task"
	levelStep      = "step"
)

// missionLevel maps depth to what the unit is. See MissionNode.Level.
//
// Step absorbs depth 3 and everything below, and that is not the repetition it
// looks like: a step's own children are inside a FOLDED step, so they are never
// read as a rung. The ladder a reader sees is four deep and each name is used
// once.
func missionLevel(depth int32) string {
	switch depth {
	case 0:
		return levelCampaign
	case 1:
		return levelMission
	case 2:
		return levelObjective
	case 3:
		return levelTask
	default:
		return levelStep
	}
}

// missionBlockers names, for each unit above the frontier, the open units of
// the frontier stage.
//
// Only the frontier: a unit in stage 4 is transitively behind stages 1-3, but
// the thing to go and look at is the one stage that is actually open. Listing
// every unit below would turn an answer into a census.
func missionBlockers(nodes []MissionNode, stages []MissionStage, rootID string) map[string][]MissionBlocker {
	out := map[string][]MissionBlocker{}

	var frontier *MissionStage
	for i := range stages {
		if stages[i].Frontier {
			frontier = &stages[i]
			break
		}
	}
	// No frontier means every stage is closed; nothing is behind anything.
	// A nil stage number is the single implicit stage of an unstaged set, which
	// has nothing below it to be blocked by.
	if frontier == nil || frontier.Stage == nil {
		return out
	}

	byID := make(map[string]*MissionNode, len(nodes))
	for i := range nodes {
		byID[nodes[i].ID] = &nodes[i]
	}

	blockers := make([]MissionBlocker, 0, len(frontier.IssueIDs))
	for _, id := range frontier.IssueIDs {
		node, ok := byID[id]
		if !ok || node.Terminal {
			continue
		}
		blockers = append(blockers, MissionBlocker{
			IssueID:    node.ID,
			Identifier: node.Identifier,
			Title:      node.Title,
			Status:     node.Status,
			Stage:      node.Stage,
			Relation:   blockerStageBarrier,
		})
	}
	if len(blockers) == 0 {
		return out
	}

	for i := range nodes {
		node := &nodes[i]
		if node.ParentID == nil || *node.ParentID != rootID {
			continue
		}
		// An unstaged child is ignored by the barrier, so it is not behind it.
		if node.Stage == nil || *node.Stage <= *frontier.Stage {
			continue
		}
		out[node.ID] = blockers
	}
	return out
}

// MissionNodeRun is the latest run of one unit. Every field is a column the
// product already writes; the view adds no state of its own.
type MissionNodeRun struct {
	TaskID        string     `json:"task_id"`
	Status        string     `json:"status"`
	FailureReason string     `json:"failure_reason,omitempty"`
	WaitReason    string     `json:"wait_reason,omitempty"`
	DispatchedAt  *time.Time `json:"dispatched_at,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
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
	Referential    string       `json:"referential,omitempty"`
	RecipientType  string       `json:"recipient_type"`
	LeadName       string       `json:"lead_name,omitempty"`
	Escalated      bool         `json:"escalated"`
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

// MissionAutonomy counts where raised hands ended up, over the whole tree and
// over every hand, not just the open ones — a measurement of one afternoon is
// noise.
//
// ReachedHuman is the number that matters. The design note is explicit that it,
// not the raw count of raised hands, is what measures autonomy: a team whose
// hands all get settled by a lead is not a team that stopped asking, it is a
// team whose referentials and leads can answer.
type MissionAutonomy struct {
	Total          int `json:"total"`
	ReachedHuman   int `json:"reached_human"`
	Escalated      int `json:"escalated"`
	SettledByLead  int `json:"settled_by_lead"`
	SettledByHuman int `json:"settled_by_human"`
	StillOpen      int `json:"still_open"`
}

// MissionLeadContest is one lead's contest record.
//
// The lead's job before escalating is to check whether the answer already
// exists in a referential it can reach. Nothing enforces that and nothing
// could — a lead that relays a question unchanged looks identical to one that
// checked and found nothing. What can be measured is the outcome, and settled
// against escalated is exactly it.
//
// Distinct from the per-referential cut in Prometheus on purpose. This one
// answers "is this lead contesting or relaying"; the metric answers "is this
// referential answerable by a lead at all". Two different failures that look
// the same in one number.
type MissionLeadContest struct {
	LeadID    string `json:"lead_id"`
	LeadName  string `json:"lead_name,omitempty"`
	Total     int    `json:"total"`
	Settled   int    `json:"settled"`
	Escalated int    `json:"escalated"`
	StillOpen int    `json:"still_open"`
}

type MissionResponse struct {
	// Product is the line this tree belongs to, not a rung above it. Absent
	// when the root carries no project tag, which the client states rather
	// than hides.
	Product *MissionProduct      `json:"product,omitempty"`
	Root    MissionNode          `json:"root"`
	Nodes   []MissionNode        `json:"nodes"`
	Stages  []MissionStage       `json:"stages"`
	Waiting []MissionWaitingUnit `json:"waiting"`
	// WithLead is the half of the raised hands that is NOT on the human. A hand
	// addressed to a squad leader is out of the primary list by construction —
	// that is the entire point of a recipient, and leaving it in would mean the
	// list still counts every interruption as yours.
	WithLead []MissionWaitingUnit `json:"with_lead"`
	// Autonomy is the measurement the recipient exists to produce.
	Autonomy MissionAutonomy `json:"autonomy"`
	// Leads is the same measurement cut per lead: who contests, who relays.
	Leads []MissionLeadContest `json:"leads"`
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
// It was "agent_id" while the grouping was a proxy for the body of knowledge a
// question interrogates. Migration 481 gave the raised hand a real
// referential_key, validated against a per-workspace catalog, so the grouping
// now counts the thing itself.
const missionReferentialField = "referential_key"

// missionReferentialStandIn records whether the grouping is still a proxy. It
// is not: the field is real and every new hand is refused without it.
const missionReferentialStandIn = false

// missionReferentialUnrecorded is the bucket for hands raised before the field
// existed.
//
// Deliberately NOT folded into the `unclassified` catalog key, which means "the
// raiser could not tell which body of knowledge failed it". "Nobody was asked"
// and "the raiser could not tell" are different facts, and the second one is a
// finding about the raiser while the first is only an artefact of when the
// column landed.
const missionReferentialUnrecorded = "unrecorded"

// missionReferential returns the referential a hand interrogates.
//
// labels maps catalog keys to display names. A key the catalog no longer has —
// archived, renamed, deleted — resolves to itself rather than disappearing:
// raised_hand.referential_key is deliberately not an FK so history survives a
// change to the vocabulary, and losing the row here would undo that.
func missionReferential(hand MissionHand, labels map[string]string) (key, label string) {
	if hand.Referential == "" {
		return missionReferentialUnrecorded, "Not recorded (raised before referentials)"
	}
	if name, ok := labels[hand.Referential]; ok && name != "" {
		return hand.Referential, name
	}
	return hand.Referential, hand.Referential
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

	// A sixth query, and only when there is something to label. A mission with
	// no open hand — the common case — still costs five. The budget that
	// matters is "fixed, never per node", not the exact number.
	referentialLabels := map[string]string{}
	if len(hands) > 0 {
		catalog, cerr := h.Queries.ListReferentials(r.Context(), db.ListReferentialsParams{
			WorkspaceID:     root.WorkspaceID,
			IncludeArchived: true,
		})
		if cerr != nil {
			writeError(w, http.StatusInternalServerError, "failed to load referentials")
			return
		}
		for _, entry := range catalog {
			referentialLabels[entry.Key] = entry.Name
		}
	}

	// The autonomy counters, over EVERY hand in the tree rather than the open
	// ones: a measurement of one afternoon is noise.
	counts, cerr := h.Queries.CountMissionHands(r.Context(), issueIDs)
	if cerr != nil {
		writeError(w, http.StatusInternalServerError, "failed to count raised hands")
		return
	}

	leadRows, lerr := h.Queries.ListMissionLeadContest(r.Context(), issueIDs)
	if lerr != nil {
		writeError(w, http.StatusInternalServerError, "failed to load lead contest counts")
		return
	}
	leads := make([]MissionLeadContest, 0, len(leadRows))
	for _, row := range leadRows {
		entry := MissionLeadContest{
			LeadID:    uuidToString(row.LeadID),
			Total:     int(row.Total),
			Settled:   int(row.Settled),
			Escalated: int(row.Escalated),
			StillOpen: int(row.StillOpen),
		}
		if row.LeadName.Valid {
			entry.LeadName = row.LeadName.String
		}
		leads = append(leads, entry)
	}

	// The rung is the unit's TRUE depth, not its depth from the root you opened.
	// Open the view on a mission and without this its own root would be numbered
	// 0 and labelled a campaign.
	rootDepth, err := h.Queries.CountIssueAncestors(r.Context(), db.CountIssueAncestorsParams{
		IssueID:  root.ID,
		MaxDepth: int32(missionMaxDepth),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to place the mission in its tree")
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
		if row.ReferentialKey.Valid {
			hand.Referential = row.ReferentialKey.String
		}
		hand.RecipientType = row.RecipientType
		if row.LeadName.Valid {
			hand.LeadName = row.LeadName.String
		}
		hand.Escalated = row.EscalatedAt.Valid
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
			Level:          missionLevel(rootDepth + row.Depth),
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
		if task, ok := tasksByIssue[id]; ok {
			node.LastRun = missionNodeRun(task)
		}
		// Same rule the waiting list applies: the transition row is the only
		// honest answer, and updated_at is an admitted fallback.
		if change, ok := changeByIssue[id]; ok && !change.at.IsZero() && change.to == row.Status {
			at := change.at
			node.StatusSince = &at
			node.StatusSinceExact = true
		} else {
			at := row.UpdatedAt.Time.UTC()
			node.StatusSince = &at
			node.StatusSinceExact = false
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

	// Split by recipient. A hand with a lead is not on the human, so it leaves
	// the primary list entirely — that is what having a recipient BUYS, and
	// leaving it in would mean the list still counts every interruption as
	// yours.
	withLead := make([]MissionWaitingUnit, 0)
	onHuman := make([]MissionWaitingUnit, 0, len(waiting))
	for _, unit := range waiting {
		if unit.Hand != nil && unit.Hand.RecipientType == recipientLead {
			withLead = append(withLead, unit)
			continue
		}
		onHuman = append(onHuman, unit)
	}
	waiting = onHuman
	sort.SliceStable(withLead, func(i, j int) bool {
		return withLead[i].WaitedSecs > withLead[j].WaitedSecs
	})

	// Longest wait first. Deliberately not by priority: the question this list
	// answers is "what has been sitting on me", and a low-priority unit parked
	// for six days is more interesting than a high-priority one parked for an
	// hour. Priority is on the board, which is one click away.
	sort.SliceStable(waiting, func(i, j int) bool {
		return waiting[i].WaitedSecs > waiting[j].WaitedSecs
	})

	// The product line. One row, read off the root. pgx returns ErrNoRows when
	// the root carries no project tag, which is not an error: it is the answer.
	var product *MissionProduct
	if row, err := h.Queries.GetMissionCampaign(r.Context(), root.ID); err == nil {
		product = &MissionProduct{
			ID:     uuidToString(row.ID),
			Title:  row.Title,
			Status: row.Status,
		}
		if row.Icon.Valid {
			product.Icon = row.Icon.String
		}
	}

	stages, unstagedIgnored := missionStages(nodes, uuidToString(root.ID))
	for id, blockers := range missionBlockers(nodes, stages, uuidToString(root.ID)) {
		if i, ok := nodeIndex[id]; ok {
			nodes[i].BlockedBy = blockers
		}
	}

	// Declared dependencies, on top of the barrier. They come second because
	// they are the ones that can point outside this tree, and a reader scanning
	// the list should meet the local ordering before the remote wait.
	deps, err := h.Queries.ListMissionDependencies(r.Context(), issueIDs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dependencies")
		return
	}
	for _, dep := range deps {
		i, ok := nodeIndex[uuidToString(dep.BlockedIssueID)]
		if !ok {
			continue
		}
		blockerID := uuidToString(dep.BlockerIssueID)
		// A blocker that is already in this tree is not "outside", even though
		// it arrived through the dependency table — outside is about where the
		// unit IS, not about which mechanism named it.
		_, inTree := nodeIndex[blockerID]
		blocker := MissionBlocker{
			IssueID:    blockerID,
			Identifier: prefix + "-" + strconv.Itoa(int(dep.BlockerNumber)),
			Title:      dep.BlockerTitle,
			Status:     dep.BlockerStatus,
			Relation:   blockerDependency,
			Outside:    !inTree,
		}
		if dep.BlockerAssigneeType.Valid {
			at := dep.BlockerAssigneeType.String
			blocker.AssigneeType = &at
		}
		if dep.BlockerAssigneeID.Valid {
			aid := uuidToString(dep.BlockerAssigneeID)
			blocker.AssigneeID = &aid
		}
		nodes[i].BlockedBy = append(nodes[i].BlockedBy, blocker)
	}

	// Stalled barriers, at EVERY level — a branch three deep whose stage 2 was
	// never promoted stops that branch just as dead as one at the root.
	// Both lists, not just `waiting`. A hand addressed to a lead left `waiting`
	// in the split above, and excluding only `waiting` here reported the same
	// issue twice — once as with-a-lead and once as stalled. A unit that
	// appears in two lists at once is a list nobody trusts.
	waitingIDs := make(map[string]struct{}, len(waiting)+len(withLead))
	for _, unit := range waiting {
		waitingIDs[unit.IssueID] = struct{}{}
	}
	for _, unit := range withLead {
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
	markWaitingBelow(nodes, nodeIndex, append(append(append([]MissionWaitingUnit{}, waiting...), withLead...), stalled...))

	resp := MissionResponse{
		Product:  product,
		Root:     nodes[0],
		Nodes:    nodes,
		Stages:   stages,
		Waiting:  waiting,
		WithLead: withLead,
		Autonomy: MissionAutonomy{
			Total:          int(counts.Total),
			ReachedHuman:   int(counts.ReachedHuman),
			Escalated:      int(counts.Escalated),
			SettledByLead:  int(counts.SettledByLead),
			SettledByHuman: int(counts.SettledByHuman),
			StillOpen:      int(counts.StillOpen),
		},
		Leads:              leads,
		Stalled:            stalled,
		Referentials:       missionReferentials(allHands, referentialLabels),
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

// missionNodeRun narrows the task row to the fields a reader needs. The row
// carries more; the view ships what it will show and nothing else, the same
// call ListMissionTree makes about description and properties.
func missionNodeRun(task db.ListMissionLatestTasksRow) *MissionNodeRun {
	run := &MissionNodeRun{
		TaskID:    uuidToString(task.TaskID),
		Status:    task.Status,
		CreatedAt: task.CreatedAt.Time.UTC(),
	}
	if task.FailureReason.Valid {
		run.FailureReason = task.FailureReason.String
	}
	if task.WaitReason.Valid {
		run.WaitReason = task.WaitReason.String
	}
	if task.DispatchedAt.Valid {
		at := task.DispatchedAt.Time.UTC()
		run.DispatchedAt = &at
	}
	if task.StartedAt.Valid {
		at := task.StartedAt.Time.UTC()
		run.StartedAt = &at
	}
	if task.CompletedAt.Valid {
		at := task.CompletedAt.Time.UTC()
		run.CompletedAt = &at
	}
	return run
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
func missionReferentials(hands []MissionHand, labels map[string]string) []MissionReferential {
	byKey := map[string]*MissionReferential{}
	order := make([]string, 0)
	for _, hand := range hands {
		key, label := missionReferential(hand, labels)
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

// MissionProduct is the product line this tree belongs to. Multica calls it a
// project.
//
// NOT a rung. project_id is a flat per-issue tag that is not inherited, so it
// groups ACROSS the tree rather than sitting above it — which is exactly right
// for a product: every issue in Veezeet carries it, at any depth, as a filter.
// The ladder is parentage, the one relation the product enforces.
type MissionProduct struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Icon   string `json:"icon,omitempty"`
	Status string `json:"status"`
}

// ── The leveled board ───────────────────────────────────────────────────────

// MissionBoardLevel is one rung, and how much is sitting on it.
type MissionBoardLevel struct {
	Level string `json:"level"`
	Total int    `json:"total"`
	// Orphans are units whose DECLARED rung disagrees with their parentage —
	// an objective with no mission over it, say. They are counted per rung
	// rather than once for the workspace, because "three orphan objectives" is
	// a different problem from "three orphan tasks".
	Orphans int `json:"orphans"`
}

// MissionBoardRef names a unit in one line, for a row to point at.
type MissionBoardRef struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
}

// MissionBoardRow is one unit on the board, at whatever rung is being shown.
type MissionBoardRow struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Level      string `json:"level"`
	Depth      int32  `json:"depth"`

	// Orphan is the disagreement itself: this unit says what it is, and its
	// parentage says otherwise. Not an error — a thing to go and look at.
	Orphan bool `json:"orphan"`
	// Declared is false when the rung came from depth rather than from anyone
	// saying so. An undeclared unit can never be an orphan, which is what keeps
	// the signal meaningful on a workspace full of existing issues.
	Declared bool `json:"declared"`

	Parent   *MissionBoardRef `json:"parent,omitempty"`
	Campaign MissionBoardRef  `json:"campaign"`

	Units     int `json:"units"`
	Done      int `json:"done"`
	OpenHands int `json:"open_hands"`

	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

type MissionBoardResponse struct {
	// Levels always carries every rung, including the empty ones, so the
	// board's tabs do not appear and disappear as work moves.
	Levels   []MissionBoardLevel `json:"levels"`
	Level    string              `json:"level"`
	Rows     []MissionBoardRow   `json:"rows"`
	MaxDepth int                 `json:"max_depth"`
}

// missionRungs is the ladder in order. Also the allow-list for ?level=.
var missionRungs = []string{levelCampaign, levelMission, levelObjective, levelTask, levelStep}

// ListMissions is the leveled board: everything at one rung, plus how much sits
// on every other rung.
//
// Three queries regardless of the size of the workspace — the counts, the rows,
// and one rollup over all the rows at once. Same rule as the detail view: a
// board that fans out per row is the mistake one level up.
func (h *Handler) ListMissions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	// Campaign by default: it is the rung you start from when you do not yet
	// know which mission you are looking for.
	level := levelCampaign
	if raw := r.URL.Query().Get("level"); raw != "" {
		if !slices.Contains(missionRungs, raw) {
			writeError(w, http.StatusBadRequest, "unknown level")
			return
		}
		level = raw
	}
	orphansOnly := r.URL.Query().Get("orphans") == "1"

	var campaignFilter pgtype.UUID
	if raw := r.URL.Query().Get("campaign"); raw != "" {
		parsed, ok := parseUUIDOrBadRequest(w, raw, "campaign")
		if !ok {
			return
		}
		campaignFilter = parsed
	}

	counts, err := h.Queries.CountIssuesByLevel(r.Context(), db.CountIssuesByLevelParams{
		WorkspaceID: wsUUID,
		MaxDepth:    int32(missionBoardDepth),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to count levels")
		return
	}
	byLevel := make(map[string]db.CountIssuesByLevelRow, len(counts))
	for _, row := range counts {
		byLevel[row.Level] = row
	}
	levels := make([]MissionBoardLevel, 0, len(missionRungs))
	for _, rung := range missionRungs {
		entry := MissionBoardLevel{Level: rung}
		if row, ok := byLevel[rung]; ok {
			entry.Total = int(row.Total)
			entry.Orphans = int(row.Orphans)
		}
		levels = append(levels, entry)
	}

	issues, err := h.Queries.ListIssuesAtLevel(r.Context(), db.ListIssuesAtLevelParams{
		WorkspaceID: wsUUID,
		MaxDepth:    int32(missionBoardDepth),
		Level:       level,
		OrphansOnly: orphansOnly,
		CampaignID:  campaignFilter,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list the board")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	resolver := issuestatus.NewResolver(wsUUID)
	identifier := func(number int32) string { return prefix + "-" + strconv.Itoa(int(number)) }

	rollups := map[string]*missionRollup{}
	if len(issues) > 0 {
		ids := make([]pgtype.UUID, 0, len(issues))
		for _, row := range issues {
			ids = append(ids, row.ID)
		}
		rows, err := h.Queries.ListMissionRollups(r.Context(), db.ListMissionRollupsParams{
			MissionIds: ids,
			MaxDepth:   int32(missionBoardDepth),
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to roll up the board")
			return
		}
		for _, row := range rows {
			id := uuidToString(row.MissionID)
			entry, ok := rollups[id]
			if !ok {
				entry = &missionRollup{}
				rollups[id] = entry
			}
			entry.units += int(row.Units)
			entry.hands += int(row.OpenHands)
			if isTerminalChildStatus(resolver.Effective(r.Context(), h.Queries, row.Status)) {
				entry.done += int(row.Units)
			}
		}
	}

	out := make([]MissionBoardRow, 0, len(issues))
	for _, row := range issues {
		id := uuidToString(row.ID)
		entry := MissionBoardRow{
			ID:         id,
			Identifier: identifier(row.Number),
			Title:      row.Title,
			Status:     row.Status,
			Level:      row.Level,
			Depth:      row.Depth,
			// pgtype.Bool: the CASE can produce NULL for a row whose declared
			// level is NULL, which is "not an orphan" rather than "unknown".
			Orphan:   row.Orphan.Valid && row.Orphan.Bool,
			Declared: row.DeclaredLevel.Valid,
			Campaign: MissionBoardRef{
				ID:         uuidToString(row.CampaignID),
				Identifier: identifier(row.CampaignNumber),
				Title:      row.CampaignTitle,
			},
			UpdatedAt: row.UpdatedAt.Time.UTC(),
		}
		if row.ParentNumber.Valid {
			entry.Parent = &MissionBoardRef{
				Identifier: identifier(row.ParentNumber.Int32),
				Title:      row.ParentTitle.String,
			}
		}
		if rollup, ok := rollups[id]; ok {
			entry.Units = rollup.units
			entry.Done = rollup.done
			entry.OpenHands = rollup.hands
		}
		if row.LastActivityAt.Valid {
			at := row.LastActivityAt.Time.UTC()
			entry.LastActivityAt = &at
		}
		out = append(out, entry)
	}

	writeJSON(w, http.StatusOK, MissionBoardResponse{
		Levels:   levels,
		Level:    level,
		Rows:     out,
		MaxDepth: missionBoardDepth,
	})
}

type missionRollup struct {
	units, done, hands int
}

// SetIssueLevel declares what a unit is meant to be.
//
// The whole write surface for the ladder, and the only way an orphan comes into
// existence: undeclared units take their rung from depth and so can never
// disagree with their own parentage. Sending null clears the declaration and
// hands the unit back to depth.
func (h *Handler) SetIssueLevel(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		Level *string `json:"level"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	var level pgtype.Text
	if req.Level != nil {
		if !slices.Contains(missionRungs, *req.Level) {
			writeError(w, http.StatusBadRequest, "unknown level: "+strings.Join(missionRungs, ", "))
			return
		}
		level = pgtype.Text{String: *req.Level, Valid: true}
	}

	// No reparenting, no cascade to children, no healing of a disagreement.
	// That restraint IS the feature: a declared rung that stops matching its
	// parentage is the report, and rewriting it would delete the finding.
	updated, err := h.Queries.SetIssueLevel(r.Context(), db.SetIssueLevelParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Level:       level,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set level")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":    uuidToString(updated.ID),
		"level": req.Level,
	})
}

// SetIssueDependency declares or clears "this unit waits on that one".
//
// The write path issue_dependency never had. It is the only way to say that a
// unit waits on something the stage barrier cannot reach — another mission,
// another campaign, another squad's work — which is the ordinary case the
// moment two teams share a release.
//
// POST adds, DELETE removes. Both are idempotent: adding twice writes one row,
// removing something that is not there is not an error.
func (h *Handler) SetIssueDependency(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		DependsOn string `json:"depends_on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	// Resolved through the same loader as the issue itself, so a caller cannot
	// name a unit it is not allowed to see and learn that it exists.
	blocker, ok := h.loadIssueForUser(w, r, req.DependsOn)
	if !ok {
		return
	}

	if r.Method == http.MethodDelete {
		if err := h.Queries.RemoveIssueDependency(r.Context(), db.RemoveIssueDependencyParams{
			IssueID:          issue.ID,
			DependsOnIssueID: blocker.ID,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to remove dependency")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"removed": true})
		return
	}

	// No cycle check. Deliberate, and worth stating: a cycle here is two units
	// each waiting on the other, which is a real thing a team does to itself
	// and a thing the view should SHOW rather than refuse to record. Refusing
	// the write would only move the deadlock somewhere nothing can see it.
	if _, err := h.Queries.AddIssueDependency(r.Context(), db.AddIssueDependencyParams{
		IssueID:          issue.ID,
		DependsOnIssueID: blocker.ID,
	}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		// ErrNoRows is the guard clause firing — same pair, or a unit naming
		// itself. Both are no-ops, not failures.
		writeError(w, http.StatusInternalServerError, "failed to add dependency")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"issue_id":   uuidToString(issue.ID),
		"depends_on": uuidToString(blocker.ID),
	})
}

// ListIssueDependencies is what one unit is waiting on.
//
// The mission payload already carries this for a whole tree; this is the same
// rows for a single issue, so the issue page can show and edit them without
// loading a mission it may not be part of.
func (h *Handler) ListIssueDependencies(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	rows, err := h.Queries.ListMissionDependencies(r.Context(), []pgtype.UUID{issue.ID})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load dependencies")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), issue.WorkspaceID)
	out := make([]MissionBlocker, 0, len(rows))
	for _, row := range rows {
		blocker := MissionBlocker{
			IssueID:    uuidToString(row.BlockerIssueID),
			Identifier: prefix + "-" + strconv.Itoa(int(row.BlockerNumber)),
			Title:      row.BlockerTitle,
			Status:     row.BlockerStatus,
			Relation:   blockerDependency,
		}
		if row.BlockerAssigneeType.Valid {
			at := row.BlockerAssigneeType.String
			blocker.AssigneeType = &at
		}
		if row.BlockerAssigneeID.Valid {
			aid := uuidToString(row.BlockerAssigneeID)
			blocker.AssigneeID = &aid
		}
		out = append(out, blocker)
	}

	// `outside` is deliberately absent here. It means "not in the tree you are
	// looking at", and from one issue there is no tree to be outside of —
	// sending it would be answering a question nobody asked.
	writeJSON(w, http.StatusOK, map[string]any{"blocked_by": out})
}

// applyLevelPolicy overrides a claimed task's model, thinking level and service
// tier with whatever the issue's RUNG says it should run on.
//
// One persona, one identity, and the level of the work picks how it runs —
// instead of copying an agent per configuration and splitting its raised hands,
// its contest ratio and its autonomy numbers across the copies.
//
// Costs nothing on a workspace that has set no policy: one small indexed read
// that comes back empty, and the function returns before it looks at anything
// else. The ancestor walk only happens when a policy exists AND the issue has
// not declared its own rung.
//
// The agent's own settings are the fallback, not the winner. That is the point:
// "put it at the level" means the level decides, and an agent's model becomes
// what runs when its rung has no opinion.
func (h *Handler) applyLevelPolicy(ctx context.Context, agentData *TaskAgentData, issue db.Issue) {
	if agentData == nil {
		return
	}
	policies, err := h.Queries.ListLevelPolicies(ctx, issue.WorkspaceID)
	if err != nil || len(policies) == 0 {
		// A failed lookup runs the task on the agent's settings rather than
		// refusing the claim. A policy is a preference about cost; it is not
		// worth stranding work over.
		return
	}

	level := issue.Level.String
	if !issue.Level.Valid || level == "" {
		// Undeclared: the rung comes from depth, which needs the walk up. Only
		// reached when the workspace actually has policies.
		depth, err := h.Queries.CountIssueAncestors(ctx, db.CountIssueAncestorsParams{
			IssueID:  issue.ID,
			MaxDepth: int32(missionBoardDepth),
		})
		if err != nil {
			return
		}
		level = missionLevel(depth)
	}

	for _, policy := range policies {
		if policy.Level != level {
			continue
		}
		// Each field independently: a policy that names only a model must not
		// silently clear the agent's thinking level.
		if policy.Model.Valid {
			agentData.Model = policy.Model.String
		}
		if policy.ThinkingLevel.Valid {
			agentData.ThinkingLevel = policy.ThinkingLevel.String
		}
		if policy.ServiceTier.Valid {
			agentData.ServiceTier = policy.ServiceTier.String
		}
		return
	}
}

// LevelPolicyEntry is what one rung runs on. A nil field means the rung has no
// opinion and the agent's own setting stands.
type LevelPolicyEntry struct {
	Level         string  `json:"level"`
	Model         *string `json:"model,omitempty"`
	ThinkingLevel *string `json:"thinking_level,omitempty"`
	ServiceTier   *string `json:"service_tier,omitempty"`
}

// ListLevelPolicies returns every rung, including the ones with no policy, so a
// reader can see the whole ladder rather than only the rungs someone has
// already opinionated.
func (h *Handler) ListLevelPolicies(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.ListLevelPolicies(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list level policies")
		return
	}
	byLevel := make(map[string]db.ListLevelPoliciesRow, len(rows))
	for _, row := range rows {
		byLevel[row.Level] = row
	}
	out := make([]LevelPolicyEntry, 0, len(missionRungs))
	for _, rung := range missionRungs {
		entry := LevelPolicyEntry{Level: rung}
		if row, ok := byLevel[rung]; ok {
			entry.Model = textToPtr(row.Model)
			entry.ThinkingLevel = textToPtr(row.ThinkingLevel)
			entry.ServiceTier = textToPtr(row.ServiceTier)
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"policies": out})
}

// SetLevelPolicy replaces one rung's policy outright.
//
// A full replace, not a merge: a merge cannot express "stop overriding the
// model but keep overriding thinking", and that is a thing somebody will want
// on the first day they use this.
func (h *Handler) SetLevelPolicy(w http.ResponseWriter, r *http.Request) {
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
		Model         *string `json:"model"`
		ThinkingLevel *string `json:"thinking_level"`
		ServiceTier   *string `json:"service_tier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	// All three empty is a delete, not a row that sets nothing — the table's
	// CHECK would refuse it anyway, and refusing "clear this rung" as a
	// malformed request would be the wrong answer to a reasonable thing to ask.
	if isBlank(req.Model) && isBlank(req.ThinkingLevel) && isBlank(req.ServiceTier) {
		if err := h.Queries.DeleteLevelPolicy(r.Context(), db.DeleteLevelPolicyParams{
			WorkspaceID: wsUUID,
			Level:       level,
		}); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to clear level policy")
			return
		}
		writeJSON(w, http.StatusOK, LevelPolicyEntry{Level: level})
		return
	}

	// NOT validated against a model catalog here. The catalog is per runtime and
	// per provider, and a task's runtime is not known until it is claimed — so
	// the only place the answer exists is the daemon, which already validates
	// and degrades rather than failing. Rejecting here would mean guessing with
	// less information than the thing that checks it properly.
	row, err := h.Queries.UpsertLevelPolicy(r.Context(), db.UpsertLevelPolicyParams{
		WorkspaceID:   wsUUID,
		Level:         level,
		Model:         nargText(req.Model),
		ThinkingLevel: nargText(req.ThinkingLevel),
		ServiceTier:   nargText(req.ServiceTier),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set level policy")
		return
	}
	writeJSON(w, http.StatusOK, LevelPolicyEntry{
		Level:         row.Level,
		Model:         textToPtr(row.Model),
		ThinkingLevel: textToPtr(row.ThinkingLevel),
		ServiceTier:   textToPtr(row.ServiceTier),
	})
}

func isBlank(value *string) bool {
	return value == nil || strings.TrimSpace(*value) == ""
}

func nargText(value *string) pgtype.Text {
	if isBlank(value) {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(*value), Valid: true}
}
