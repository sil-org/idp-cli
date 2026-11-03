package logins

import (
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sil-org/idp-cli/internal/loginlog"
)

func key(s string) tea.KeyMsg {
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// export drives the TUI's export flow to path and returns the updated model.
func export(t *testing.T, m tuiModel, path string) tuiModel {
	t.Helper()
	m = update(t, m, key("e"))
	if !m.exportIn.Focused() {
		t.Fatal("e should open the export prompt")
	}
	m.exportIn.SetValue(path)
	next, cmd := m.Update(key("enter"))
	m = next.(tuiModel)
	if cmd == nil {
		return m // e.g. validation error
	}
	return update(t, m, cmd()) // run the write synchronously, deliver the result
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return records
}

func TestExport_LoginsTabExportsTheFilteredView(t *testing.T) {
	m := loadedModel(t)
	m = submit(t, m, map[int]string{fieldSP: "sp-a"})
	path := filepath.Join(t.TempDir(), "out.csv")

	m = export(t, m, path)

	records := readCSV(t, path)
	if len(records) != 3 { // header + 2 sp-a logins
		t.Fatalf("got %d records, want header + 2: %v", len(records), records)
	}
	if strings.Join(records[0], ",") != strings.Join(entryHeader, ",") {
		t.Errorf("header = %v, want %v", records[0], entryHeader)
	}
	for _, r := range records[1:] {
		if r[3] != "sp-a" {
			t.Errorf("exported a login outside the filter: %v", r)
		}
	}
	if !strings.Contains(m.status, "Exported 2 logins to "+path) {
		t.Errorf("status = %q", m.status)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("file mode = %v, want 0600", perm)
		}
	}
}

func TestExport_LoginsTabIncludesRowsBeyondTheTableCap(t *testing.T) {
	n := maxTableRows + 5
	now := time.Now().UTC()
	entries := make([]loginlog.Entry, n)
	for i := range n {
		entries[i] = loginlog.Entry{Time: now.Add(-time.Duration(i) * time.Second).Format(loginlog.TimeLayout), SP: "sp"}
	}
	m := newTuiModel(loginlog.Scanner{Table: "test"}, &filterFlags{})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	next, _ := m.applyForm(false)
	m = next.(tuiModel)
	m = update(t, m, scanDoneMsg{scope: m.pending.TimeRange(), entries: entries})
	path := filepath.Join(t.TempDir(), "all.csv")

	export(t, m, path)

	if got := len(readCSV(t, path)) - 1; got != n {
		t.Errorf("exported %d logins, want all %d (not just the %d displayed)", got, n, maxTableRows)
	}
}

func TestExport_SearchIsApplied(t *testing.T) {
	m := loadedModel(t)
	m.search.SetValue("hub-wyc")
	m.refreshTable()
	path := filepath.Join(t.TempDir(), "search.csv")

	export(t, m, path)

	if got := len(readCSV(t, path)) - 1; got != 1 {
		t.Errorf("exported %d logins, want the 1 matching the search", got)
	}
}

func TestExport_SummaryTabExportsGroups(t *testing.T) {
	m := loadedModel(t)
	m = update(t, m, key("2")) // By SP
	path := filepath.Join(t.TempDir(), "by-sp.csv")

	m = export(t, m, path)

	records := readCSV(t, path)
	if len(records) != 3 || records[0][0] != "SP" || records[1][0] != "sp-a" || records[1][1] != "2" {
		t.Errorf("unexpected by-SP export: %v", records)
	}
	if !strings.Contains(m.status, "Exported 2 groups") {
		t.Errorf("status = %q", m.status)
	}
}

func TestExport_RefusesToOverwrite(t *testing.T) {
	m := loadedModel(t)
	path := filepath.Join(t.TempDir(), "existing.csv")
	if err := os.WriteFile(path, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	m = export(t, m, path)

	if !strings.Contains(m.status, "already exists") {
		t.Errorf("status = %q, want an already-exists error", m.status)
	}
	if !m.exportIn.Focused() || m.exportIn.Value() != path {
		t.Errorf("prompt should reopen with the name tried; focused=%v value=%q", m.exportIn.Focused(), m.exportIn.Value())
	}
	if b, _ := os.ReadFile(path); string(b) != "keep me" {
		t.Errorf("existing file was modified: %q", b)
	}
}

func TestExport_FailureLeavesNoPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.csv")

	_, err := writeNewFile(path, func(w io.Writer) (int, error) {
		io.WriteString(w, "Time (UTC),User\n2026-10-01 00:00:00,") // half a file
		return 1, errors.New("disk full")
	})

	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("err = %v, want the write error", err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("partial file was left behind (stat err: %v)", statErr)
	}
}

func TestExport_EmptyNameIsRejected(t *testing.T) {
	m := loadedModel(t)

	m = export(t, m, "   ")

	if !m.exportIn.Focused() || !strings.Contains(m.status, "Enter a file name") {
		t.Errorf("prompt should stay open with an error; focused=%v status=%q", m.exportIn.Focused(), m.status)
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	tests := map[string]string{
		"~":          home,
		"~/x.csv":    filepath.Join(home, "x.csv"),
		"x.csv":      "x.csv",
		"/tmp/x.csv": "/tmp/x.csv",
		"~bob/x.csv": "~bob/x.csv",
		"~notes.csv": "~notes.csv",
	}
	for in, want := range tests {
		if got := expandHome(in); got != want {
			t.Errorf("expandHome(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDefaultExportName(t *testing.T) {
	at := time.Date(2026, 11, 3, 5, 23, 1, 0, time.UTC)
	if got := defaultExportName("", at); got != "logins-list-20261103-052301.csv" {
		t.Errorf("logins tab: %q", got)
	}
	if got := defaultExportName(loginlog.BySP, at); got != "logins-by-sp-20261103-052301.csv" {
		t.Errorf("by-sp tab: %q", got)
	}
}
