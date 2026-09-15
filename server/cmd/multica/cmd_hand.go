// SPIKE (not upstream): `multica hand` — raise, list, answer.
//
// `raise` is the half an agent calls from inside a run; the daemon already puts
// MULTICA_TOKEN and MULTICA_TASK_ID in its environment, so it needs no setup.
// `list` and `answer` are for whoever decides.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var handCmd = &cobra.Command{
	Use:   "hand",
	Short: "Raise a hand for a decision, or answer one",
	Long: "A raised hand is a unit stopping for a decision it may not take.\n\n" +
		"It is not a question in a comment: it carries bounded options, what it\n" +
		"costs to be WRONG on each side, and a recommendation. Raising one parks\n" +
		"the issue; answering it returns the issue to the board and the agent\n" +
		"resumes where it stopped.",
}

// ── Raise ───────────────────────────────────────────────────────────────────

var handRaiseCmd = &cobra.Command{
	Use:   "raise <issue>",
	Short: "Stop this unit and ask for a decision",
	Long: "Raise a hand on an issue.\n\n" +
		"Every option needs a cost — what it costs to be wrong on that side.\n" +
		"That is what lets someone who does not know the domain decide in thirty\n" +
		"seconds, and it is why the option syntax requires three parts:\n\n" +
		"  --option 'key|label|cost of being wrong'\n\n" +
		"--referential names the body of knowledge that failed to answer you —\n" +
		"the design system, the API contract, the architecture. It is required:\n" +
		"a pile of hands on one referential is what says that referential is too\n" +
		"thin to answer on its own, and a hand that does not name one cannot\n" +
		"contribute to that. Run `multica referential list` for the keys, or use\n" +
		"`unclassified` when you genuinely cannot tell which one failed you.\n\n" +
		"Example:\n" +
		"  multica hand raise SPIK-7 \\\n" +
		"    --question 'Is a plan shareable with travel companions?' \\\n" +
		"    --referential product_direction \\\n" +
		"    --option 'a|Solo plan|Three weeks sooner, but the collaborative door closes for a year' \\\n" +
		"    --option 'b|Shared from the start|The model carries the invitation; complexity paid even if nobody shares' \\\n" +
		"    --recommend a",
	Args: cobra.ExactArgs(1),
	RunE: runHandRaise,
}

func runHandRaise(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}

	question, _ := cmd.Flags().GetString("question")
	if strings.TrimSpace(question) == "" {
		return fmt.Errorf("--question is required")
	}
	rawOptions, _ := cmd.Flags().GetStringArray("option")
	if len(rawOptions) < 2 {
		return fmt.Errorf("at least two --option values are required — a raised hand with one option is not a question")
	}

	options := make([]map[string]string, 0, len(rawOptions))
	for _, raw := range rawOptions {
		parts := strings.SplitN(raw, "|", 3)
		if len(parts) != 3 {
			return fmt.Errorf("option %q must be 'key|label|cost of being wrong'", raw)
		}
		key, label, cost := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]), strings.TrimSpace(parts[2])
		if key == "" || label == "" || cost == "" {
			return fmt.Errorf("option %q needs a non-empty key, label and cost", raw)
		}
		options = append(options, map[string]string{"key": key, "label": label, "cost": cost})
	}

	recommend, _ := cmd.Flags().GetString("recommend")
	material, _ := cmd.Flags().GetString("material")
	referential, _ := cmd.Flags().GetString("referential")
	if strings.TrimSpace(referential) == "" {
		return fmt.Errorf("--referential is required: which body of knowledge failed to answer this.\n" +
			"Run `multica referential list` for the keys, or use `unclassified` if you cannot tell")
	}

	body := map[string]any{
		"question":    question,
		"options":     options,
		"referential": referential,
	}
	if recommend != "" {
		body["recommendation"] = recommend
	}
	if material != "" {
		body["material"] = material
	}
	// Present when a run raises this itself; absent when a human does.
	if taskID := os.Getenv("MULTICA_TASK_ID"); taskID != "" {
		body["task_id"] = taskID
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var hand map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+args[0]+"/hands", body, &hand); err != nil {
		return fmt.Errorf("raise hand: %w", err)
	}

	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, hand)
	}
	fmt.Fprintf(os.Stderr, "Hand raised on %s. The issue is parked until it is answered.\n", args[0])
	return nil
}

// ── List ────────────────────────────────────────────────────────────────────

var handListCmd = &cobra.Command{
	Use:   "list <issue>",
	Short: "Show the hands raised on an issue",
	Args:  cobra.ExactArgs(1),
	RunE:  runHandList,
}

func runHandList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp struct {
		Hands []map[string]any `json:"hands"`
	}
	if err := client.GetJSON(ctx, "/api/issues/"+args[0]+"/hands", &resp); err != nil {
		return fmt.Errorf("list hands: %w", err)
	}

	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp.Hands)
	}
	if len(resp.Hands) == 0 {
		fmt.Fprintln(os.Stderr, "No hands raised on this issue.")
		return nil
	}

	for _, hand := range resp.Hands {
		fmt.Printf("\n%s  [%s]\n", strVal(hand, "question"), strVal(hand, "status"))
		if ref := strVal(hand, "referential"); ref != "" {
			fmt.Printf("referential: %s\n", ref)
		}
		if rt := strVal(hand, "recipient_type"); rt != "" {
			fmt.Printf("with:        %s\n", rt)
		}
		if rec := strVal(hand, "recommendation"); rec != "" {
			fmt.Printf("recommends: %s\n", rec)
		}
		if chosen := strVal(hand, "chosen_option"); chosen != "" {
			fmt.Printf("answered:   %s\n", chosen)
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "  KEY\tOPTION\tCOST OF BEING WRONG")
		if opts, ok := hand["options"].([]any); ok {
			for _, o := range opts {
				opt, ok := o.(map[string]any)
				if !ok {
					continue
				}
				fmt.Fprintf(w, "  %s\t%s\t%s\n", strVal(opt, "key"), strVal(opt, "label"), strVal(opt, "cost"))
			}
		}
		_ = w.Flush()
		if material := strVal(hand, "material"); material != "" {
			fmt.Printf("\n%s\n", material)
		}
	}
	return nil
}

// ── Answer ──────────────────────────────────────────────────────────────────

var handAnswerCmd = &cobra.Command{
	Use:   "answer <issue> <option-key>",
	Short: "Decide, and send the unit back to the board",
	Long: "Pick one of the raised hand's options.\n\n" +
		"The decision is recorded on the hand, delivered to the agent as a\n" +
		"comment, and the issue is promoted — which re-triggers the agent on the\n" +
		"same session, so it continues rather than starting over.",
	Args: cobra.ExactArgs(2),
	RunE: runHandAnswer,
}

func runHandAnswer(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	note, _ := cmd.Flags().GetString("note")

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var hand map[string]any
	body := map[string]any{"chosen_option": args[1]}
	if note != "" {
		body["answer"] = note
	}
	if err := client.PostJSON(ctx, "/api/issues/"+args[0]+"/hands/answer", body, &hand); err != nil {
		return fmt.Errorf("answer hand: %w", err)
	}

	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, hand)
	}
	fmt.Fprintf(os.Stderr, "Answered %s with %q. The issue is back on the board and the agent resumes.\n", args[0], args[1])
	return nil
}

// ── Escalate ────────────────────────────────────────────────────────────────

var handEscalateCmd = &cobra.Command{
	Use:   "escalate <issue>",
	Short: "Pass a hand up: the lead could not settle it",
	Long: "The lead's move when the answer is not in any referential it can\n" +
		"reach and it may not decide itself.\n\n" +
		"Its job before this is to CONTEST: most raised hands close against a\n" +
		"referential without anyone being disturbed. A lead that forwards the\n" +
		"question unchanged has only moved the interruption. Use --note to say\n" +
		"what was already ruled out, so the human starts from a smaller question.",
	Args: cobra.ExactArgs(1),
	RunE: runHandEscalate,
}

func runHandEscalate(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	note, _ := cmd.Flags().GetString("note")

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var hand map[string]any
	body := map[string]any{}
	if note != "" {
		body["note"] = note
	}
	if err := client.PostJSON(ctx, "/api/issues/"+args[0]+"/hands/escalate", body, &hand); err != nil {
		return fmt.Errorf("escalate hand: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, hand)
	}
	fmt.Fprintf(os.Stderr, "Escalated %s. It is now on a human.\n", args[0])
	return nil
}

func init() {
	handRaiseCmd.Flags().String("question", "", "What is being asked, in one line (required)")
	handRaiseCmd.Flags().StringArray("option", nil, "Repeatable: 'key|label|cost of being wrong' (at least two)")
	handRaiseCmd.Flags().String("referential", "", "Which body of knowledge failed to answer (required; see `multica referential list`)")
	handRaiseCmd.Flags().String("recommend", "", "Option key the raiser recommends")
	handRaiseCmd.Flags().String("material", "", "Anything the decider needs to look at")
	handRaiseCmd.Flags().String("output", "table", "Output format: table or json")

	handListCmd.Flags().String("output", "table", "Output format: table or json")

	handAnswerCmd.Flags().String("note", "", "Anything to add beyond the chosen option")
	handAnswerCmd.Flags().String("output", "table", "Output format: table or json")

	handCmd.AddCommand(handRaiseCmd)
	handCmd.AddCommand(handListCmd)
	handEscalateCmd.Flags().String("note", "", "What the lead already ruled out, so the human starts from a smaller question")
	handEscalateCmd.Flags().String("output", "table", "Output format: table or json")

	handCmd.AddCommand(handAnswerCmd)
	handCmd.AddCommand(handEscalateCmd)
}
