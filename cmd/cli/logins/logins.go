/*
Copyright © 2026 SIL Global
*/

// Package logins implements the "logins" commands, which query the login
// audit log written by ssp-base's loginfinalizer:LogToDynamo filter.
package logins

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/sil-org/idp-cli/cmd/cli/flags"
	"github.com/sil-org/idp-cli/internal/loginlog"
)

// filterFlags are per-invocation (deliberately not bound to viper, so a
// config file can't silently narrow every query).
type filterFlags struct {
	since, until string
	idpEntity    string
	sp, hub      string
	user         string
}

func (ff *filterFlags) register(cmd *cobra.Command) {
	pf := cmd.PersistentFlags()
	pf.StringVar(&ff.since, "since", "", `only logins at/after this time: 24h, 7d, 2w, 2026-10-01, "2026-10-01 13:45" (UTC)`)
	pf.StringVar(&ff.until, "until", "", "only logins at/before this time (same forms as --since; a date or a time without seconds includes that whole day or minute)")
	pf.StringVar(&ff.idpEntity, "idp-entity", "", "only logins at this IdP entity ID")
	pf.StringVar(&ff.sp, "sp", "", "only logins to this SP entity ID")
	pf.StringVar(&ff.hub, "hub", "", `only logins via this Hub entity ID, or "direct" for logins with no Hub`)
	pf.StringVar(&ff.user, "user", "", "only logins by this user (exact EmployeeNumber, eduPersonPrincipalName, or CN)")
}

func (ff *filterFlags) filter(now time.Time) (loginlog.Filter, error) {
	since, err := loginlog.ParseTimeArg(ff.since, now, false)
	if err != nil {
		return loginlog.Filter{}, fmt.Errorf("--since: %w", err)
	}
	until, err := loginlog.ParseTimeArg(ff.until, now, true)
	if err != nil {
		return loginlog.Filter{}, fmt.Errorf("--until: %w", err)
	}
	if !since.IsZero() && !until.IsZero() && until.Before(since) {
		return loginlog.Filter{}, fmt.Errorf("--until (%s) is before --since (%s)",
			until.Format(loginlog.TimeLayout), since.Format(loginlog.TimeLayout))
	}
	return loginlog.Filter{
		Since: since,
		Until: until,
		IDP:   ff.idpEntity,
		SP:    ff.sp,
		Hub:   ff.hub,
		User:  ff.user,
	}, nil
}

func SetupLoginsCmd(parentCommand *cobra.Command) {
	ff := &filterFlags{}

	cmd := &cobra.Command{
		Use:   "logins",
		Short: "Query the login audit log",
		Long: `Query the login audit log that ssp-base's loginfinalizer:LogToDynamo writes to DynamoDB.

The table is keyed only by a random ID, so every query is a full-table Scan;
filters reduce what is transferred, not the read capacity consumed. Use
--max-rcu to keep a scan from competing with the IdPs' writes on a
provisioned-capacity table.

AWS credentials come from --profile, or else the standard chain (AWS_PROFILE,
env vars, SSO, ...).`,
	}
	parentCommand.AddCommand(cmd)

	flags.NewStringFlag(cmd, flags.LogTable, "", "", "required - DynamoDB login log table name")
	flags.NewStringFlag(cmd, flags.AwsProfile, "", "", "AWS profile from your shared config/credentials files (like the AWS CLI's --profile)")
	flags.NewStringFlag(cmd, flags.DynamoEndpoint, "", "", "DynamoDB endpoint override (only for dynamodb-local, e.g. http://localhost:8000)")
	flags.NewIntFlag(cmd, flags.ScanSegments, "", 4, "number of parallel scan segments")
	flags.NewFloatFlag(cmd, flags.MaxRCU, "", 500, "max read capacity units per second for the scan (0 = unlimited)")
	flags.NewIntFlag(cmd, flags.MaxRecords, "", 1_000_000, "stop with an error if more than this many logins match, to protect memory (0 = no limit)")
	ff.register(cmd)

	initListCmd(cmd, ff)
	initSummaryCmd(cmd, ff)
	initTuiCmd(cmd, ff)
}

// maxScanSegments is DynamoDB's limit on a Scan's TotalSegments.
const maxScanSegments = 1_000_000

// scanLimits reads and validates the scan tuning parameters. Negative values
// are rejected rather than treated as "unlimited": they're almost certainly
// typos, and silently lifting a limit is the dangerous direction to fail in.
func scanLimits() (loginlog.Scanner, error) {
	s := loginlog.Scanner{
		Segments:   viper.GetInt(flags.ScanSegments),
		MaxRCU:     viper.GetFloat64(flags.MaxRCU),
		MaxRecords: viper.GetInt(flags.MaxRecords),
	}
	switch {
	case s.Segments < 1 || s.Segments > maxScanSegments:
		return loginlog.Scanner{}, fmt.Errorf("%s must be between 1 and %d, not %d", flags.ScanSegments, maxScanSegments, s.Segments)
	case !(s.MaxRCU >= 0): // also rejects NaN
		return loginlog.Scanner{}, fmt.Errorf("%s must be 0 (unlimited) or more, not %v", flags.MaxRCU, s.MaxRCU)
	case s.MaxRecords < 0:
		return loginlog.Scanner{}, fmt.Errorf("%s must be 0 (no limit) or more, not %d", flags.MaxRecords, s.MaxRecords)
	}
	return s, nil
}

func newScanner(ctx context.Context) (loginlog.Scanner, error) {
	scanner, err := scanLimits()
	if err != nil {
		return loginlog.Scanner{}, err
	}

	table := viper.GetString(flags.LogTable)
	if table == "" {
		return loginlog.Scanner{}, fmt.Errorf("parameter %[1]s is not set, use --%[1]s on command line or include in idp-cli.toml file", flags.LogTable)
	}

	client, err := loginlog.NewClient(ctx, loginlog.ClientOptions{
		Region:   viper.GetString(flags.Region),
		Profile:  viper.GetString(flags.AwsProfile),
		Endpoint: viper.GetString(flags.DynamoEndpoint),
	})
	if err != nil {
		return loginlog.Scanner{}, err
	}

	scanner.Client = client
	scanner.Table = table
	return scanner, nil
}

// runScan performs a scan for the non-interactive commands, reporting progress
// on stderr when it's a terminal. Ctrl-C cancels the scan cleanly.
func runScan(ff *filterFlags) ([]loginlog.Entry, error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	filter, err := ff.filter(time.Now())
	if err != nil {
		return nil, err
	}
	scanner, err := newScanner(ctx)
	if err != nil {
		return nil, err
	}

	progress := &loginlog.Progress{}
	showProgress := isatty.IsTerminal(os.Stderr.Fd())
	done := make(chan struct{})
	if showProgress {
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.Tick(250 * time.Millisecond):
					fmt.Fprintf(os.Stderr, "\r%s", progressLine(progress))
				}
			}
		}()
	}

	entries, err := scanner.Scan(ctx, filter, progress)
	close(done)
	if showProgress {
		fmt.Fprintf(os.Stderr, "\r%s\n", progressLine(progress))
	}
	return entries, err
}

func progressLine(p *loginlog.Progress) string {
	return fmt.Sprintf("Scanned %s items, %s matched, %s RCU consumed",
		commas(p.Scanned.Load()), commas(p.Matched.Load()), commas(int64(p.ConsumedRCU())))
}
