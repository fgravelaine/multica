// SPIKE (not upstream): delivering a raised hand's answer to the agent.
package handler

import (
	"fmt"
	"log/slog"
	"net/http"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/dbid"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// postHandAnswerComment writes the decision where the resumed run will read it.
//
// The raised hand is the object; this comment is its delivery. Splitting them
// that way is what the free-text-comment status quo cannot do: a comment alone
// is both the question and the answer and the record, so nothing can act on it.
// Here the object holds the decision — which option, by whom, when — and the
// comment carries only what the agent needs in order to continue.
//
// Authored as `system` rather than as the deciding member on purpose. A member
// comment is itself a trigger (the assignee-fallback path in issue.go), so
// posting one here would race the promotion below into a second run on the same
// issue. The promotion is the single intended trigger.
//
// Best-effort: the decision is already recorded on the hand, so a failure here
// is logged rather than returned. It does mean a resumed run could start with
// the answer missing from its comment feed, which is a real hole and is written
// up in SPIKE-NOTES rather than papered over.
func (h *Handler) postHandAnswerComment(r *http.Request, issue db.Issue, question string, chosen handOption, answer, actorType, actorID string) {
	content := fmt.Sprintf(
		"**Raised hand answered** — %s\n\n**Decision: %s** (`%s`)\n\nCost of being wrong on this side: %s",
		question, chosen.Label, chosen.Key, chosen.Cost,
	)
	if answer != "" {
		content += "\n\n" + answer
	}
	content += "\n\nContinue from where you stopped."

	created, err := h.Queries.CreateComment(r.Context(), db.CreateCommentParams{
		ID:          dbid.NewV7(),
		IssueID:     issue.ID,
		WorkspaceID: issue.WorkspaceID,
		AuthorType:  "system",
		AuthorID:    pgtype.UUID{Valid: true},
		Content:     content,
		Type:        "system",
		ParentID:    pgtype.UUID{Valid: false},
	})
	if err != nil {
		slog.Warn("raised hand: answer comment failed",
			"error", err, "issue_id", uuidToString(issue.ID))
		return
	}

	comment := created.Comment()
	h.publish(protocol.EventCommentCreated, uuidToString(issue.WorkspaceID), "system", "", map[string]any{
		"comment":             commentToResponse(comment, nil, nil),
		"issue_title":         issue.Title,
		"issue_assignee_type": textToPtr(issue.AssigneeType),
		"issue_assignee_id":   uuidToPtr(issue.AssigneeID),
		"issue_status":        issue.Status,
		"issue_revision":      created.IssueRevision,
	})
}
