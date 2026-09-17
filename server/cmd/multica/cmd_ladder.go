// SPIKE (not upstream): `multica level` and `multica waits-on`.
//
// The write half of the ladder. Both endpoints existed with no way to reach
// them but curl, which meant an agent — the thing that actually creates work
// mid-run — could not declare what it had made or what it was stuck behind.
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var ladderRungs = []string{"campaign", "mission", "objective", "task", "step"}

// ── The rung ────────────────────────────────────────────────────────────────

var levelCmd = &cobra.Command{
	Use:   "level <issue> [rung]",
	Short: "Declare what a unit is meant to be",
	Long: "Campaign → Mission → Objective → Task → Step.\n\n" +
		"Undeclared, a unit takes its rung from how deep it sits. Declaring is\n" +
		"how a unit can DISAGREE with its own parentage — an objective you\n" +
		"started on its own, with no mission over it yet. That disagreement is\n" +
		"the only thing that makes such a unit findable: undeclared, it has no\n" +
		"parent, so it reads as a campaign and looks exactly like one.\n\n" +
		"Declaring is not a claim about where it sits. It is a claim about what\n" +
		"it is FOR, and the board reports the two not matching rather than\n" +
		"quietly reconciling them.\n\n" +
		"Pass no rung to print the current one; pass `none` to undeclare.\n\n" +
		"Examples:\n" +
		"  multica level SPIK-24 objective\n" +
		"  multica level SPIK-24 none",
	Args: cobra.RangeArgs(1, 2),
	RunE: runLevel,
}

func runLevel(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if len(args) == 1 {
		var issue map[string]any
		if err := client.GetJSON(ctx, "/api/issues/"+args[0], &issue); err != nil {
			return fmt.Errorf("read issue: %w", err)
		}
		if level := strVal(issue, "level"); level != "" {
			fmt.Println(level)
			return nil
		}
		// Not an error, and worth saying in full: nothing is wrong with a unit
		// that has never been declared, and its rung is still knowable.
		fmt.Fprintln(os.Stderr, "Undeclared — its rung comes from how deep it sits.")
		return nil
	}

	rung := strings.ToLower(strings.TrimSpace(args[1]))
	body := map[string]any{"level": rung}
	if rung == "none" || rung == "null" {
		body["level"] = nil
	} else if !contains(ladderRungs, rung) {
		return fmt.Errorf("unknown rung %q: one of %s, or `none` to undeclare",
			rung, strings.Join(ladderRungs, ", "))
	}

	var out map[string]any
	if err := client.PutJSON(ctx, "/api/issues/"+args[0]+"/level", body, &out); err != nil {
		return fmt.Errorf("set level: %w", err)
	}
	if body["level"] == nil {
		fmt.Fprintf(os.Stderr, "%s is undeclared again.\n", args[0])
		return nil
	}
	// "a objective" reads as a typo in a tool an agent will print verbatim.
	article := "a"
	if strings.ContainsRune("aeiou", rune(rung[0])) {
		article = "an"
	}
	fmt.Fprintf(os.Stderr, "%s is %s %s.\n", args[0], article, rung)
	return nil
}

// ── The wait ────────────────────────────────────────────────────────────────

var waitsOnCmd = &cobra.Command{
	Use:   "waits-on <issue> <blocker>",
	Short: "Declare that a unit cannot start until another finishes",
	Long: "For the wait that stage ordering cannot express.\n\n" +
		"Stages order SIBLINGS under one parent. They cannot say that this unit\n" +
		"waits on something in another mission, another campaign, or another\n" +
		"squad's tree — which is the ordinary case the moment two teams share a\n" +
		"release. This is that link, and the blocker may be anywhere.\n\n" +
		"No cycle check, on purpose: two units each waiting on the other is a\n" +
		"real thing a team does to itself, and it is better seen than refused.\n\n" +
		"Examples:\n" +
		"  multica waits-on SPIK-18 SPIK-25\n" +
		"  multica waits-on SPIK-18 SPIK-25 --remove",
	Args: cobra.ExactArgs(2),
	RunE: runWaitsOn,
}

func runWaitsOn(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{"depends_on": args[1]}
	path := "/api/issues/" + args[0] + "/dependencies"

	if remove, _ := cmd.Flags().GetBool("remove"); remove {
		if err := client.DeleteJSONWithBody(ctx, path, body); err != nil {
			return fmt.Errorf("remove dependency: %w", err)
		}
		fmt.Fprintf(os.Stderr, "%s no longer waits on %s.\n", args[0], args[1])
		return nil
	}

	var out map[string]any
	if err := client.PostJSON(ctx, path, body, &out); err != nil {
		return fmt.Errorf("add dependency: %w", err)
	}
	fmt.Fprintf(os.Stderr, "%s waits on %s.\n", args[0], args[1])
	return nil
}

// ── What a rung costs to run ────────────────────────────────────────────────

var levelPolicyCmd = &cobra.Command{
	Use:   "level-policy [rung]",
	Short: "What a rung runs on — model, thinking, service tier",
	Long: "An agent carries ONE model, one thinking level and one service tier,\n" +
		"so having the same agent run cheaply here and expensively there means\n" +
		"copying it — and a copied persona splits its raised hands, its contest\n" +
		"ratio and its autonomy numbers across the copies.\n\n" +
		"This puts the three settings on the RUNG instead. One persona keeps one\n" +
		"identity, and the level of the work decides how it runs.\n\n" +
		"The agent's own settings are the fallback, not the winner: a rung with\n" +
		"no policy runs on whatever the agent says, exactly as before.\n\n" +
		"Pass no rung to list the ladder. Pass a rung with no flags to clear it.\n\n" +
		"Examples:\n" +
		"  multica level-policy\n" +
		"  multica level-policy step --model haiku --thinking low\n" +
		"  multica level-policy step",
	Args: cobra.MaximumNArgs(1),
	RunE: runLevelPolicy,
}

func runLevelPolicy(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if len(args) == 0 {
		var resp struct {
			Policies []map[string]any `json:"policies"`
		}
		if err := client.GetJSON(ctx, "/api/level-policies", &resp); err != nil {
			return fmt.Errorf("list level policies: %w", err)
		}
		if output, _ := cmd.Flags().GetString("output"); output == "json" {
			return cli.PrintJSON(os.Stdout, resp.Policies)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "RUNG\tMODEL\tTHINKING\tTIER")
		for _, p := range resp.Policies {
			// A rung with no policy prints dashes rather than blanks: an empty
			// cell reads as a value nobody typed, a dash reads as "the agent
			// decides", which is what it means.
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
				strVal(p, "level"),
				dashIfEmpty(strVal(p, "model")),
				dashIfEmpty(strVal(p, "thinking_level")),
				dashIfEmpty(strVal(p, "service_tier")))
		}
		return w.Flush()
	}

	rung := strings.ToLower(strings.TrimSpace(args[0]))
	if !contains(ladderRungs, rung) {
		return fmt.Errorf("unknown rung %q: one of %s", rung, strings.Join(ladderRungs, ", "))
	}

	model, _ := cmd.Flags().GetString("model")
	thinking, _ := cmd.Flags().GetString("thinking")
	tier, _ := cmd.Flags().GetString("tier")
	body := map[string]any{"model": model, "thinking_level": thinking, "service_tier": tier}

	var out map[string]any
	if err := client.PutJSON(ctx, "/api/level-policies/"+rung, body, &out); err != nil {
		return fmt.Errorf("set level policy: %w", err)
	}
	if model == "" && thinking == "" && tier == "" {
		fmt.Fprintf(os.Stderr, "%s runs on whatever its agent says.\n", rung)
		return nil
	}
	fmt.Fprintf(os.Stderr, "%s runs on %s.\n", rung, strings.Join(nonEmpty(model, thinking, tier), " · "))
	return nil
}

func dashIfEmpty(value string) string {
	if value == "" {
		return "—"
	}
	return value
}

func nonEmpty(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

var levelGateCmd = &cobra.Command{
	Use:   "level-gate [rung] [position]",
	Short: "What must say yes before a rung's work can move on",
	Long: "A rung declares what has the right to say NO, and who accepts the\n" +
		"return. `in_review` was the near miss: the whole product respects it —\n" +
		"agents park finished work there, notifications treat it as the thing\n" +
		"that needs you now — but nothing validates a transition anywhere in\n" +
		"Multica. `in_review` to `done` is unguarded, by anyone. It is a place\n" +
		"that holds a unit, not a gate that can refuse to release it.\n\n" +
		"Gates are ORDERED and walked one at a time. Two reviewers queue; they\n" +
		"do not both hold the unit. Gate 2 is reachable only from gate 1, and a\n" +
		"done status only from the last gate.\n\n" +
		"Three things are always allowed, because a gate that traps a unit is\n" +
		"worse than no gate: moving BACKWARDS, dropping to an earlier gate, and\n" +
		"cancelling. Only forward progress is ratified.\n\n" +
		"--ratifier is one of three:\n" +
		"  human           any member\n" +
		"  agent  --agent  one named agent, so the agent that did the work\n" +
		"                  cannot accept its own return\n" +
		"  check  --check  a script already answered; nobody is asked\n\n" +
		"A check gate reads what GitHub reported for the change proposals\n" +
		"attached to the issue, on each one's CURRENT head. It refuses when no\n" +
		"proposal is attached, when a named check has not reported yet, and\n" +
		"when one concluded anything but success. Repeat --check per name.\n\n" +
		"Pass no rung to list every gate. Pass a rung and a position with no\n" +
		"--status to clear that gate.\n\n" +
		"Examples:\n" +
		"  multica level-gate\n" +
		"  multica level-gate task 1 --status qa --ratifier check \\\n" +
		"      --check composition-guard --check frontmatter-lint\n" +
		"  multica level-gate objective 1 --status qa --ratifier agent --agent <id>\n" +
		"  multica level-gate objective 2 --status in_review\n" +
		"  multica level-gate objective 2",
	Args: cobra.MaximumNArgs(2),
	RunE: runLevelGate,
}

func runLevelGate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	if len(args) == 0 {
		var resp struct {
			Gates []map[string]any `json:"gates"`
		}
		if err := client.GetJSON(ctx, "/api/level-gates", &resp); err != nil {
			return fmt.Errorf("list level gates: %w", err)
		}
		if output, _ := cmd.Flags().GetString("output"); output == "json" {
			return cli.PrintJSON(os.Stdout, resp.Gates)
		}
		if len(resp.Gates) == 0 {
			fmt.Fprintln(os.Stderr, "No rung gates anything. Every status moves to every other status.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "RUNG\t#\tSTATUS\tRATIFIED BY")
		for _, g := range resp.Gates {
			ratifier := strVal(g, "ratifier_type")
			if id := strVal(g, "ratifier_id"); id != "" {
				ratifier = ratifier + " " + id
			}
			if rv, _ := g["requires_verdicts"].(bool); rv {
				ratifier = ratifier + " + verdicts"
			}
			if raw, ok := g["required_checks"].([]any); ok && len(raw) > 0 {
				names := make([]string, 0, len(raw))
				for _, n := range raw {
					names = append(names, fmt.Sprint(n))
				}
				ratifier = ratifier + ": " + strings.Join(names, ", ")
			}
			fmt.Fprintf(w, "%s\t%v\t%s\t%s\n",
				strVal(g, "level"), g["position"], strVal(g, "status_key"), ratifier)
		}
		return w.Flush()
	}

	rung := strings.ToLower(strings.TrimSpace(args[0]))
	if !contains(ladderRungs, rung) {
		return fmt.Errorf("unknown rung %q: one of %s", rung, strings.Join(ladderRungs, ", "))
	}
	if len(args) < 2 {
		return fmt.Errorf("a position is required: the order this gate is walked in, starting at 1")
	}
	position, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil || position < 1 {
		return fmt.Errorf("position must be a whole number, 1 or more — it is the order the gates are walked in")
	}

	status, _ := cmd.Flags().GetString("status")
	ratifier, _ := cmd.Flags().GetString("ratifier")
	agentID, _ := cmd.Flags().GetString("agent")
	checks, _ := cmd.Flags().GetStringArray("check")
	verdicts, _ := cmd.Flags().GetBool("verdicts")

	body := map[string]any{"position": position, "status_key": status}
	if status != "" {
		if ratifier == "" {
			// A gate declared without saying who ratifies it defaults to a
			// human. Defaulting to `check` would open a gate that names no
			// check, and defaulting to `agent` has no agent to name.
			ratifier = "human"
		}
		if len(checks) > 0 && ratifier == "human" && !cmd.Flags().Changed("ratifier") {
			// --check alone says what is meant. Making the caller also type
			// `--ratifier check` would be a second way to say the same thing,
			// and a gate that silently ignored --check would be worse.
			ratifier = "check"
		}
		body["ratifier_type"] = ratifier
		if agentID != "" {
			body["ratifier_id"] = agentID
		}
		if len(checks) > 0 {
			body["required_checks"] = checks
		}
		// Independent of the ratifier: "did the right party say yes" and "does
		// the evidence exist" are different questions, and a real gate asks both.
		body["requires_verdicts"] = verdicts
	}

	var resp map[string]any
	if err := client.PutJSON(ctx, "/api/level-gates/"+rung, body, &resp); err != nil {
		return fmt.Errorf("set level gate: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp)
	}
	if status == "" {
		fmt.Printf("Gate %d cleared on %s.\n", position, rung)
		return nil
	}
	if len(checks) > 0 {
		fmt.Printf("%s gate %d: %s, ratified by %s (%s).\n",
			rung, position, status, ratifier, strings.Join(checks, ", "))
		return nil
	}
	fmt.Printf("%s gate %d: %s, ratified by %s.\n", rung, position, status, ratifier)
	return nil
}

func init() {
	waitsOnCmd.Flags().Bool("remove", false, "Drop the wait instead of declaring it")

	levelPolicyCmd.Flags().String("model", "", "Model this rung runs on")
	levelPolicyCmd.Flags().String("thinking", "", "Thinking level this rung runs on")
	levelPolicyCmd.Flags().String("tier", "", "Service tier this rung runs on")
	levelPolicyCmd.Flags().String("output", "table", "Output format: table or json")

	levelGateCmd.Flags().String("status", "", "Status a unit sits in while this gate holds it (empty clears the gate)")
	levelGateCmd.Flags().String("ratifier", "", "Who accepts the return: human | agent | check (default human)")
	levelGateCmd.Flags().Bool("verdicts", false, "Also require a passing verdict on every acceptance criterion")
	levelGateCmd.Flags().StringArray("check", nil, "Check that must conclude success, exactly as GitHub names it. Repeatable; implies --ratifier check")
	levelGateCmd.Flags().String("agent", "", "Agent id, required when --ratifier agent")
	levelGateCmd.Flags().String("output", "table", "Output format: table or json")
}
