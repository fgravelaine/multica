package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/spf13/cobra"
)

// SPIKE (not upstream): the verify beat, at the command line.
//
// AP-5's file already says how this works. These two commands only give it
// somewhere to write:
//
//	"Verify per criterion. Each acceptance criterion gets its own line and its
//	 own verdict. No aggregate 'works fine'."
//	"Evidence or it did not happen."
//	"No criteria, no verdict."

var criteriaCmd = &cobra.Command{
	Use:   "criteria <issue>",
	Short: "The acceptance criteria a unit is verified against",
	Long: "Criteria are what T4 rules on, one verdict each. Without them a gate\n" +
		"that reads verdicts has nothing to read, and \"every criterion passed\"\n" +
		"is true over an empty list — so an issue with no criteria does not pass,\n" +
		"it goes back to whoever scoped it.\n\n" +
		"--from-description reads the issue's own `## Acceptance Criteria`\n" +
		"section. The checkbox list in the issue template is already a list of\n" +
		"criteria; this is what finally reads it.\n\n" +
		"Setting the list REPLACES it, and drops the verdicts with it. That is\n" +
		"deliberate: a verdict is evidence about one exact statement, and a\n" +
		"statement that has been rewritten has not been verified.\n\n" +
		"Examples:\n" +
		"  multica criteria SPIK-24\n" +
		"  multica criteria SPIK-24 --from-description\n" +
		"  multica criteria SPIK-24 --set 'Copy matches the register' --set 'List excludes churned users'",
	Args: exactArgs(1),
	RunE: runCriteria,
}

var verdictCmd = &cobra.Command{
	Use:   "verdict <issue> <criterion> <pass|fail>",
	Short: "Rule on one acceptance criterion, with the evidence",
	Long: "One criterion, one verdict, and the evidence is required on a PASS as\n" +
		"much as on a fail — a pass nobody can reproduce is the aggregate\n" +
		"\"works fine\" in a per-criterion costume.\n\n" +
		"Verdicts accumulate. Re-checking after a fix adds a row rather than\n" +
		"overwriting one, so \"criterion 2 failed twice before passing\" stays\n" +
		"an answerable question.\n\n" +
		"Example:\n" +
		"  multica verdict SPIK-24 2 fail \\\n" +
		"      --evidence 'Ran the export: 1,204 churned users still in the list.'",
	Args: exactArgs(3),
	RunE: runVerdict,
}

func runCriteria(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return err
	}

	set, _ := cmd.Flags().GetStringArray("set")
	fromDesc, _ := cmd.Flags().GetBool("from-description")

	if len(set) > 0 || fromDesc {
		body := map[string]any{}
		if fromDesc {
			body["from_description"] = true
		} else {
			body["statements"] = set
		}
		var resp struct {
			Count int `json:"count"`
		}
		if err := client.PutJSON(ctx, "/api/issues/"+issueRef.ID+"/criteria", body, &resp); err != nil {
			return fmt.Errorf("set acceptance criteria: %w", err)
		}
		fmt.Printf("%d criteria on %s. Every verdict was cleared with them.\n", resp.Count, args[0])
		return nil
	}

	var resp struct {
		Criteria []map[string]any `json:"criteria"`
	}
	if err := client.GetJSON(ctx, "/api/issues/"+issueRef.ID+"/criteria", &resp); err != nil {
		return fmt.Errorf("list acceptance criteria: %w", err)
	}
	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp.Criteria)
	}
	if len(resp.Criteria) == 0 {
		fmt.Fprintln(os.Stderr, "No acceptance criteria. This issue cannot be verified, and that is a finding.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "#\tVERDICT\tCRITERION\tEVIDENCE")
	for _, c := range resp.Criteria {
		verdict := "—"
		if ruled, _ := c["ruled"].(bool); ruled {
			if passed, _ := c["passed"].(bool); passed {
				verdict = "pass"
			} else {
				verdict = "FAIL"
			}
		}
		fmt.Fprintf(w, "%v\t%s\t%s\t%s\n", c["ordinal"], verdict,
			strVal(c, "statement"), strVal(c, "evidence"))
	}
	return w.Flush()
}

func runVerdict(cmd *cobra.Command, args []string) error {
	ordinal, err := strconv.Atoi(strings.TrimSpace(args[1]))
	if err != nil || ordinal < 1 {
		return fmt.Errorf("the criterion is its position in the list, starting at 1")
	}
	var passed bool
	switch strings.ToLower(strings.TrimSpace(args[2])) {
	case "pass":
		passed = true
	case "fail":
		passed = false
	default:
		// No third value. "partially" and "n/a" are how a verdict stops meaning
		// anything, and AP-5's file already rules that out: "you decide only
		// what is true".
		return fmt.Errorf("a verdict is `pass` or `fail`")
	}
	evidence, _ := cmd.Flags().GetString("evidence")
	if strings.TrimSpace(evidence) == "" {
		return fmt.Errorf("--evidence is required: steps to reproduce, expected, observed")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issueRef, err := resolveIssueRef(ctx, client, args[0])
	if err != nil {
		return err
	}

	var resp map[string]any
	if err := client.PostJSON(ctx, "/api/issues/"+issueRef.ID+"/verdict", map[string]any{
		"ordinal": ordinal, "passed": passed, "evidence": evidence,
	}, &resp); err != nil {
		return fmt.Errorf("record the verdict: %w", err)
	}
	outcome := "fail"
	if passed {
		outcome = "pass"
	}
	fmt.Printf("Criterion %d on %s: %s.\n", ordinal, args[0], outcome)
	return nil
}

func init() {
	criteriaCmd.Flags().StringArray("set", nil, "Replace the criteria with these statements. Repeatable.")
	criteriaCmd.Flags().Bool("from-description", false, "Read them from the issue's `## Acceptance Criteria` section")
	criteriaCmd.Flags().String("output", "table", "Output format: table or json")

	verdictCmd.Flags().String("evidence", "", "Steps to reproduce, expected, observed (required)")
	verdictCmd.Flags().String("output", "table", "Output format: table or json")
}
