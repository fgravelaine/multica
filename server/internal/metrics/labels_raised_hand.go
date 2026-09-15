package metrics

// SPIKE (not upstream): label normalizers for the raised hand.
//
// The referential is a per-workspace catalog and a workspace may add its own
// keys, so the label is bucketed against the six seeded built-ins and
// everything else collapses to "custom". That is the same trade every
// normalizer in this package makes: a metric label must have a small fixed set
// of values, and an unbounded one is a cardinality leak that shows up as a
// Prometheus outage rather than as a bug.
//
// The workspace still sees its own custom referentials at full fidelity in the
// mission API. Only the metric buckets them.

var knownReferentials = map[string]string{
	"design_system":     "design_system",
	"api_contract":      "api_contract",
	"architecture":      "architecture",
	"brand_register":    "brand_register",
	"product_direction": "product_direction",
	"unclassified":      "unclassified",
}

// knownHandRecipients doubles as the level allow-list: where a hand was SENT
// and who ANSWERED it share the same two words.
var knownHandRecipients = map[string]string{
	"lead":  "lead",
	"human": "human",
}

// NormalizeReferential buckets a referential key for a metric label.
// A workspace's own key becomes "custom" rather than its own series.
func NormalizeReferential(value string) string {
	return normalizeFromAllowList(value, knownReferentials, "custom")
}

// NormalizeHandRecipient buckets a hand's recipient or answering level.
func NormalizeHandRecipient(value string) string {
	return normalizeFromAllowList(value, knownHandRecipients, "other")
}
