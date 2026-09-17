package main

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// SPIKE (not upstream): every cycle verb is reachable from `multica` alone.
//
// This test exists because the bug it catches already happened, silently, to
// all six of them. They were registered on the root, they worked, and none
// appeared in `multica` — the help template ranges over GROUPS, so a command
// with no GroupID renders nowhere. There is no error, no warning, and the
// command keeps working for anyone who already knows its name.
//
// Which is the whole problem. Agents do not read SPIKE-NOTES; they find out
// what they may do from the CLI. An agent that hits a block at framing, looks
// for how to stop and ask, and is shown only `issue`, `comment` and `status`
// does what the raised-hand doc says it does without the verb — it "does its
// best, and the defect is found much later".
//
// A verb nobody can find is counted zero times, and zero is exactly what a
// team that never needed it produces. The two are indistinguishable in the
// number, which makes every counter this spike built unreadable.
func TestCycleVerbsAreDiscoverable(t *testing.T) {
	// The contract's verbs, by the name the contract uses. `hand` carries raise
	// and answer as subcommands; `restate` and the ladder verbs are their own.
	wantVerbs := []string{
		"hand",
		"referential",
		"restate",
		"level",
		"level-gate",
		"level-policy",
		"waits-on",
	}

	// rootCmd is wired by the package's init(), including initHelp, so it is
	// fully assembled here — the same object `multica` renders from.
	root := rootCmd

	byName := map[string]*cobra.Command{}
	for _, c := range root.Commands() {
		byName[c.Name()] = c
	}

	for _, verb := range wantVerbs {
		cmd, ok := byName[verb]
		if !ok {
			t.Errorf("%q is not registered on the root command", verb)
			continue
		}
		if cmd.GroupID == "" {
			t.Errorf("%q has no GroupID: it works, and `multica` will not list it", verb)
			continue
		}
		if cmd.GroupID != groupCycle {
			t.Errorf("%q is in group %q, want %q", verb, cmd.GroupID, groupCycle)
		}
		// A command with no Short renders as a blank line in the group, which is
		// worse than being absent — it looks like a rendering bug rather than a
		// verb.
		if strings.TrimSpace(cmd.Short) == "" {
			t.Errorf("%q has no Short: it would list as an empty line", verb)
		}
	}

	// And the group itself has to exist, or every assignment above is inert.
	var found bool
	for _, g := range root.Groups() {
		if g.ID == groupCycle {
			found = true
		}
	}
	if !found {
		t.Fatalf("group %q is not registered: the verbs are grouped into nothing", groupCycle)
	}
}

// TestNoRootCommandIsUngrouped is the general form, and the one that will catch
// the NEXT verb rather than these seven. Any command on the root with no group
// is invisible in help.
func TestNoRootCommandIsUngrouped(t *testing.T) {
	root := rootCmd
	for _, c := range root.Commands() {
		if !c.IsAvailableCommand() || c.Name() == "help" || c.Name() == "completion" {
			continue
		}
		if c.GroupID == "" {
			t.Errorf("%q is on the root with no GroupID — it will not appear in `multica`", c.Name())
		}
	}
}
