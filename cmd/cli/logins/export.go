package logins

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/sil-org/idp-cli/internal/loginlog"
)

// exportDoneMsg reports the result of a CSV export started from the TUI.
type exportDoneMsg struct {
	path string
	rows int
	what string // "logins" or "groups"
	err  error
}

// defaultExportName suggests a file name for exporting the given tab.
func defaultExportName(by loginlog.GroupBy, now time.Time) string {
	kind := "list"
	if by != "" {
		kind = "by-" + string(by)
	}
	return fmt.Sprintf("logins-%s-%s.csv", kind, now.UTC().Format("20060102-150405"))
}

// exportCmd snapshots what the current tab shows and returns a command that
// writes it to path as CSV, in the same format as `logins list/summary
// --format csv`. The Logins tab exports every login in the view (filters,
// drill-downs and search applied), not just the rows the table displays.
func (m tuiModel) exportCmd(path string) tea.Cmd {
	by := tabs[m.tab]
	needle := m.search.Value()

	var (
		write func(io.Writer) (int, error)
		what  string
	)
	if by == "" {
		// entries and view are never modified in place once loaded (recompute
		// builds a new view slice), so the background write can read them.
		entries, view := m.entries, m.view
		what = "logins"
		write = func(w io.Writer) (int, error) {
			cw := csv.NewWriter(w)
			if err := cw.Write(entryHeader); err != nil {
				return 0, err
			}
			n := 0
			for _, i := range view {
				row := entryRow(entries[i])
				if !matchesSearch(row, needle) {
					continue
				}
				if err := cw.Write(row); err != nil {
					return n, err
				}
				n++
			}
			cw.Flush()
			return n, cw.Error()
		}
	} else {
		groups := m.rowsG // already search-filtered, and never capped
		what = "groups"
		write = func(w io.Writer) (int, error) {
			return len(groups), writeCSV(w, groupHeader(by), groups, groupRow)
		}
	}

	return func() tea.Msg {
		rows, err := writeNewFile(path, write)
		return exportDoneMsg{path: path, rows: rows, what: what, err: err}
	}
}

// writeNewFile creates path -- refusing to overwrite an existing file, and
// readable only by the current user, since exports contain user identifiers
// -- and fills it with write. A failed write removes the partial file.
func writeNewFile(path string, write func(io.Writer) (int, error)) (int, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return 0, fmt.Errorf("%s already exists; choose another name", path)
	}
	if err != nil {
		return 0, err
	}

	n, err := write(f)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(path)
		return 0, err
	}
	return n, nil
}

// updateExportPrompt handles keys while the export file name prompt is open.
func (m tuiModel) updateExportPrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			m.exportIn.Blur()
			m.status = ""
			return m, nil
		case "enter":
			path := expandHome(strings.TrimSpace(m.exportIn.Value()))
			if path == "" {
				m.status = errStyle.Render("Enter a file name, or esc to cancel.")
				return m, nil
			}
			m.exportIn.Blur()
			m.status = mutedStyle.Render("Exporting to " + path + "…")
			return m, m.exportCmd(path)
		}
	}

	var cmd tea.Cmd
	m.exportIn, cmd = m.exportIn.Update(msg)
	return m, cmd
}

// startExportPrompt opens the file name prompt with a suggested name.
func (m *tuiModel) startExportPrompt() tea.Cmd {
	m.exportIn.SetValue(defaultExportName(tabs[m.tab], time.Now()))
	m.exportIn.CursorEnd()
	m.status = ""
	return m.exportIn.Focus()
}

func exportStatus(msg exportDoneMsg) string {
	if msg.err != nil {
		return errStyle.Render("Export failed: " + msg.err.Error())
	}
	return crumbStyle.Render(fmt.Sprintf("Exported %s %s to %s", commas(int64(msg.rows)), msg.what, msg.path))
}

// expandHome expands a leading "~/" (or a bare "~"), which a shell would do
// but os.OpenFile doesn't.
func expandHome(path string) string {
	rest, ok := strings.CutPrefix(path, "~")
	if !ok || (rest != "" && !strings.HasPrefix(rest, "/") && !strings.HasPrefix(rest, string(filepath.Separator))) {
		return path // not "~" or "~/..." (e.g. "~bob/x" or "~file.csv")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, rest)
}
