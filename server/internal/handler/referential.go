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
