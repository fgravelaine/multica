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

	// Level is what this unit IS.
	//
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
	//   campaign/mission   REAL. Different tables — project and issue.
	//   mission/objective  SHAPE ONLY. A root is parent_issue_id IS NULL, so it
	//                      has no parent to wake. Nothing else differs.
	//   objective/task     NOTHING. No code anywhere keys on depth.
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
	// Stage is the blocker's own stage, so a reader can see the ordering that
	// produced this rather than taking the claim on faith.
	Stage *int32 `json:"stage,omitempty"`
	// Relation is why it blocks. One value today — the barrier is the only
	// blocking relation the product records — and a field rather than an
	// assumption, so a real dependency link could join it without a migration
	// on the wire format.
	Relation string `json:"relation"`
}

const blockerStageBarrier = "stage_barrier"

const (
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
		return levelMission
	case 1:
		return levelObjective
	case 2:
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
	// Campaign is the rung above the mission. Absent when the mission belongs
	// to none, which the client states rather than hides.
	Campaign *MissionCampaign     `json:"campaign,omitempty"`
	Root     MissionNode          `json:"root"`
	Nodes    []MissionNode        `json:"nodes"`
	Stages   []MissionStage       `json:"stages"`
	Waiting  []MissionWaitingUnit `json:"waiting"`
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
			Level:          missionLevel(row.Depth),
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

	// The campaign. One row, on the root only — an objective and a task belong
	// to their mission, not directly to a campaign. pgx returns ErrNoRows when
	// the mission is unattached, which is not an error: it is the answer.
	var campaign *MissionCampaign
	if row, err := h.Queries.GetMissionCampaign(r.Context(), root.ID); err == nil {
		campaign = &MissionCampaign{
			ID:     uuidToString(row.ID),
			Title:  row.Title,
			Status: row.Status,
		}
		if row.Icon.Valid {
			campaign.Icon = row.Icon.String
		}
	}

	stages, unstagedIgnored := missionStages(nodes, uuidToString(root.ID))
	for id, blockers := range missionBlockers(nodes, stages, uuidToString(root.ID)) {
		if i, ok := nodeIndex[id]; ok {
			nodes[i].BlockedBy = blockers
		}
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
		Campaign: campaign,
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

// ── The index ───────────────────────────────────────────────────────────────

// MissionSummary is one mission in the list.
//
// Nothing here is stored. A mission is a top-level issue that has children —
// the product has no mission entity and this view does not invent one, which is
// also why the list cannot be filtered, sorted or saved: it is a derivation,
// not a place to put things.
type MissionSummary struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"`
	Number     int32  `json:"number"`
	Title      string `json:"title"`
	Status     string `json:"status"`

	// Units is everything below the root, to the same depth bound the detail
	// view uses — so the two never disagree about how big a mission is.
	Units    int `json:"units"`
	Done     int `json:"done"`
	OpenHand int `json:"open_hands"`

	LastActivityAt *time.Time `json:"last_activity_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// MissionCampaign is the campaign a mission belongs to.
//
// Multica calls it a project. The naming ladder is Campaign → Mission →
// Objective → Task, and only the last three are depths in the issue tree — the
// top rung is an entity that already exists, which is why nothing had to be
// invented for it.
type MissionCampaign struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Icon   string `json:"icon,omitempty"`
	Status string `json:"status"`
}

type MissionListResponse struct {
	Missions []MissionSummary `json:"missions"`
	MaxDepth int              `json:"max_depth"`
}

// ListMissions is the index: every mission in the workspace, with enough to
// choose one.
//
// Two queries regardless of how many missions there are — the roots, then one
// recursive rollup over all of them at once. A list that fans out per row is
// the same mistake as a view that fans out per node, one level up.
func (h *Handler) ListMissions(w http.ResponseWriter, r *http.Request) {
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace_id")
	if !ok {
		return
	}

	roots, err := h.Queries.ListMissionRoots(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list missions")
		return
	}
	if len(roots) == 0 {
		writeJSON(w, http.StatusOK, MissionListResponse{
			Missions: []MissionSummary{},
			MaxDepth: missionMaxDepth,
		})
		return
	}

	missionIDs := make([]pgtype.UUID, 0, len(roots))
	for _, root := range roots {
		missionIDs = append(missionIDs, root.ID)
	}

	rollups, err := h.Queries.ListMissionRollups(r.Context(), db.ListMissionRollupsParams{
		MissionIds: missionIDs,
		MaxDepth:   int32(missionMaxDepth),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to roll up missions")
		return
	}

	prefix := h.getIssuePrefix(r.Context(), wsUUID)
	// One resolver for the whole list: the catalog read is amortised across
	// every mission and every status in it, which is the point of the type.
	resolver := issuestatus.NewResolver(wsUUID)

	type rollup struct {
		units, done, hands int
	}
	byMission := make(map[string]*rollup, len(roots))
	for _, row := range rollups {
		id := uuidToString(row.MissionID)
		entry, ok := byMission[id]
		if !ok {
			entry = &rollup{}
			byMission[id] = entry
		}
		entry.units += int(row.Units)
		entry.hands += int(row.OpenHands)
		// Terminal is the workspace's rule, not a string comparison — the same
		// resolver the detail view uses, so "done" means one thing in both.
		if isTerminalChildStatus(resolver.Effective(r.Context(), h.Queries, row.Status)) {
			entry.done += int(row.Units)
		}
	}

	missions := make([]MissionSummary, 0, len(roots))
	for _, root := range roots {
		id := uuidToString(root.ID)
		summary := MissionSummary{
			ID:         id,
			Identifier: prefix + "-" + strconv.Itoa(int(root.Number)),
			Number:     root.Number,
			Title:      root.Title,
			Status:     root.Status,
			UpdatedAt:  root.UpdatedAt.Time.UTC(),
		}
		if entry, ok := byMission[id]; ok {
			summary.Units = entry.units
			summary.Done = entry.done
			summary.OpenHand = entry.hands
		}
		if root.LastActivityAt.Valid {
			at := root.LastActivityAt.Time.UTC()
			summary.LastActivityAt = &at
		}
		missions = append(missions, summary)
	}

	writeJSON(w, http.StatusOK, MissionListResponse{
		Missions: missions,
		MaxDepth: missionMaxDepth,
	})
}
