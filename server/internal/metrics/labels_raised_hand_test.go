package metrics

import "testing"

// SPIKE (not upstream): the raised-hand label normalizers.
//
// These exist to stop a cardinality leak, so the case that matters is the one
// that does NOT come from the seeded catalog.

func TestNormalizeReferential_BucketsCustomKeys(t *testing.T) {
	// A workspace may add its own referentials. Each one becoming its own
	// Prometheus series is how a metrics endpoint turns into an outage, so a
	// custom key collapses to one bucket. The workspace still sees its own key
	// at full fidelity in the mission API — only the metric buckets it.
	if got := NormalizeReferential("our_internal_playbook"); got != "custom" {
		t.Errorf("custom referential = %q, want custom", got)
	}
	if got := NormalizeReferential(""); got != "custom" {
		t.Errorf("empty referential = %q, want custom", got)
	}
}

func TestNormalizeReferential_KeepsSeededKeys(t *testing.T) {
	for _, key := range []string{
		"design_system", "api_contract", "architecture",
		"brand_register", "product_direction", "unclassified",
	} {
		if got := NormalizeReferential(key); got != key {
			t.Errorf("NormalizeReferential(%q) = %q, want it unchanged", key, got)
		}
	}
	// Case and padding are normalized rather than bucketed: the same
	// referential arriving two ways must not split into two series.
	if got := NormalizeReferential("  Design_System  "); got != "design_system" {
		t.Errorf("padded/mixed-case key = %q, want design_system", got)
	}
}

func TestNormalizeHandRecipient_OnlyTwoValues(t *testing.T) {
	if got := NormalizeHandRecipient("lead"); got != "lead" {
		t.Errorf("lead = %q", got)
	}
	if got := NormalizeHandRecipient("human"); got != "human" {
		t.Errorf("human = %q", got)
	}
	// Anything else is a bug upstream, and it must not mint a series.
	if got := NormalizeHandRecipient("squad"); got != "other" {
		t.Errorf("unknown recipient = %q, want other", got)
	}
}

func TestRaisedHandMetricsDeclareTheirLabels(t *testing.T) {
	// metricLabels reads businessMetricLabels; a metric missing from that table
	// registers with no labels and then panics at the first WithLabelValues.
	for name, want := range map[string]int{
		"multica_raised_hand_total":           2,
		"multica_raised_hand_settled_total":   2,
		"multica_raised_hand_escalated_total": 1,
	} {
		if got := len(metricLabels(name)); got != want {
			t.Errorf("%s has %d labels, want %d", name, got, want)
		}
	}
	// And no id-shaped label sneaked in.
	for _, name := range []string{
		"multica_raised_hand_total",
		"multica_raised_hand_settled_total",
		"multica_raised_hand_escalated_total",
	} {
		for _, label := range metricLabels(name) {
			if label == "lead" || label == "agent" || label == "agent_id" || label == "issue" {
				t.Errorf("%s carries %q — an unbounded label belongs in the API, not here", name, label)
			}
		}
	}
}
