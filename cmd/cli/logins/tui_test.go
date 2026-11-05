package logins

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sil-org/idp-cli/internal/loginlog"
)

// loadedModel returns a TUI model that has "scanned" entries for the last
// 7 days (the form's default Since), without touching DynamoDB.
func loadedModel(t *testing.T) tuiModel {
	t.Helper()
	ago := func(h int) string {
		return time.Now().UTC().Add(-time.Duration(h) * time.Hour).Format(loginlog.TimeLayout)
	}
	entries := []loginlog.Entry{
		{ID: "1", Time: ago(1), SP: "sp-a", Hub: "hub-sil", EmployeeNumber: "100"},
		{ID: "2", Time: ago(2), SP: "sp-a", EmployeeNumber: "200"},
		{ID: "3", Time: ago(50), SP: "sp-b", Hub: "hub-wyc", EmployeeNumber: "100"},
		{ID: "4", Time: ago(100), SP: "sp-b", EmployeeNumber: "300"},
	}

	m := newTuiModel(loginlog.Scanner{Table: "test"}, &filterFlags{})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})

	// Submit the form, then complete the scan with our entries.
	next, _ := m.applyForm(false)
	m = next.(tuiModel)
	if m.screen != screenScanning {
		t.Fatalf("first submit should scan, got screen %v", m.screen)
	}
	m.scanProgress.Scanned.Store(40)
	m = update(t, m, scanDoneMsg{scope: m.pending.TimeRange(), entries: entries, elapsed: time.Second})
	if m.screen != screenResults || len(m.view) != 4 {
		t.Fatalf("after scan: screen %v, %d in view; want results with 4", m.screen, len(m.view))
	}
	return m
}

func update(t *testing.T, m tuiModel, msg tea.Msg) tuiModel {
	t.Helper()
	next, _ := m.Update(msg)
	return next.(tuiModel)
}

func submit(t *testing.T, m tuiModel, values map[int]string) tuiModel {
	t.Helper()
	for field, v := range values {
		m.inputs[field].SetValue(v)
	}
	next, _ := m.applyForm(false)
	return next.(tuiModel)
}

func TestTui_NonTimeFiltersApplyWithoutRescan(t *testing.T) {
	m := loadedModel(t)

	m = submit(t, m, map[int]string{fieldSP: "sp-a"})
	if m.screen != screenResults {
		t.Fatalf("SP filter should apply in memory, got screen %v", m.screen)
	}
	if len(m.view) != 2 || m.totals.Logins != 2 {
		t.Errorf("SP filter: %d in view (%d logins), want 2", len(m.view), m.totals.Logins)
	}

	// Change it again, and clear it: still no rescan, all data still there.
	m = submit(t, m, map[int]string{fieldSP: "", fieldHub: loginlog.DirectHub})
	if m.screen != screenResults || len(m.view) != 2 {
		t.Errorf("hub=direct: screen %v, %d in view; want results with 2", m.screen, len(m.view))
	}
	m = submit(t, m, map[int]string{fieldHub: "", fieldUser: "100"})
	if m.screen != screenResults || len(m.view) != 2 {
		t.Errorf("user=100: screen %v, %d in view; want results with 2", m.screen, len(m.view))
	}
	m = submit(t, m, map[int]string{fieldUser: ""})
	if len(m.view) != 4 {
		t.Errorf("cleared filters: %d in view, want all 4", len(m.view))
	}
}

func TestTui_NarrowerTimeRangeAppliesWithoutRescan(t *testing.T) {
	m := loadedModel(t)

	m = submit(t, m, map[int]string{fieldSince: "24h"})
	if m.screen != screenResults {
		t.Fatalf("narrower Since should apply in memory, got screen %v", m.screen)
	}
	if len(m.view) != 2 {
		t.Errorf("since 24h: %d in view, want 2", len(m.view))
	}
}

func TestTui_WiderTimeRangeRescans(t *testing.T) {
	m := loadedModel(t)

	m = submit(t, m, map[int]string{fieldSince: "30d"})
	if m.screen != screenScanning {
		t.Fatalf("wider Since should rescan, got screen %v", m.screen)
	}
	// The loaded data and active filter are untouched until the scan finishes.
	if len(m.entries) != 4 || !m.filter.Since.After(time.Now().Add(-8*24*time.Hour)) {
		t.Errorf("rescan should not disturb loaded data before it completes")
	}
	if m.pending.SP != "" || m.pending.Since.IsZero() {
		t.Errorf("pending filter not recorded: %+v", m.pending)
	}
}

func TestTui_ForceRescan(t *testing.T) {
	m := loadedModel(t)

	next, _ := m.applyForm(true)
	if next.(tuiModel).screen != screenScanning {
		t.Errorf("ctrl+r should rescan even when the data is loaded")
	}
}

func TestTui_DrillsComposeWithFilters(t *testing.T) {
	m := loadedModel(t)
	m = submit(t, m, map[int]string{fieldUser: "100"})

	m.drills = []drill{{by: loginlog.BySP, key: "sp-b"}}
	m.recompute()
	if len(m.view) != 1 || m.entries[m.view[0]].ID != "3" {
		t.Errorf("user=100 › sp=sp-b: view = %+v, want only entry 3", m.view)
	}
}

func TestTui_ScanSendsOnlyTimeRange(t *testing.T) {
	m := newTuiModel(loginlog.Scanner{Table: "test"}, &filterFlags{sp: "sp-a", hub: "direct", user: "100"})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})

	next, _ := m.applyForm(false)
	m = next.(tuiModel)
	scope := m.pending.TimeRange()
	if scope.SP != "" || scope.Hub != "" || scope.User != "" || scope.Since.IsZero() {
		t.Errorf("scan scope should be time-only, got %+v", scope)
	}
	if m.pending.SP != "sp-a" || m.pending.Hub != "direct" || m.pending.User != "100" {
		t.Errorf("CLI-flag filters should be kept for in-memory use, got %+v", m.pending)
	}
}

func TestTui_LoginsTabCapsRowsButCountsEverything(t *testing.T) {
	n := maxTableRows + 250
	now := time.Now().UTC()
	entries := make([]loginlog.Entry, n)
	for i := range n {
		entries[i] = loginlog.Entry{
			ID:             fmt.Sprint(i),
			Time:           now.Add(-time.Duration(i) * time.Second).Format(loginlog.TimeLayout),
			SP:             "sp",
			EmployeeNumber: fmt.Sprint(i % 7),
		}
	}
	m := newTuiModel(loginlog.Scanner{Table: "test"}, &filterFlags{})
	m = update(t, m, tea.WindowSizeMsg{Width: 120, Height: 30})
	next, _ := m.applyForm(false)
	m = next.(tuiModel)
	m = update(t, m, scanDoneMsg{scope: m.pending.TimeRange(), entries: entries})

	if len(m.rowsE) != maxTableRows || m.hidden != 250 {
		t.Errorf("rows = %d, hidden = %d; want %d and 250", len(m.rowsE), m.hidden, maxTableRows)
	}
	if m.totals.Logins != n || m.totals.Users != 7 {
		t.Errorf("totals = %+v; want all %d logins by 7 users", m.totals, n)
	}
	if m.entries[m.rowsE[0]].ID != "0" {
		t.Errorf("first row should be the newest login")
	}
	if !strings.Contains(m.View(), "showing the newest 10,000 of 10,250 logins") {
		t.Errorf("expected a note about hidden rows in the view")
	}

	// Summary tabs still cover every login.
	m = update(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2")})
	if len(m.rowsG) != 1 || m.rowsG[0].Logins != n {
		t.Errorf("By SP = %+v; want one group with all %d logins", m.rowsG, n)
	}
}

func TestTui_FailedRescanKeepsPreviousStats(t *testing.T) {
	for name, err := range map[string]error{
		"canceled": context.Canceled,
		"failed":   errors.New("boom"),
	} {
		t.Run(name, func(t *testing.T) {
			m := loadedModel(t)
			loadedProgress, loadedElapsed := m.progress, m.elapsed

			m = submit(t, m, map[int]string{fieldSince: "30d"})
			m.scanProgress.Scanned.Store(999) // the aborted scan got partway
			m = update(t, m, scanDoneMsg{scope: m.pending.TimeRange(), err: err, elapsed: time.Minute})

			if m.progress != loadedProgress || m.elapsed != loadedElapsed {
				t.Errorf("a %s rescan replaced the loaded results' stats", name)
			}
			if got := m.progress.Scanned.Load(); got != 40 {
				t.Errorf("Scanned = %d, want the loaded scan's 40", got)
			}
		})
	}
}

func TestTui_TooManyRecordsExplainsTimeRangeScan(t *testing.T) {
	m := loadedModel(t)
	m = submit(t, m, map[int]string{fieldSince: "90d", fieldUser: "100"})
	m = update(t, m, scanDoneMsg{err: &loginlog.TooManyRecordsError{Limit: 1_000_000}})

	if m.screen != screenForm {
		t.Fatalf("screen = %v, want the form", m.screen)
	}
	for _, want := range []string{"more than 1000000 logins match", "Since/Until", "logins list"} {
		if !strings.Contains(m.formErr, want) {
			t.Errorf("formErr %q doesn't mention %q", m.formErr, want)
		}
	}
}
