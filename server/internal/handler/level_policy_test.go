package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// SPIKE (not upstream): what a rung costs to run.
//
// The point of the feature is that ONE agent keeps one identity while the level
// of the work picks how it runs — instead of copying the agent per
// configuration and splitting its raised hands, its contest ratio and its
// autonomy numbers across the copies. These pin the resolution, because a
// silent wrong answer here spends real money on the wrong model.

func policyTestIssue(t *testing.T, title string) db.Issue {
	t.Helper()
	id := createTestIssue(t, title, "todo", "none")
	t.Cleanup(func() { deleteTestIssue(t, id) })

	var uuid pgtype.UUID
	if err := uuid.Scan(id); err != nil {
		t.Fatalf("parse issue id: %v", err)
	}
	issue, err := testHandler.Queries.GetIssue(context.Background(), uuid)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}
	return issue
}

func setPolicy(t *testing.T, level, model, thinking, tier string) {
	t.Helper()
	body := map[string]any{"model": model, "thinking_level": thinking, "service_tier": tier}
	w := httptest.NewRecorder()
	req := newRequest("PUT", "/api/level-policies/"+level+"?workspace_id="+testWorkspaceID, body)
	req = withURLParam(req, "level", level)
	testHandler.SetLevelPolicy(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("SetLevelPolicy %s: got %d: %s", level, w.Code, w.Body.String())
	}
	t.Cleanup(func() {
		w := httptest.NewRecorder()
		req := newRequest("PUT", "/api/level-policies/"+level+"?workspace_id="+testWorkspaceID,
			map[string]any{"model": "", "thinking_level": "", "service_tier": ""})
		req = withURLParam(req, "level", level)
		testHandler.SetLevelPolicy(w, req)
	})
}

func TestLevelPolicy_NoPolicyLeavesTheAgentAlone(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// The default, and the one that must never break: a workspace that has set
	// nothing runs exactly as it did before this existed.
	issue := policyTestIssue(t, "policy: untouched")
	agentData := &TaskAgentData{Model: "agent-model", ThinkingLevel: "high", ServiceTier: "priority"}

	testHandler.applyLevelPolicy(context.Background(), agentData, issue)

	if agentData.Model != "agent-model" || agentData.ThinkingLevel != "high" || agentData.ServiceTier != "priority" {
		t.Errorf("no policy changed the agent's settings: %+v", agentData)
	}
}

func TestLevelPolicy_RungWinsOverTheAgent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// "Put it at the level" means the level decides. The agent's own model is
	// the fallback for a rung with no opinion, not the winner.
	issue := policyTestIssue(t, "policy: rung wins")
	setPolicy(t, "campaign", "haiku", "low", "")

	agentData := &TaskAgentData{Model: "opus", ThinkingLevel: "high"}
	testHandler.applyLevelPolicy(context.Background(), agentData, issue)

	// A parentless issue is a campaign by depth, with nothing declared.
	if agentData.Model != "haiku" || agentData.ThinkingLevel != "low" {
		t.Errorf("rung policy did not win: %+v", agentData)
	}
}

func TestLevelPolicy_UnsetFieldsDoNotClearTheAgent(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A policy naming only a model must not silently wipe the agent's thinking
	// level. NULL means "no opinion", never "clear it" — the distinction the
	// three independent columns exist for.
	issue := policyTestIssue(t, "policy: partial")
	setPolicy(t, "campaign", "haiku", "", "")

	agentData := &TaskAgentData{Model: "opus", ThinkingLevel: "high", ServiceTier: "priority"}
	testHandler.applyLevelPolicy(context.Background(), agentData, issue)

	if agentData.Model != "haiku" {
		t.Errorf("model = %q, want haiku", agentData.Model)
	}
	if agentData.ThinkingLevel != "high" || agentData.ServiceTier != "priority" {
		t.Errorf("a model-only policy cleared other fields: %+v", agentData)
	}
}

func TestLevelPolicy_DeclaredRungBeatsDepth(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	// A parentless issue is a campaign by depth. Declaring it a step must move
	// it onto the step's policy — otherwise declaring a rung is decoration,
	// and the whole point is that it decides something.
	issue := policyTestIssue(t, "policy: declared beats depth")
	setPolicy(t, "campaign", "opus", "", "")
	setPolicy(t, "step", "haiku", "", "")

	if _, err := testHandler.Queries.SetIssueLevel(context.Background(), db.SetIssueLevelParams{
		ID:          issue.ID,
		WorkspaceID: issue.WorkspaceID,
		Level:       pgtype.Text{String: "step", Valid: true},
	}); err != nil {
		t.Fatalf("declare level: %v", err)
	}
	issue.Level = pgtype.Text{String: "step", Valid: true}

	agentData := &TaskAgentData{Model: "sonnet"}
	testHandler.applyLevelPolicy(context.Background(), agentData, issue)

	if agentData.Model != "haiku" {
		t.Errorf("model = %q, want haiku — the declared rung, not the depth", agentData.Model)
	}
}
