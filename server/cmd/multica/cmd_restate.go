package main

import (
	"github.com/spf13/cobra"
)

// SPIKE (not upstream): `restate`, the cycle verb, under the name the contract
// uses for it.
//
// The operation already existed as `multica issue update`. What did not exist
// was the WORD — and the gap is not cosmetic. An agent told to restate a unit
// does not find `update`, and an agent that finds `update` has no way to know
// it is the verb it was told to use. A contract that names fourteen verbs and a
// CLI that spells one of them differently is a contract with a hole in it.
//
// This is deliberately NARROWER than `issue update`, and that is the point.
// Galactics defines restate as "replace what a work item says it is — its goal,
// its criteria, its scope", and separates it from `move` (which changes state)
// and `assign` (which changes owner). `issue update` does all three through one
// door. Exposing that door under the name `restate` would make the word mean
// "change anything", which is how a vocabulary stops being a contract.
//
// So: title and description only. Everything else keeps its own verb.
var restateCmd = &cobra.Command{
	Use:   "restate <issue>",
	Short: "Replace what a work item says it is — its goal, its criteria, its scope",
	Long: "Restating REPLACES the unit's own content. It is not a comment.\n\n" +
		"A comment adds to the record and leaves the item saying what it said.\n" +
		"Restating changes what the item IS, which is what happens when the\n" +
		"answer to a raised hand lands on the unit rather than on a reference.\n" +
		"Conflating the two is how a ticket ends up with its real criteria\n" +
		"buried three comments deep.\n\n" +
		"Only the goal, the criteria and the scope. To change state use\n" +
		"`multica issue status`; to change owner use `multica issue assign`.\n" +
		"They are different verbs because they are different acts.\n\n" +
		"Examples:\n" +
		"  multica restate SPIK-24 --title 'Send the launch email to active users'\n" +
		"  multica restate SPIK-24 --description-file ./criteria.md",
	Args: exactArgs(1),
	RunE: runIssueUpdate,
}

func init() {
	// The same flag names as `issue update`, because runIssueUpdate reads them
	// by name off the command it is given. Only the content flags are exposed —
	// a flag that is absent here is a verb that lives elsewhere.
	restateCmd.Flags().String("title", "", "New goal, in one line")
	restateCmd.Flags().String("description", "", "New body (decodes \\n, \\t)")
	restateCmd.Flags().Bool("description-stdin", false, "Read the new body from stdin")
	restateCmd.Flags().String("description-file", "", "Read the new body from a file")
	restateCmd.Flags().Bool("allow-external-file", false, "Allow --description-file outside the working directory")
	restateCmd.Flags().Bool("no-start", false, "Restate without starting an agent run")
	restateCmd.Flags().String("output", "json", "Output format: table or json")
}
