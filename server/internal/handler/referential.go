// SPIKE (not upstream): the referential catalog.
//
// The bodies of knowledge a raised hand can interrogate. Nearly read-only over
// HTTP: the built-ins are seeded lazily, and the one thing that can be written
// is the OWNER — who holds this body of knowledge. That exists because a hand
// names the reference that failed to answer it, so the reference is the only
// non-guessing route from a question to the agent concerned by it.
// Shaped after issue_status, which is this repo's answer to the same problem.
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type ReferentialResponse struct {
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	IsSystem    bool   `json:"is_system"`
	// OwnerAgentID is the agent who holds this body of knowledge, when one
	// does. A surface deciding which agent to put in front of a question reads
	// the hand's referential and lands here.
	OwnerAgentID *string `json:"owner_agent_id"`
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
			Key:          e.Key,
			Name:         e.Name,
			Description:  e.Description,
			IsSystem:     e.IsSystem,
			OwnerAgentID: uuidToPtr(e.OwnerAgentID),
		})
	}
	writeMeasuredJSON(w, http.StatusOK, map[string]any{"referentials": out})
}

// SetReferentialOwner names the agent who holds one body of knowledge.
//
// An empty owner_agent_id clears it, for the same reason SetLevelGate treats an
// empty status_key as a clear: "this has no owner" is a reasonable thing to say
// and should not come back as a malformed request.
func (h *Handler) SetReferentialOwner(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUserID(w, r); !ok {
		return
	}
	wsUUID, ok := parseUUIDOrBadRequest(w, h.resolveWorkspaceID(r), "workspace id")
	if !ok {
		return
	}
	key := chi.URLParam(r, "key")
	if strings.TrimSpace(key) == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	var req struct {
		OwnerAgentID string `json:"owner_agent_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var owner pgtype.UUID
	if strings.TrimSpace(req.OwnerAgentID) != "" {
		parsed, pok := parseUUIDOrBadRequest(w, strings.TrimSpace(req.OwnerAgentID), "owner_agent_id")
		if !pok {
			return
		}
		// The agent has to be in THIS workspace. Without the check a typo names
		// an owner nothing can resolve, which reads as "unowned" and silently
		// drops the referential out of every concerned-agent list.
		if _, aerr := h.Queries.GetAgentInWorkspace(r.Context(), db.GetAgentInWorkspaceParams{
			ID: parsed, WorkspaceID: wsUUID,
		}); aerr != nil {
			writeError(w, http.StatusNotFound, "agent not found in this workspace")
			return
		}
		owner = parsed
	}
	entry, err := h.Queries.SetReferentialOwner(r.Context(), db.SetReferentialOwnerParams{
		WorkspaceID:  wsUUID,
		Key:          key,
		OwnerAgentID: owner,
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "referential not found")
		return
	}
	writeMeasuredJSON(w, http.StatusOK, ReferentialResponse{
		Key:          entry.Key,
		Name:         entry.Name,
		Description:  entry.Description,
		IsSystem:     entry.IsSystem,
		OwnerAgentID: uuidToPtr(entry.OwnerAgentID),
	})
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
