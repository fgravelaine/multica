package handler

import (
	"context"
	"fmt"
	"testing"

	"github.com/multica-ai/multica/server/internal/testutil"
	"github.com/multica-ai/multica/server/internal/util"
)

// counterpartTriggerAgentID returns the id of the trigger routed through
// NID-22's counterpart mechanism, if any.
func counterpartTriggerAgentID(triggers []commentAgentTrigger) (string, bool) {
	for _, tr := range triggers {
		if tr.Source == commentTriggerSourceCounterpart {
			return uuidToString(tr.Agent.ID), true
		}
	}
	return "", false
}

// TestCommentCounterpart_AssigneeWakesCreator: an assignee's comment wakes
// the issue's creator, when the creator is another agent.
func TestCommentCounterpart_AssigneeWakesCreator(t *testing.T) {
	ctx := context.Background()
	assigneeID := createHandlerTestAgent(t, "Counterpart Assignee", nil)
	creatorID := createHandlerTestAgent(t, "Counterpart Creator", nil)

	issueID := dbfx.Issue(t, "assignee wakes creator", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   assigneeID,
		"creator_type":  "agent",
		"creator_id":    creatorID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, "done, please review", nil, "agent", assigneeID, commentTriggerComputeOptions{})
	got, ok := counterpartTriggerAgentID(triggers)
	if !ok || got != creatorID {
		t.Fatalf("counterpart trigger = %v (ok=%v), want creator %s", got, ok, creatorID)
	}
}

// TestCommentCounterpart_AssigneeWakesParentAssigneeWhenCreatorIsMember: when
// the creator is a member, the assignee's comment wakes the parent issue's
// assignee instead.
func TestCommentCounterpart_AssigneeWakesParentAssigneeWhenCreatorIsMember(t *testing.T) {
	ctx := context.Background()
	assigneeID := createHandlerTestAgent(t, "Counterpart Child Assignee", nil)
	parentAssigneeID := createHandlerTestAgent(t, "Counterpart Parent Assignee", nil)

	parentIssueID := dbfx.Issue(t, "parent task", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   parentAssigneeID,
	})
	issueID := dbfx.Issue(t, "child task, member-created", testutil.Cols{
		"assignee_type":   "agent",
		"assignee_id":     assigneeID,
		"creator_type":    "member",
		"creator_id":      testUserID,
		"parent_issue_id": parentIssueID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, "done, please review", nil, "agent", assigneeID, commentTriggerComputeOptions{})
	got, ok := counterpartTriggerAgentID(triggers)
	if !ok || got != parentAssigneeID {
		t.Fatalf("counterpart trigger = %v (ok=%v), want parent assignee %s", got, ok, parentAssigneeID)
	}
}

// TestCommentCounterpart_NonAssigneeAgentWakesAssignee: a comment from an
// agent that is not the assignee (e.g. the creator asking for changes) wakes
// the assignee.
func TestCommentCounterpart_NonAssigneeAgentWakesAssignee(t *testing.T) {
	ctx := context.Background()
	assigneeID := createHandlerTestAgent(t, "Counterpart Assignee 2", nil)
	creatorID := createHandlerTestAgent(t, "Counterpart Creator 2", nil)

	issueID := dbfx.Issue(t, "creator asks for changes", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   assigneeID,
		"creator_type":  "agent",
		"creator_id":    creatorID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, "please fix X", nil, "agent", creatorID, commentTriggerComputeOptions{})
	got, ok := counterpartTriggerAgentID(triggers)
	if !ok || got != assigneeID {
		t.Fatalf("counterpart trigger = %v (ok=%v), want assignee %s", got, ok, assigneeID)
	}
}

// TestCommentCounterpart_NeverTheAuthor: an agent that is both the issue's
// creator and its assignee never wakes itself.
func TestCommentCounterpart_NeverTheAuthor(t *testing.T) {
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "Counterpart Self", nil)

	issueID := dbfx.Issue(t, "self created and self assigned", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   agentID,
		"creator_type":  "agent",
		"creator_id":    agentID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, "noting progress", nil, "agent", agentID, commentTriggerComputeOptions{})
	if got, ok := counterpartTriggerAgentID(triggers); ok {
		t.Fatalf("counterpart trigger = %v, want none (never the author)", got)
	}
}

// TestCommentCounterpart_ExplicitMentionWins: an explicit @agent mention in
// an agent-authored comment routes to the mentioned agent, not the
// counterpart.
func TestCommentCounterpart_ExplicitMentionWins(t *testing.T) {
	ctx := context.Background()
	assigneeID := createHandlerTestAgent(t, "Counterpart Assignee 3", nil)
	creatorID := createHandlerTestAgent(t, "Counterpart Creator 3", nil)
	mentionedID := createHandlerTestAgent(t, "Counterpart Mentioned", nil)

	issueID := dbfx.Issue(t, "explicit mention wins", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   assigneeID,
		"creator_type":  "agent",
		"creator_id":    creatorID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	content := fmt.Sprintf("[@Mentioned](mention://agent/%s) can you take a look", mentionedID)
	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, content, nil, "agent", assigneeID, commentTriggerComputeOptions{})

	if _, ok := counterpartTriggerAgentID(triggers); ok {
		t.Fatalf("explicit mention should suppress the counterpart route")
	}
	found := false
	for _, tr := range triggers {
		if uuidToString(tr.Agent.ID) == mentionedID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the explicitly mentioned agent %s to be triggered, got %+v", mentionedID, triggers)
	}
}

// TestCommentCounterpart_NoteWakesNobody: a comment starting with /note wakes
// nobody, even on an agent-assigned issue.
func TestCommentCounterpart_NoteWakesNobody(t *testing.T) {
	ctx := context.Background()
	assigneeID := createHandlerTestAgent(t, "Counterpart Assignee 4", nil)
	creatorID := createHandlerTestAgent(t, "Counterpart Creator 4", nil)

	issueID := dbfx.Issue(t, "note wakes nobody", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   assigneeID,
		"creator_type":  "agent",
		"creator_id":    creatorID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, "/note approved", nil, "agent", assigneeID, commentTriggerComputeOptions{})
	if len(triggers) != 0 {
		t.Fatalf("triggers = %+v, want none for a /note comment", triggers)
	}
}

// TestCommentCounterpart_CapAfterSixConsecutive: the 7th consecutive
// non-member comment since the last member comment wakes nobody; a member
// comment resets the count.
func TestCommentCounterpart_CapAfterSixConsecutive(t *testing.T) {
	ctx := context.Background()
	assigneeID := createHandlerTestAgent(t, "Counterpart Cap Assignee", nil)
	creatorID := createHandlerTestAgent(t, "Counterpart Cap Creator", nil)

	issueID := dbfx.Issue(t, "cap after six consecutive", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   assigneeID,
		"creator_type":  "agent",
		"creator_id":    creatorID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	// Six consecutive assignee comments (no member comment in between): each
	// of the first six must still wake the creator.
	for i := 1; i <= 6; i++ {
		content := fmt.Sprintf("round %d", i)
		dbfx.Comment(t, issueID, content, testutil.Cols{
			"author_type": "agent",
			"author_id":   assigneeID,
		})
		triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, content, nil, "agent", assigneeID, commentTriggerComputeOptions{})
		if _, ok := counterpartTriggerAgentID(triggers); !ok {
			t.Fatalf("comment %d: expected a counterpart trigger, got none", i)
		}
	}

	// The 7th consecutive comment wakes nobody.
	seventhContent := "round 7"
	dbfx.Comment(t, issueID, seventhContent, testutil.Cols{
		"author_type": "agent",
		"author_id":   assigneeID,
	})
	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, seventhContent, nil, "agent", assigneeID, commentTriggerComputeOptions{})
	if got, ok := counterpartTriggerAgentID(triggers); ok {
		t.Fatalf("7th consecutive comment triggered %v, want none (cap reached)", got)
	}

	// A member comment resets the count: the next agent comment wakes again.
	dbfx.Comment(t, issueID, "looks good, one more pass please", testutil.Cols{
		"author_type": "member",
		"author_id":   testUserID,
	})
	afterResetContent := "round after reset"
	dbfx.Comment(t, issueID, afterResetContent, testutil.Cols{
		"author_type": "agent",
		"author_id":   assigneeID,
	})
	triggers, _ = testHandler.computeCommentAgentTriggers(ctx, issue, afterResetContent, nil, "agent", assigneeID, commentTriggerComputeOptions{})
	if _, ok := counterpartTriggerAgentID(triggers); !ok {
		t.Fatalf("expected a counterpart trigger after the member comment reset the count")
	}
}

// TestCommentCounterpart_SystemCommentNeitherCountsNorResets: a system
// comment (child-done notice, hand raise/answer, ...) sitting among the six
// consecutive agent comments neither spends a slot nor resets the count —
// only author_type == "agent" counts toward the cap. The 7th agent comment
// still wakes nobody.
func TestCommentCounterpart_SystemCommentNeitherCountsNorResets(t *testing.T) {
	ctx := context.Background()
	assigneeID := createHandlerTestAgent(t, "Counterpart System Assignee", nil)
	creatorID := createHandlerTestAgent(t, "Counterpart System Creator", nil)

	issueID := dbfx.Issue(t, "system comment among six", testutil.Cols{
		"assignee_type": "agent",
		"assignee_id":   assigneeID,
		"creator_type":  "agent",
		"creator_id":    creatorID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	// Three agent comments, a system comment, then three more agent comments:
	// six agent comments total, still no member comment in between, so the
	// sixth must still wake the creator.
	for i := 1; i <= 3; i++ {
		content := fmt.Sprintf("round %d", i)
		dbfx.Comment(t, issueID, content, testutil.Cols{
			"author_type": "agent",
			"author_id":   assigneeID,
		})
	}
	dbfx.Comment(t, issueID, "child issue done", testutil.Cols{
		"author_type": "system",
		"author_id":   "00000000-0000-0000-0000-000000000000",
	})
	var sixthContent string
	for i := 4; i <= 6; i++ {
		sixthContent = fmt.Sprintf("round %d", i)
		dbfx.Comment(t, issueID, sixthContent, testutil.Cols{
			"author_type": "agent",
			"author_id":   assigneeID,
		})
	}
	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, sixthContent, nil, "agent", assigneeID, commentTriggerComputeOptions{})
	if _, ok := counterpartTriggerAgentID(triggers); !ok {
		t.Fatalf("6th agent comment (with a system comment among them) triggered none, want a counterpart trigger")
	}

	// The 7th consecutive agent comment wakes nobody.
	seventhContent := "round 7"
	dbfx.Comment(t, issueID, seventhContent, testutil.Cols{
		"author_type": "agent",
		"author_id":   assigneeID,
	})
	triggers, _ = testHandler.computeCommentAgentTriggers(ctx, issue, seventhContent, nil, "agent", assigneeID, commentTriggerComputeOptions{})
	if got, ok := counterpartTriggerAgentID(triggers); ok {
		t.Fatalf("7th consecutive agent comment triggered %v, want none (cap reached)", got)
	}
}

// TestCommentCounterpart_SquadAssignedUnchanged: a squad-assigned issue keeps
// MUL-3879's leader-only path; the new counterpart mechanism never applies.
func TestCommentCounterpart_SquadAssignedUnchanged(t *testing.T) {
	ctx := context.Background()
	leaderID := createHandlerTestAgent(t, "Counterpart Squad Leader", nil)
	workerID := createHandlerTestAgent(t, "Counterpart Squad Worker", nil)
	squadID := dbfx.Squad(t, "Counterpart Squad", leaderID)

	issueID := dbfx.Issue(t, "squad assigned unchanged", testutil.Cols{
		"assignee_type": "squad",
		"assignee_id":   squadID,
		"creator_type":  "agent",
		"creator_id":    workerID,
	})
	issue, err := testHandler.Queries.GetIssue(ctx, util.MustParseUUID(issueID))
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	// A worker (non-leader) comment must still resolve solely through the
	// squad leader fallback, with no commentTriggerSourceCounterpart trigger.
	triggers, _ := testHandler.computeCommentAgentTriggers(ctx, issue, "worker update", nil, "agent", workerID, commentTriggerComputeOptions{})
	if got, ok := counterpartTriggerAgentID(triggers); ok {
		t.Fatalf("squad-assigned issue routed through the counterpart mechanism: %v", got)
	}
	found := false
	for _, tr := range triggers {
		if tr.Source == commentTriggerSourceIssueAssignee && uuidToString(tr.Agent.ID) == leaderID {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the squad leader fallback to trigger the leader, got %+v", triggers)
	}
}

