package execenv

import (
	"strings"
	"testing"
)

// SPIKE (not upstream): the agent is handed its mandate.
//
// The criteria became objects in migration 490 and the gate reads their
// verdicts — but nothing gave them to the agent that has to SATISFY them. It
// received a description and was left to find the list inside it. AP-5's file
// calls the criteria "your entire mandate"; a mandate you have to go looking
// for is not one.

func TestBrief_RendersAcceptanceCriteria(t *testing.T) {
	var b strings.Builder
	writeAcceptanceCriteria(&b, TaskContextForEnv{AcceptanceCriteria: []CriterionForEnv{
		{Ordinal: 1, Statement: "Returns used, limit and a percentage", Ruled: true, Passed: true, Evidence: "curl returned all three"},
		{Ordinal: 2, Statement: "Free plan gets limit 1000", Ruled: true, Passed: false, Evidence: "got 500"},
		{Ordinal: 3, Statement: "Unauthenticated gets 401", Ruled: false},
	}})
	out := b.String()

	for _, want := range []string{
		"1. [pass] Returns used, limit and a percentage",
		"2. [FAIL] Free plan gets limit 1000",
		"evidence: got 500",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("brief is missing %q:\n%s", want, out)
		}
	}

	// THE ONE THAT MATTERS. Passed is coalesced upstream and means nothing
	// without Ruled, so rendering it alone would mark every criterion nobody
	// has looked at yet as FAILED — the most alarming possible way to say
	// "not checked", and it would send an agent chasing a bug that is not there.
	if !strings.Contains(out, "3. [—] Unauthenticated gets 401") {
		t.Errorf("an unruled criterion should render as —, not as a verdict:\n%s", out)
	}
	if strings.Contains(out, "3. [FAIL]") {
		t.Errorf("an unruled criterion was rendered as failed:\n%s", out)
	}
}

func TestBrief_NoCriteriaRendersNothing(t *testing.T) {
	// An issue with no criteria gets no section, and no verify commands either.
	// Carrying the surface into every run is the dead weight MUL-5442 already
	// called out for squad maintenance.
	var b strings.Builder
	ctx := TaskContextForEnv{}
	writeAcceptanceCriteria(&b, ctx)
	writeVerifyCommands(&b, ctx)
	if b.Len() != 0 {
		t.Errorf("an issue with no criteria rendered %q", b.String())
	}
}

func TestBrief_VerifyCommandsNameTheVerb(t *testing.T) {
	// An agent told to "record the verdict on the issue" — which is exactly what
	// AP-5's file says — reaches for `issue comment add`, because that is the
	// verb it already knows. The brief has to name the structured one next to
	// the criteria, and say why a comment will not do.
	var b strings.Builder
	writeVerifyCommands(&b, TaskContextForEnv{AcceptanceCriteria: []CriterionForEnv{
		{Ordinal: 1, Statement: "only"},
	}})
	out := b.String()
	if !strings.Contains(out, "multica verdict") {
		t.Errorf("the brief does not name `multica verdict`:\n%s", out)
	}
	if !strings.Contains(out, "A comment is not a verdict") {
		t.Errorf("the brief does not say why a comment will not do:\n%s", out)
	}
}

func TestBrief_CriterionStatementIsSanitized(t *testing.T) {
	// Statements are user-authored, like status names. A crafted one must not
	// inject a heading or break out of the surrounding markdown.
	var b strings.Builder
	writeAcceptanceCriteria(&b, TaskContextForEnv{AcceptanceCriteria: []CriterionForEnv{
		{Ordinal: 1, Statement: "## Injected heading\nand a second line"},
	}})
	out := b.String()
	if strings.Contains(out, "\n## Injected heading") {
		t.Errorf("a crafted criterion injected a heading:\n%s", out)
	}
}
