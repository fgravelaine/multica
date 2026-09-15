// SPIKE (not upstream): the raised hand.
//
// A unit stops and needs a decision it cannot take. Today that is a free-text
// comment. Here it is an object with bounded options, the cost of being wrong
// on each side, and a recommendation — and answering it does something: it
// writes the decision and returns the unit to the board.
//
// See migrations/480_raised_hand.up.sql for the model and what it leaves out.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/issuestatus"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// handOption is one bounded answer. `cost` is what it costs to be WRONG on this
// side, not what the option does — that is the field which lets someone who
// does not know the domain decide in thirty seconds, and it is required.
type handOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cost  string `json:"cost"`
}

const (
	maxHandOptions = 6
	minHandOptions = 2
)

// parkedStatus is where a raised hand leaves its issue.
//
// `blocked` is the semantically correct status and is NOT used, for a reason
// worth recording: `service.IssueTriggerInput` only re-enqueues a run on a
// status change whose PREVIOUS status was `backlog` (issue_trigger.go, the
// RunSourceStatus case). Parking in `blocked` would strand the unit — the
// answer would move it to `todo` and nothing would pick it up.
//
// So the product has exactly one parking status, and it is the one its
// promotion rule is keyed to. Multica's own words for it are "backlog acts as a
// parking lot where issues can be pre-assigned without immediately triggering
// execution" (shouldEnqueueAgentTask). That is the same concept the design note
// calls *parking*, so this reuses it rather than inventing a second one — at the
// cost of conflating "never started" with "stopped mid-flight for a decision".
// The raised hand object itself is what tells those two apart.
const parkedStatus = issuestatus.Backlog

// resumeStatus is where answering puts the unit back. backlog → todo is the
// transition the product already re-triggers on, so the resume is not written
// here; it is WillEnqueueRun doing what it already does.
const resumeStatus = issuestatus.Todo

// RaiseHand records a raised hand and parks its issue.
//
// Called by an agent from inside a run (the daemon puts MULTICA_TOKEN and
// MULTICA_TASK_ID in its environment), which is why it accepts an agent-task
// token as well as a user token.
func (h *Handler) RaiseHand(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		Question       string       `json:"question"`
		Options        []handOption `json:"options"`
		Recommendation string       `json:"recommendation"`
		Material       string       `json:"material"`
		TaskID         string       `json:"task_id"`
		// Referential is the body of knowledge that failed to answer. Required,
		// for the same reason every option needs a cost: the mission view's
		// diagnostic is a COUNT, and a hand that does not say what it
		// interrogates contributes nothing to it. `unclassified` is a real key
		// for a raiser that genuinely cannot tell — which is a finding rather
		// than a gap, and countable as such.
		Referential string `json:"referential"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.Question) == "" {
		writeError(w, http.StatusBadRequest, "question is required")
		return
	}
	if len(req.Options) < minHandOptions || len(req.Options) > maxHandOptions {
		writeError(w, http.StatusBadRequest,
			fmt.Sprintf("between %d and %d options are required — a raised hand with one option is not a question", minHandOptions, maxHandOptions))
		return
	}
	seen := make(map[string]struct{}, len(req.Options))
	for i, opt := range req.Options {
		if strings.TrimSpace(opt.Key) == "" || strings.TrimSpace(opt.Label) == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("option %d needs a key and a label", i+1))
			return
		}
		// The cost is the point of the object. Without it this is a poll, and
		// the decider is back to needing the domain knowledge the raiser has.
		if strings.TrimSpace(opt.Cost) == "" {
			writeError(w, http.StatusBadRequest,
				fmt.Sprintf("option %q needs a cost: what it costs to be wrong on this side", opt.Key))
			return
		}
		if _, dup := seen[opt.Key]; dup {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("duplicate option key %q", opt.Key))
			return
		}
		seen[opt.Key] = struct{}{}
	}
	if req.Recommendation != "" {
		if _, ok := seen[req.Recommendation]; !ok {
			writeError(w, http.StatusBadRequest, "recommendation must be one of the option keys")
			return
		}
	}

	// The catalog is seeded lazily, exactly like issue_status: a workspace that
	// has never raised a hand gets its built-ins on the first attempt rather
	// than needing a provisioning step.
	if err := h.Queries.SeedReferentials(r.Context(), issue.WorkspaceID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to seed referentials")
		return
	}
	referential := strings.TrimSpace(req.Referential)
	if referential == "" {
		writeError(w, http.StatusBadRequest,
			"referential is required: which body of knowledge failed to answer this. Use `multica referential list` to see the keys, or `unclassified` if you cannot tell")
		return
	}
	if _, rerr := h.Queries.GetReferentialByKey(r.Context(), db.GetReferentialByKeyParams{
		WorkspaceID: issue.WorkspaceID,
		Key:         referential,
	}); rerr != nil {
		// Free text here would make the count meaningless — "design system",
		// "design-system" and "Design System" would be three referentials.
		writeError(w, http.StatusBadRequest,
			"unknown referential "+strconv.Quote(referential)+" — run `multica referential list` for the keys in this workspace")
		return
	}

	optionsJSON, err := json.Marshal(req.Options)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encode options")
		return
	}

	var taskID pgtype.UUID
	if id, perr := util.ParseUUID(req.TaskID); perr == nil {
		taskID = id
	}

	hand, err := h.Queries.RaiseHand(r.Context(), db.RaiseHandParams{
		WorkspaceID:    issue.WorkspaceID,
		IssueID:        issue.ID,
		AgentID:        issue.AssigneeID,
		TaskID:         taskID,
		Question:       strings.TrimSpace(req.Question),
		Options:        optionsJSON,
		Recommendation: pgtype.Text{String: req.Recommendation, Valid: req.Recommendation != ""},
		Material:       pgtype.Text{String: req.Material, Valid: req.Material != ""},
		ReferentialKey: pgtype.Text{String: referential, Valid: true},
	})
	if err != nil {
		// The partial unique index on (issue_id) WHERE status = 'open' is what
		// makes a second hand on a stopped unit a 409 rather than a silent
		// second question nobody will answer.
		writeError(w, http.StatusConflict, "this issue already has an open raised hand")
		return
	}

	// Park the unit. One raised hand stops ONE unit — nothing here touches any
	// sibling issue, which is the design note's "les quatre autres tickets
	// continuent" and costs nothing to honour because no such coupling exists.
	if _, err := h.Queries.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      parkedStatus,
		WorkspaceID: issue.WorkspaceID,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "raised hand recorded but the issue could not be parked")
		return
	}

	writeMeasuredJSON(w, http.StatusCreated, renderHand(hand))
}

// ListHands returns every hand on an issue, newest first.
func (h *Handler) ListHands(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}
	hands, err := h.Queries.ListHandsForIssue(r.Context(), issue.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list raised hands")
		return
	}
	out := make([]map[string]any, 0, len(hands))
	for _, hand := range hands {
		out = append(out, renderHand(hand))
	}
	writeMeasuredJSON(w, http.StatusOK, map[string]any{"hands": out})
}

// AnswerHand applies a decision and returns the unit to the board.
//
// Three things happen and the order matters: the hand is answered under a CAS
// so two deciders cannot both win, the decision is written where the agent will
// read it, and only then is the unit promoted — because promotion is what
// re-triggers the run, and a run that starts before the answer is readable has
// nothing to resume on.
func (h *Handler) AnswerHand(w http.ResponseWriter, r *http.Request) {
	userID, ok := requireUserID(w, r)
	if !ok {
		return
	}
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		ChosenOption string `json:"chosen_option"`
		Answer       string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.ChosenOption) == "" {
		writeError(w, http.StatusBadRequest, "chosen_option is required")
		return
	}

	hand, err := h.Queries.GetOpenHandForIssue(r.Context(), issue.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusNotFound, "no open raised hand on this issue")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load raised hand")
		return
	}

	var options []handOption
	if err := json.Unmarshal(hand.Options, &options); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to decode options")
		return
	}
	chosen, found := handOption{}, false
	for _, opt := range options {
		if opt.Key == req.ChosenOption {
			chosen, found = opt, true
			break
		}
	}
	if !found {
		// Picking outside the bounded set is the one thing an answer may not
		// do. If none of the options is right, the question was wrong, and the
		// repair is a new hand rather than a free-text escape from this one.
		writeError(w, http.StatusBadRequest, "chosen_option is not one of this hand's options")
		return
	}

	answererID, _ := util.ParseUUID(userID)
	answered, err := h.Queries.AnswerRaisedHand(r.Context(), db.AnswerRaisedHandParams{
		ID:           hand.ID,
		ChosenOption: pgtype.Text{String: chosen.Key, Valid: true},
		Answer:       pgtype.Text{String: req.Answer, Valid: req.Answer != ""},
		AnsweredBy:   answererID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusConflict, "this raised hand was already answered")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to answer raised hand")
		return
	}

	// The decision has to be readable by the resumed run, and a comment is the
	// only channel the agent already reads. So the object does not REPLACE the
	// comment — it produces one. That is not a shortcut: the comment is the
	// delivery, the object is the decision.
	actorType, actorID := h.resolveActor(r, userID, uuidToString(issue.WorkspaceID))
	h.postHandAnswerComment(r, issue, hand.Question, chosen, req.Answer, actorType, actorID)

	prevStatus := issue.Status
	promoted, err := h.Queries.UpdateIssueStatus(r.Context(), db.UpdateIssueStatusParams{
		ID:          issue.ID,
		Status:      resumeStatus,
		WorkspaceID: issue.WorkspaceID,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "answer recorded but the issue could not be promoted")
		return
	}

	// And this is the resume. Not written here — the product's own promotion
	// rule, handed the same input UpdateIssue hands it. The re-triggered task is
	// the same (agent_id, issue_id) pair, so GetLastTaskSession resumes the
	// agent's existing session and the run picks up where it stopped.
	if trigger, willRun := h.IssueService.WillEnqueueRun(r.Context(),
		service.IssueTriggerInput{
			Issue:         promoted,
			PrevStatus:    prevStatus,
			StatusChanged: prevStatus != promoted.Status,
		},
		h.issueTriggerWriteProbe(r, actorType, actorID, promoted),
	); willRun {
		h.dispatchIssueRun(r.Context(), promoted, trigger, actorType, actorID, "")
	}

	writeMeasuredJSON(w, http.StatusOK, renderHand(answered))
}

func renderHand(hand db.RaisedHand) map[string]any {
	var options []handOption
	_ = json.Unmarshal(hand.Options, &options)
	out := map[string]any{
		"id":       uuidToString(hand.ID),
		"issue_id": uuidToString(hand.IssueID),
		"question": hand.Question,
		"options":  options,
		"status":   hand.Status,
	}
	if hand.Recommendation.Valid {
		out["recommendation"] = hand.Recommendation.String
	}
	if hand.Material.Valid {
		out["material"] = hand.Material.String
	}
	if hand.ReferentialKey.Valid {
		out["referential"] = hand.ReferentialKey.String
	}
	if hand.ChosenOption.Valid {
		out["chosen_option"] = hand.ChosenOption.String
	}
	if hand.Answer.Valid {
		out["answer"] = hand.Answer.String
	}
	return out
}
