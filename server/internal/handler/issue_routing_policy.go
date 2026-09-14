// SPIKE (not upstream): the write surface for task-level routing.
//
// An issue carries a routing policy; every task it enqueues inherits it, and
// the claim resolves it against whichever machine is asking for work. See
// migrations/479_task_routing_policy.up.sql for what the policy can and cannot
// express, and why the "cannot" is the interesting half.
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

// routingPolicyPattern mirrors the CHECK constraint in migration 479. Kept as a
// separate check here so a bad value comes back as 400 with the grammar in the
// message, rather than as a 500 carrying a raw constraint name.
var routingPolicyPattern = regexp.MustCompile(`^(runtime:[0-9a-fA-F-]{36}|provider:[a-z0-9_-]{1,64})$`)

// SetIssueRoutingPolicy sets or clears an issue's routing policy.
//
// An empty string clears it, which returns the issue to inheriting its agent's
// runtime binding — today's behaviour, and the reason every untouched issue in
// the workspace keeps working exactly as before.
func (h *Handler) SetIssueRoutingPolicy(w http.ResponseWriter, r *http.Request) {
	issue, ok := h.loadIssueForUser(w, r, chi.URLParam(r, "id"))
	if !ok {
		return
	}

	var req struct {
		RoutingPolicy string `json:"routing_policy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	policy := strings.TrimSpace(req.RoutingPolicy)
	if policy != "" && !routingPolicyPattern.MatchString(policy) {
		writeError(w, http.StatusBadRequest,
			"routing_policy must be 'runtime:<uuid>' or 'provider:<name>', or empty to clear")
		return
	}

	updated, err := h.Queries.SetIssueRoutingPolicy(r.Context(), db.SetIssueRoutingPolicyParams{
		ID:            issue.ID,
		RoutingPolicy: pgtype.Text{String: policy, Valid: policy != ""},
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to set routing policy")
		return
	}

	writeMeasuredJSON(w, http.StatusOK, map[string]any{
		"id":             uuidToString(updated.ID),
		"routing_policy": policy,
	})
}
