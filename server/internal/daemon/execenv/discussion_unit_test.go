package execenv

// SPIKE (not upstream): the brief a chat gets when it is bound to a unit.
//
// The claim under test is not "the section renders". It is that the COSTS
// survive — an option without the cost of being wrong on its side is a poll,
// and an agent asked to contest a recommendation needs exactly the thing the
// raiser thought it was risking.

import (
	"strings"
	"testing"
)

func discussionFixture() TaskContextForEnv {
	return TaskContextForEnv{DiscussionUnit: &DiscussionUnitForEnv{
		Identifier: "YAV-207",
		Title:      "The customer side",
		Status:     "needs_discussion",
		Body:       "Four domains with no persona and no responsibility.",
		Hands: []HandForEnv{{
			Question:       "Is support a responsibility, a squad, or a workflow?",
			Recommendation: "workflow",
			Material:       "Veezeet has users.",
			Referential:    "unclassified",
			Trigger:        "block",
			Options: []HandOptionForEnv{
				{Key: "responsibility", Label: "Its own responsibility", Cost: "a tenth owner on unmeasured volume"},
				{Key: "workflow", Label: "A workflow in an existing squad", Cost: "promotion later, and promotions get deferred"},
			},
		}},
	}}
}

func TestBrief_DiscussionUnitCarriesEveryCost(t *testing.T) {
	var b strings.Builder
	writeUnitUnderDiscussion(&b, discussionFixture())
	out := b.String()

	for _, want := range []string{
		"YAV-207",
		"The customer side",
		"Four domains with no persona",
		"Is support a responsibility, a squad, or a workflow?",
		// Both costs, in full. This is the assertion that matters.
		"a tenth owner on unmeasured volume",
		"promotion later, and promotions get deferred",
		"RECOMMENDED",
		"CONTEST",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("brief is missing %q\n---\n%s", want, out)
		}
	}
}

// A chat bound to no issue renders nothing at all — the ordinary case.
func TestBrief_NoDiscussionUnitRendersNothing(t *testing.T) {
	var b strings.Builder
	writeUnitUnderDiscussion(&b, TaskContextForEnv{})
	if b.Len() != 0 {
		t.Errorf("unbound chat rendered %d bytes, want none:\n%s", b.Len(), b.String())
	}
}

// A unit whose questions have all been answered says so rather than rendering
// an empty list, which reads as "there is nothing to decide" and is a different
// claim from "nobody has asked yet".
func TestBrief_UnitWithNoOpenHandsSaysSo(t *testing.T) {
	ctx := discussionFixture()
	ctx.DiscussionUnit.Hands = nil
	var b strings.Builder
	writeUnitUnderDiscussion(&b, ctx)
	if !strings.Contains(b.String(), "parked on no question") {
		t.Errorf("want the no-question case stated explicitly:\n%s", b.String())
	}
}
