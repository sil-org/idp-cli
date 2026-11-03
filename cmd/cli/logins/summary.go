package logins

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/sil-org/idp-cli/internal/loginlog"
)

func initSummaryCmd(parent *cobra.Command, ff *filterFlags) {
	var (
		byArg     string
		top       int
		formatArg string
	)

	cmd := &cobra.Command{
		Use:   "summary",
		Short: "Count logins and distinct users, grouped by SP, Hub, IdP, day, or user",
		Example: `  idp-cli logins summary --since 30d --by hub
  idp-cli logins summary --since 2026-10-01 --until 2026-10-31 --by day
  idp-cli logins summary --since 7d --by user --top 20
  idp-cli logins summary --hub direct --by sp --format csv`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			by, err := loginlog.ParseGroupBy(byArg)
			if err != nil {
				return err
			}
			f, err := parseFormat(formatArg)
			if err != nil {
				return err
			}
			cmd.SilenceUsage = true

			entries, err := runScan(ff)
			if err != nil {
				return err
			}

			groups := loginlog.Summarize(entries, by)
			totalGroups := len(groups)
			if top > 0 && totalGroups > top {
				defer fmt.Fprintf(os.Stderr, "Showing top %d of %d groups (use --top 0 for all)\n", top, totalGroups)
				groups = groups[:top]
			}
			return writeGroups(os.Stdout, f, by, groups, totalGroups, loginlog.Total(entries))
		},
	}
	parent.AddCommand(cmd)

	cmd.Flags().StringVarP(&byArg, "by", "b", string(loginlog.BySP), "group by: sp, hub, idp, day, or user")
	cmd.Flags().IntVar(&top, "top", 0, "show only the N largest groups (0 = all)")
	cmd.Flags().StringVarP(&formatArg, "format", "o", string(formatTable), "output format: table, csv, or json")
}
