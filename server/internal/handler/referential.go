// SPIKE (not upstream): the referential catalog.
//
// The bodies of knowledge a raised hand can interrogate. Read-only over HTTP:
// the built-ins are seeded lazily and there is no create/edit endpoint here,
// because the spike needs the vocabulary to COUNT, not to be administered.
// Shaped after issue_status, which is this repo's answer to the same problem.
package handler

import (
	"net/http"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ReferentialResponse struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsSystem    bool   `json:"is_system"`
}

// ListReferentials returns the workspace's referential vocabulary, seeding the
// built-ins on first read so a workspace that has never raised a hand still
// gets a list rather than an empty one.
func (h *Handler) ListReferentials(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	workspaceID := h.resolveWorkspaceID(r)
	wsUUID, ok := parseUUIDOrBadRequest(w, workspaceID, "workspace id")
	if !ok {
		return
	}
	if err := h.Queries.SeedReferentials(r.Context(), wsUUID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to seed referentials")
		return
	}
	entries, err := h.Queries.ListReferentials(r.Context(), db.ListReferentialsParams{
		WorkspaceID:     wsUUID,
		IncludeArchived: false,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list referentials")
		return
	}
	out := make([]ReferentialResponse, 0, len(entries))
	for _, e := range entries {
		out = append(out, ReferentialResponse{
			Key:         e.Key,
			Name:        e.Name,
			Description: e.Description,
			IsSystem:    e.IsSystem,
		})
	}
	writeMeasuredJSON(w, http.StatusOK, map[string]any{"referentials": out})
}

// ReferentialLoopRow is one referential's half-loop: how many hands it could not
// answer, and how many of those answers came back as rules.
type ReferentialLoopRow struct {
	Key          string `json:"key"`
	Hands        int    `json:"hands"`
	Rules        int    `json:"rules"`
	LocalChoices int    `json:"local_choices"`
	StillOpen    int    `json:"still_open"`
}

// GetReferentialLoop answers the question the diagnostic could only half ask.
//
// Counting hands per referential says which body of knowledge is too thin to
// answer on its own. It does not say whether anything is being done about it.
// Twelve hands and zero rules is a reference being asked the same kind of
// question over and over and learning nothing from it — which looks identical,
// in a count of hands alone, to a reference that is steadily improving.
func (h *Handler) GetReferentialLoop(w http.ResponseWriter, r *http.Request) {
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace_id")
	if !ok {
		return
	}
	rows, err := h.Queries.CountReferentialLoop(r.Context(), wsUUID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read the referential loop")
		return
	}
	out := make([]ReferentialLoopRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, ReferentialLoopRow{
			Key:          row.ReferentialKey.String,
			Hands:        int(row.Hands),
			Rules:        int(row.Rules),
			LocalChoices: int(row.LocalChoices),
			StillOpen:    int(row.StillOpen),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"referentials": out})
}
