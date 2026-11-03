package logins

import (
	"encoding/csv"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/sil-org/idp-cli/internal/loginlog"
)

type format string

const (
	formatTable format = "table"
	formatCSV   format = "csv"
	formatJSON  format = "json"
)

func parseFormat(s string) (format, error) {
	switch f := format(strings.ToLower(s)); f {
	case formatTable, formatCSV, formatJSON:
		return f, nil
	}
	return "", fmt.Errorf("unknown format %q (choose table, csv, or json)", s)
}

var entryHeader = []string{"Time (UTC)", "User", "CN", "SP", "Hub", "IdP"}

func entryRow(e loginlog.Entry) []string {
	return []string{e.Time, e.User(), e.CN, e.SP, e.HubOrDirect(), e.IDP}
}

func writeEntries(w io.Writer, f format, entries []loginlog.Entry) error {
	switch f {
	case formatJSON:
		return writeJSON(w, entries)
	case formatCSV:
		return writeCSV(w, entryHeader, entries, entryRow)
	default:
		return writeTable(w, entryHeader, entries, entryRow)
	}
}

func groupHeader(by loginlog.GroupBy) []string {
	return []string{strings.ToUpper(string(by)), "Logins", "Users", "First (UTC)", "Last (UTC)"}
}

func groupRow(g loginlog.Group) []string {
	return []string{g.Key, strconv.Itoa(g.Logins), strconv.Itoa(g.Users), g.First, g.Last}
}

// writeGroups writes a summary. totalGroups is the group count before any
// --top truncation, for the table footer.
func writeGroups(w io.Writer, f format, by loginlog.GroupBy, groups []loginlog.Group, totalGroups int, totals loginlog.Totals) error {
	switch f {
	case formatJSON:
		return writeJSON(w, struct {
			By     loginlog.GroupBy `json:"by"`
			Totals loginlog.Totals  `json:"totals"`
			Groups []loginlog.Group `json:"groups"`
		}{by, totals, groups})
	case formatCSV:
		return writeCSV(w, groupHeader(by), groups, groupRow)
	default:
		if err := writeTable(w, groupHeader(by), groups, groupRow); err != nil {
			return err
		}
		_, err := fmt.Fprintf(w, "\n%s logins by %s users in %s groups (%s to %s)\n",
			commas(int64(totals.Logins)), commas(int64(totals.Users)), commas(int64(totalGroups)),
			orDash(totals.First), orDash(totals.Last))
		return err
	}
}

func writeJSON(w io.Writer, v any) error {
	if err := json.MarshalWrite(w, v, jsontext.WithIndent("  ")); err != nil {
		return err
	}
	_, err := fmt.Fprintln(w)
	return err
}

func writeCSV[T any](w io.Writer, header []string, items []T, row func(T) []string) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(header); err != nil {
		return err
	}
	for _, item := range items {
		if err := cw.Write(row(item)); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

func writeTable[T any](w io.Writer, header []string, items []T, row func(T) []string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(header, "\t"))
	for _, item := range items {
		fmt.Fprintln(tw, strings.Join(row(item), "\t"))
	}
	return tw.Flush()
}

// commas formats n with thousands separators.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
