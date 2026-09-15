// SPIKE (not upstream): the referential vocabulary.
package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var referentialCmd = &cobra.Command{
	Use:   "referential",
	Short: "The bodies of knowledge a raised hand can interrogate",
	Long: "A referential is what an agent checks its question against before\n" +
		"disturbing a human: the design system, the API contract, the\n" +
		"architecture. Every raised hand names one.\n\n" +
		"The count is the point. Twelve hands on the design system and none on\n" +
		"architecture says which referential is too thin to answer on its own —\n" +
		"which turns a queue of interruptions into a measurement of where\n" +
		"knowledge is missing.",
}

var referentialListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the referentials in this workspace",
	Args:  cobra.NoArgs,
	RunE:  runReferentialList,
}

func runReferentialList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	var resp struct {
		Referentials []map[string]any `json:"referentials"`
	}
	if err := client.GetJSON(ctx, "/api/referentials", &resp); err != nil {
		return fmt.Errorf("list referentials: %w", err)
	}

	if output, _ := cmd.Flags().GetString("output"); output == "json" {
		return cli.PrintJSON(os.Stdout, resp.Referentials)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "KEY\tNAME\tWHAT IT ANSWERS")
	for _, r := range resp.Referentials {
		fmt.Fprintf(w, "%s\t%s\t%s\n", strVal(r, "key"), strVal(r, "name"), strVal(r, "description"))
	}
	return w.Flush()
}

func init() {
	referentialListCmd.Flags().String("output", "table", "Output format: table or json")
	referentialCmd.AddCommand(referentialListCmd)
}
