package handler

// SPIKE (not upstream): what a chat bound to a unit is handed.
//
// The renderer is tested in execenv. This covers the LOADER, and the claim it
// makes that is easy to get wrong: only OPEN hands travel. An answered one is
// history, and handing it to an agent asks it to re-argue something settled.

import (
	"context"
	"testing"
)

func TestLoadDiscussionUnit_CarriesOpenHandsOnly(t *testing.T) {
	issueID := handFixtureIssue(t, "todo")

	openID := raise(t, issueID, "Still open?")
	answeredID := raise(t, issueID, "Already settled?")
	if code, _ := answerHand(t, issueID, answeredID, "a"); code != 200 {
		t.Fatalf("could not answer the second hand")
	}

	unit, err := testHandler.loadDiscussionUnit(context.Background(),
		parseUUID(issueID), parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if unit.Title != "multi-hand probe" {
		t.Errorf("title = %q", unit.Title)
	}
	if len(unit.Hands) != 1 {
		t.Fatalf("hands = %d, want only the open one", len(unit.Hands))
	}
	if unit.Hands[0].Question != "Still open?" {
		t.Errorf("carried %q, want the open question", unit.Hands[0].Question)
	}
	// The costs are the reason the object exists. Losing them on the way to
	// the brief turns a decision into a poll.
	if len(unit.Hands[0].Options) != 2 {
		t.Fatalf("options = %d, want 2", len(unit.Hands[0].Options))
	}
	for _, opt := range unit.Hands[0].Options {
		if opt.Cost == "" {
			t.Errorf("option %q reached the brief with no cost", opt.Key)
		}
	}
	// The counter the probe found write-only, now on a third read path.
	if unit.Hands[0].Trigger != "block" {
		t.Errorf("trigger = %q, want it carried", unit.Hands[0].Trigger)
	}
	_ = openID
}

// A unit nobody has asked about still loads — the agent is then being asked
// about the unit itself, which is a different thing from an empty brief.
func TestLoadDiscussionUnit_NoHandsStillLoadsTheUnit(t *testing.T) {
	issueID := handFixtureIssue(t, "todo")
	unit, err := testHandler.loadDiscussionUnit(context.Background(),
		parseUUID(issueID), parseUUID(testWorkspaceID))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if unit == nil || unit.Identifier == "" {
		t.Fatal("want the unit, with its identifier")
	}
	if len(unit.Hands) != 0 {
		t.Errorf("hands = %d, want none", len(unit.Hands))
	}
}
