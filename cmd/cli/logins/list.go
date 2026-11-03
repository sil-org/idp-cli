package logins

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sil-org/idp-cli/internal/loginlog"
)

func initListCmd(parent *cobra.Command, ff *filterFlags) {
	var (
		limit     int
		formatArg string
	)

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List individual logins, newest first",
		Example: `  idp-cli logins list --since 24h
  idp-cli logins list --since 2026-10-01 --until 2026-10-31 --sp https://app.example.org --format csv > oct.csv
  idp-cli logins list --user 10001 --limit 0 --format json | jq .`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := parseFormat(formatArg)
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true

			entries, err := runScan(ff)
			if err != nil {
				return err
			}

			loginlog.SortNewestFirst(entries)
			total := len(entries)
			if limit > 0 && total > limit {
				entries = entries[:limit]
				defer fmt.Fprintf(os.Stderr, "Showing newest %d of %d matching logins (use --limit 0 for all)\n", limit, total)
			}
			return writeEntries(os.Stdout, f, entries)
		},
	}
	parent.AddCommand(cmd)

	cmd.Flags().IntVarP(&limit, "limit", "n", 50, "show at most this many logins (0 = all)")
	cmd.Flags().StringVarP(&formatArg, "format", "o", string(formatTable), "output format: table, csv, or json")
}
