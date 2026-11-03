package logins

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"

	"github.com/sil-org/idp-cli/internal/loginlog"
)

func initTuiCmd(parent *cobra.Command, ff *filterFlags) {
	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Interactively browse and summarize the login log",
		Long: `Opens an interactive browser for the login log. Any filter flags pre-fill the
filter form. After a scan, switch between the raw logins and summaries by SP,
Hub, IdP, day, and user; press Enter on a summary row to drill into it.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			scanner, err := newScanner(cmd.Context())
			if err != nil {
				return err
			}
			_, err = tea.NewProgram(newTuiModel(scanner, ff), tea.WithAltScreen()).Run()
			return err
		},
	}
	parent.AddCommand(cmd)
}

// ---------------------------------------------------------------------------
// Styles

var (
	accent      = lipgloss.AdaptiveColor{Light: "#1d4ed8", Dark: "#7aa2f7"}
	muted       = lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#7f849c"}
	danger      = lipgloss.AdaptiveColor{Light: "#b91c1c", Dark: "#f7768e"}
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(accent)
	mutedStyle  = lipgloss.NewStyle().Foreground(muted)
	errStyle    = lipgloss.NewStyle().Foreground(danger).Bold(true)
	labelStyle  = lipgloss.NewStyle().Width(12).Foreground(muted)
	focusLabel  = labelStyle.Foreground(accent).Bold(true)
	tabStyle    = lipgloss.NewStyle().Padding(0, 1).Foreground(muted)
	activeTab   = lipgloss.NewStyle().Padding(0, 1).Bold(true).Foreground(accent).Underline(true)
	crumbStyle  = lipgloss.NewStyle().Foreground(accent)
	detailBox   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(accent).Padding(1, 2)
	detailLabel = lipgloss.NewStyle().Width(24).Foreground(muted)
)

// ---------------------------------------------------------------------------
// Model

type screen int

const (
	screenForm screen = iota
	screenScanning
	screenResults
	screenDetail
)

// tab 0 is the raw logins list; the rest are summaries.
var tabs = append([]loginlog.GroupBy{""}, loginlog.GroupBys...)

func tabTitle(g loginlog.GroupBy) string {
	switch g {
	case "":
		return "Logins"
	case loginlog.BySP:
		return "By SP"
	case loginlog.ByHub:
		return "By Hub"
	case loginlog.ByIDP:
		return "By IdP"
	case loginlog.ByDay:
		return "By Day"
	case loginlog.ByUser:
		return "By User"
	}
	return string(g)
}

// drill is an in-memory narrowing applied by pressing Enter on a summary row.
type drill struct {
	by  loginlog.GroupBy
	key string
}

const (
	fieldSince = iota
	fieldUntil
	fieldIDP
	fieldSP
	fieldHub
	fieldUser
	fieldCount
)

var fieldLabels = [fieldCount]string{"Since", "Until", "IdP entity", "SP", "Hub", "User"}

type tuiModel struct {
	scanner loginlog.Scanner
	width   int
	height  int
	screen  screen

	// form
	inputs  [fieldCount]textinput.Model
	focused int
	formErr string

	// scanning
	spinner  spinner.Model
	progress *loginlog.Progress
	cancel   context.CancelFunc
	started  time.Time

	// results
	loaded   loginlog.Filter  // time range the loaded entries were scanned with
	pending  loginlog.Filter  // filter to activate when the running scan finishes
	filter   loginlog.Filter  // active filter, applied in memory to entries
	entries  []loginlog.Entry // everything the scan returned, newest first
	view     []int32          // indices into entries, narrowed by filter and drills (cached)
	totals   loginlog.Totals  // totals for view (cached)
	elapsed  time.Duration
	tab      int
	drills   []drill
	search   textinput.Model
	exportIn textinput.Model // export file name prompt
	status   string          // one-off status message (e.g. export result), cleared by the next key
	table    table.Model
	rowsE    []int32          // indices into entries behind the current table rows (Logins tab)
	rowsG    []loginlog.Group // groups behind the current table rows (summary tabs)
	hidden   int              // matching logins beyond maxTableRows, not shown in the Logins tab
	selected loginlog.Entry
}

func newTuiModel(scanner loginlog.Scanner, ff *filterFlags) tuiModel {
	m := tuiModel{scanner: scanner}

	initial := [fieldCount]string{ff.since, ff.until, ff.idpEntity, ff.sp, ff.hub, ff.user}
	placeholders := [fieldCount]string{
		"e.g. 24h, 7d, 2026-10-01 (UTC)",
		"blank = now; a bare date includes the whole day",
		"exact IdP entity ID",
		"exact SP entity ID",
		`exact Hub entity ID, or "direct"`,
		"exact EmployeeNumber, ePPN, or CN",
	}
	for i := range fieldCount {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = placeholders[i]
		in.SetValue(initial[i])
		in.CharLimit = 256
		m.inputs[i] = in
	}
	if m.inputs[fieldSince].Value() == "" {
		m.inputs[fieldSince].SetValue("7d")
	}
	m.inputs[0].Focus()

	m.spinner = spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(titleStyle))

	m.exportIn = textinput.New()
	m.exportIn.Prompt = "Export CSV to: "
	m.exportIn.CharLimit = 1024

	m.search = textinput.New()
	m.search.Prompt = "/"
	m.search.Placeholder = "search visible rows"

	m.table = table.New(table.WithFocused(true))
	styles := table.DefaultStyles()
	styles.Header = styles.Header.BorderStyle(lipgloss.NormalBorder()).BorderBottom(true).BorderForeground(muted).Bold(true)
	styles.Selected = styles.Selected.Foreground(lipgloss.Color("#ffffff")).Background(accent).Bold(false)
	m.table.SetStyles(styles)
	return m
}

func (m tuiModel) Init() tea.Cmd {
	return textinput.Blink
}

// ---------------------------------------------------------------------------
// Messages

type scanDoneMsg struct {
	scope   loginlog.Filter
	entries []loginlog.Entry
	err     error
	elapsed time.Duration
}

type progressTickMsg struct{}

func progressTick() tea.Cmd {
	return tea.Tick(200*time.Millisecond, func(time.Time) tea.Msg { return progressTickMsg{} })
}

// ---------------------------------------------------------------------------
// Update

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.refreshTable()
		return m, nil

	case exportDoneMsg:
		m.status = exportStatus(msg)
		if msg.err != nil && m.screen == screenResults {
			// Reopen the prompt with the name tried, so it can be corrected.
			m.exportIn.SetValue(msg.path)
			m.exportIn.CursorEnd()
			cmd := m.exportIn.Focus()
			return m, cmd
		}
		return m, nil

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			if m.cancel != nil {
				m.cancel()
			}
			return m, tea.Quit
		}
	}

	switch m.screen {
	case screenForm:
		return m.updateForm(msg)
	case screenScanning:
		return m.updateScanning(msg)
	case screenDetail:
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "q":
				return m, tea.Quit
			case "esc", "enter", "backspace":
				m.screen = screenResults
			}
		}
		return m, nil
	default:
		return m.updateResults(msg)
	}
}

func (m tuiModel) updateForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "esc":
			if m.entries != nil {
				m.screen = screenResults
				return m, nil
			}
			return m, tea.Quit
		case "tab", "down":
			cmd := m.focusField((m.focused + 1) % fieldCount)
			return m, cmd
		case "shift+tab", "up":
			cmd := m.focusField((m.focused + fieldCount - 1) % fieldCount)
			return m, cmd
		case "enter":
			return m.applyForm(false)
		case "ctrl+r":
			return m.applyForm(true)
		}
	}

	var cmd tea.Cmd
	m.inputs[m.focused], cmd = m.inputs[m.focused].Update(msg)
	return m, cmd
}

// formFilter parses the form's inputs.
func (m tuiModel) formFilter() (loginlog.Filter, error) {
	ff := filterFlags{
		since:     m.inputs[fieldSince].Value(),
		until:     m.inputs[fieldUntil].Value(),
		idpEntity: strings.TrimSpace(m.inputs[fieldIDP].Value()),
		sp:        strings.TrimSpace(m.inputs[fieldSP].Value()),
		hub:       strings.TrimSpace(m.inputs[fieldHub].Value()),
		user:      strings.TrimSpace(m.inputs[fieldUser].Value()),
	}
	return ff.filter(time.Now())
}

// canApplyInMemory reports whether f can be applied to the loaded entries
// without a rescan: only the time range determines what was loaded.
func (m tuiModel) canApplyInMemory(f loginlog.Filter) bool {
	return m.entries != nil && m.loaded.CoversTimeRange(f)
}

// applyForm activates the form's filter -- instantly, in memory, if the
// loaded data covers its time range, or else (or when forced) via a rescan.
func (m tuiModel) applyForm(forceRescan bool) (tea.Model, tea.Cmd) {
	filter, err := m.formFilter()
	if err != nil {
		m.formErr = err.Error()
		return m, nil
	}
	m.formErr = ""

	if !forceRescan && m.canApplyInMemory(filter) {
		m.inputs[m.focused].Blur()
		m.filter = filter
		m.screen = screenResults
		m.recompute()
		m.table.GotoTop()
		return m, nil
	}
	return m.startScan(filter)
}

func (m *tuiModel) focusField(i int) tea.Cmd {
	m.inputs[m.focused].Blur()
	m.focused = i
	return m.inputs[i].Focus()
}

// startScan loads the entries in filter's time range. The rest of filter is
// applied in memory once the scan finishes, so it can change without a
// rescan. (Filtering server-side wouldn't save read capacity anyway: a Scan
// always reads the whole table.)
func (m tuiModel) startScan(filter loginlog.Filter) (tea.Model, tea.Cmd) {
	m.formErr = ""
	m.inputs[m.focused].Blur()
	m.pending = filter
	scope := filter.TimeRange()
	m.screen = screenScanning
	m.progress = &loginlog.Progress{}
	m.started = time.Now()

	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	scanner, progress, started := m.scanner, m.progress, m.started

	scan := func() tea.Msg {
		entries, err := scanner.Scan(ctx, scope, progress)
		return scanDoneMsg{scope: scope, entries: entries, err: err, elapsed: time.Since(started)}
	}
	return m, tea.Batch(scan, m.spinner.Tick, progressTick())
}

func (m tuiModel) updateScanning(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "esc" {
			m.cancel()
		}
		return m, nil

	case progressTickMsg:
		return m, progressTick()

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case scanDoneMsg:
		m.cancel()
		m.cancel = nil
		if msg.err != nil {
			if errors.Is(msg.err, context.Canceled) {
				m.formErr = "Scan canceled."
			} else {
				m.formErr = "Scan failed: " + msg.err.Error()
			}
			m.screen = screenForm
			cmd := m.inputs[m.focused].Focus()
			return m, cmd
		}

		loginlog.SortNewestFirst(msg.entries)
		m.entries = msg.entries
		m.loaded = msg.scope
		m.filter = m.pending
		m.elapsed = msg.elapsed
		m.drills = nil
		m.search.SetValue("")
		m.screen = screenResults
		m.recompute()
		m.table.GotoTop()
	}
	return m, nil
}

func (m tuiModel) updateResults(msg tea.Msg) (tea.Model, tea.Cmd) {
	// While the export prompt is open, it gets the keystrokes.
	if m.exportIn.Focused() {
		return m.updateExportPrompt(msg)
	}
	if _, ok := msg.(tea.KeyMsg); ok {
		m.status = ""
	}

	// While the search box is focused, it gets the keystrokes.
	if m.search.Focused() {
		if k, ok := msg.(tea.KeyMsg); ok {
			switch k.String() {
			case "enter":
				m.search.Blur()
				return m, nil
			case "esc":
				m.search.SetValue("")
				m.search.Blur()
				m.refreshTable()
				return m, nil
			}
		}
		var cmd tea.Cmd
		m.search, cmd = m.search.Update(msg)
		m.refreshTable()
		m.table.GotoTop()
		return m, cmd
	}

	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.String() {
		case "q":
			return m, tea.Quit
		case "f":
			m.screen = screenForm
			cmd := m.inputs[m.focused].Focus()
			return m, cmd
		case "r":
			return m.startScan(m.filter)
		case "/":
			cmd := m.search.Focus()
			return m, cmd
		case "e":
			cmd := m.startExportPrompt()
			return m, cmd
		case "right", "l", "tab":
			m.switchTab((m.tab + 1) % len(tabs))
			return m, nil
		case "left", "h", "shift+tab":
			m.switchTab((m.tab + len(tabs) - 1) % len(tabs))
			return m, nil
		case "1", "2", "3", "4", "5", "6":
			i, _ := strconv.Atoi(k.String())
			m.switchTab(i - 1)
			return m, nil
		case "esc":
			if m.search.Value() != "" {
				m.search.SetValue("")
				m.refreshTable()
				return m, nil
			}
			fallthrough
		case "backspace":
			if len(m.drills) > 0 {
				m.drills = m.drills[:len(m.drills)-1]
				m.recompute()
			}
			return m, nil
		case "enter":
			return m.enter(), nil
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m *tuiModel) switchTab(i int) {
	if i == m.tab || i < 0 || i >= len(tabs) {
		return
	}
	m.tab = i
	m.search.SetValue("")
	m.refreshTable()
	m.table.GotoTop()
}

// enter opens a login's detail, or drills into a summary group.
func (m tuiModel) enter() tea.Model {
	cursor := m.table.Cursor()
	if tabs[m.tab] == "" {
		if cursor >= 0 && cursor < len(m.rowsE) {
			m.selected = m.entries[m.rowsE[cursor]]
			m.screen = screenDetail
		}
		return m
	}

	if cursor >= 0 && cursor < len(m.rowsG) {
		m.drills = append(m.drills, drill{by: tabs[m.tab], key: m.rowsG[cursor].Key})
		m.tab = 0
		m.search.SetValue("")
		m.recompute()
		m.table.GotoTop()
	}
	return m
}

// recompute rebuilds the cached view (entries narrowed by the active filter
// and the drill-down stack) and its totals, then the table. Call it whenever
// entries, filter, or drills change.
func (m *tuiModel) recompute() {
	m.view = nil
	for i, e := range m.entries {
		if m.filter.Matches(e) && m.inDrills(e) {
			m.view = append(m.view, int32(i))
		}
	}
	m.totals = loginlog.TotalSeq(m.viewEntries())
	m.refreshTable()
}

// viewEntries iterates the entries in the current view.
func (m *tuiModel) viewEntries() iter.Seq[loginlog.Entry] {
	return func(yield func(loginlog.Entry) bool) {
		for _, i := range m.view {
			if !yield(m.entries[i]) {
				return
			}
		}
	}
}

func (m *tuiModel) inDrills(e loginlog.Entry) bool {
	for _, d := range m.drills {
		if d.by.Key(e) != d.key {
			return false
		}
	}
	return true
}

func matchesSearch(row []string, needle string) bool {
	if needle == "" {
		return true
	}
	needle = strings.ToLower(needle)
	return slices.ContainsFunc(row, func(cell string) bool {
		return strings.Contains(strings.ToLower(cell), needle)
	})
}

// refreshTable rebuilds the table's columns and rows for the current tab,
// drill-downs, search text, and window size.
func (m *tuiModel) refreshTable() {
	if m.width == 0 {
		return
	}
	needle := m.search.Value()
	m.rowsE, m.rowsG, m.hidden = nil, nil, 0

	var (
		cols []table.Column
		rows []table.Row
	)
	if by := tabs[m.tab]; by == "" {
		cols = entryColumns(m.width)
		for _, i := range m.view {
			row := entryRow(m.entries[i])
			if !matchesSearch(row, needle) {
				continue
			}
			// A table row per login costs memory and redraw time for rows
			// nobody will scroll to; show the newest, and count the rest.
			if len(rows) >= maxTableRows {
				m.hidden++
				continue
			}
			rows = append(rows, row)
			m.rowsE = append(m.rowsE, i)
		}
	} else {
		cols = groupColumns(m.width, by)
		for _, g := range loginlog.SummarizeSeq(m.viewEntries(), by) {
			if row := groupRow(g); matchesSearch(row, needle) {
				rows = append(rows, row)
				m.rowsG = append(m.rowsG, g)
			}
		}
	}

	// Clear rows first: the table renders rows against the current columns,
	// and a row shorter than the column list would index out of range.
	m.table.SetRows(nil)
	m.table.SetColumns(cols)
	m.table.SetRows(rows)
	m.table.SetWidth(m.width)
	m.table.SetHeight(max(m.height-chromeHeight, 3))
	if m.table.Cursor() >= len(rows) {
		m.table.SetCursor(max(len(rows)-1, 0))
	}
}

// maxTableRows caps the Logins tab's table rows. Totals and summaries still
// cover every matching login.
const maxTableRows = 10_000

// chromeHeight is the number of lines the results screen uses outside the table.
const chromeHeight = 9

// flexWidths splits the width left after fixed columns among flexible ones by weight.
func flexWidths(total, fixed int, weights ...int) []int {
	sum := 0
	for _, w := range weights {
		sum += w
	}
	avail := max(total-fixed, 10*len(weights))
	out := make([]int, len(weights))
	for i, w := range weights {
		out[i] = max(avail*w/sum, 8)
	}
	return out
}

func entryColumns(width int) []table.Column {
	// Each column gets 2 chars of cell padding from the default styles.
	const timeW, userW = 19, 12
	flex := flexWidths(width, timeW+userW+6*2, 2, 4, 3, 2)
	return []table.Column{
		{Title: entryHeader[0], Width: timeW},
		{Title: entryHeader[1], Width: userW},
		{Title: entryHeader[2], Width: flex[0]},
		{Title: entryHeader[3], Width: flex[1]},
		{Title: entryHeader[4], Width: flex[2]},
		{Title: entryHeader[5], Width: flex[3]},
	}
}

func groupColumns(width int, by loginlog.GroupBy) []table.Column {
	const numW, timeW = 8, 19
	h := groupHeader(by)
	h[0] = tabTitle(by)[3:] // "By SP" -> "SP"
	flex := flexWidths(width, 2*numW+2*timeW+5*2, 1)
	return []table.Column{
		{Title: h[0], Width: flex[0]},
		{Title: h[1], Width: numW},
		{Title: h[2], Width: numW},
		{Title: h[3], Width: timeW},
		{Title: h[4], Width: timeW},
	}
}

// ---------------------------------------------------------------------------
// View

func (m tuiModel) View() string {
	if m.width == 0 {
		return ""
	}
	header := titleStyle.Render("idp-cli logins") + mutedStyle.Render("  ·  "+m.scanner.Table)

	switch m.screen {
	case screenForm:
		return header + "\n\n" + m.viewForm()
	case screenScanning:
		return header + "\n\n" + m.viewScanning()
	case screenDetail:
		return header + "\n\n" + m.viewDetail()
	default:
		return header + "\n" + m.viewResults()
	}
}

func (m tuiModel) viewForm() string {
	var b strings.Builder
	b.WriteString("Filters " + mutedStyle.Render("(times are UTC; Since/Until choose which logins to load; the other filters narrow them instantly)") + "\n\n")
	for i := range fieldCount {
		label := labelStyle
		if i == m.focused {
			label = focusLabel
		}
		m.inputs[i].Width = max(m.width-16, 20)
		b.WriteString(label.Render(fieldLabels[i]) + "  " + m.inputs[i].View() + "\n")
	}
	if m.formErr != "" {
		b.WriteString("\n" + errStyle.Render(m.formErr) + "\n")
	}
	limit := "unlimited"
	if m.scanner.MaxRCU > 0 {
		limit = fmt.Sprintf("%g RCU/s", m.scanner.MaxRCU)
	}
	maxRecords := "no record limit"
	if m.scanner.MaxRecords > 0 {
		maxRecords = "up to " + commas(int64(m.scanner.MaxRecords)) + " logins"
	}
	b.WriteString("\n" + mutedStyle.Render(fmt.Sprintf("Scan: %d segments, %s, %s",
		max(m.scanner.Segments, 1), limit, maxRecords)) + "\n")

	enter := "enter run scan"
	if m.entries != nil {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("Loaded: %s logins (%s)",
			commas(int64(len(m.entries))), describeRange(m.loaded))) + "\n")
		if f, err := m.formFilter(); err == nil && m.canApplyInMemory(f) {
			b.WriteString(crumbStyle.Render("Enter applies instantly, from the loaded logins.") + "\n")
			enter = "enter apply · ctrl+r rescan anyway"
		} else if err == nil {
			b.WriteString(errStyle.Render("This time range goes beyond the loaded logins, so Enter will rescan.") + "\n")
			enter = "enter rescan"
		}
	}

	esc := "quit"
	if m.entries != nil {
		esc = "back to results"
	}
	b.WriteString("\n" + mutedStyle.Render("tab/↑↓ move · "+enter+" · esc "+esc+" · ctrl+c quit"))
	return b.String()
}

func (m tuiModel) viewScanning() string {
	elapsed := time.Since(m.started).Truncate(time.Second)
	return fmt.Sprintf("%s Scanning…  %s\n\n%s\n\n%s",
		m.spinner.View(),
		mutedStyle.Render(elapsed.String()),
		progressLine(m.progress),
		mutedStyle.Render("esc cancel · ctrl+c quit"),
	)
}

func (m tuiModel) viewResults() string {
	var tabBar []string
	for i, g := range tabs {
		label := fmt.Sprintf("%d %s", i+1, tabTitle(g))
		if i == m.tab {
			tabBar = append(tabBar, activeTab.Render(label))
		} else {
			tabBar = append(tabBar, tabStyle.Render(label))
		}
	}

	totals := m.totals
	stats := fmt.Sprintf("%s logins · %s users · %s → %s",
		commas(int64(totals.Logins)), commas(int64(totals.Users)), orDash(totals.First), orDash(totals.Last))
	loadedLine := fmt.Sprintf("loaded %s logins %s · scanned %s items, %s RCU in %s",
		commas(int64(len(m.entries))), describeRange(m.loaded), commas(m.progress.Scanned.Load()),
		commas(int64(m.progress.ConsumedRCU())), roundDuration(m.elapsed))

	crumbs := describeFilter(m.filter)
	for _, d := range m.drills {
		crumbs = append(crumbs, crumbStyle.Render(fmt.Sprintf("%s=%s", d.by, d.key)))
	}
	crumbLine := mutedStyle.Render("filters: ") + strings.Join(crumbs, mutedStyle.Render(" › "))

	searchLine := ""
	if m.exportIn.Focused() {
		hint := mutedStyle.Render("enter save · esc cancel")
		if m.status != "" {
			hint = m.status
		}
		searchLine = m.exportIn.View() + "  " + hint
	} else if m.status != "" {
		searchLine = m.status
	} else if m.search.Focused() || m.search.Value() != "" {
		searchLine = m.search.View()
	}
	if m.hidden > 0 && tabs[m.tab] == "" && !m.exportIn.Focused() && m.status == "" {
		note := fmt.Sprintf("showing the newest %s of %s logins; narrow the filters to see the rest, or use a summary tab",
			commas(int64(len(m.rowsE))), commas(int64(len(m.rowsE)+m.hidden)))
		searchLine = strings.TrimSpace(searchLine + "  " + mutedStyle.Render(note))
	}

	help := "←→/1-6 tabs · enter "
	if tabs[m.tab] == "" {
		help += "details"
	} else {
		help += "drill in"
	}
	if len(m.drills) > 0 {
		help += " · backspace drill out"
	}
	help += " · / search · e export · f filters · r rescan · q quit"

	return strings.Join([]string{
		lipgloss.JoinHorizontal(lipgloss.Top, tabBar...),
		mutedStyle.Render(stats),
		mutedStyle.Render(loadedLine),
		crumbLine,
		m.table.View(),
		searchLine,
		mutedStyle.Render(help),
	}, "\n")
}

// describeRange describes a scan's time range, e.g. "since 2026-10-27 05:58:02".
func describeRange(f loginlog.Filter) string {
	switch {
	case f.Since.IsZero() && f.Until.IsZero():
		return "all time"
	case f.Until.IsZero():
		return "since " + f.Since.UTC().Format(loginlog.TimeLayout)
	case f.Since.IsZero():
		return "until " + f.Until.UTC().Format(loginlog.TimeLayout)
	default:
		return f.Since.UTC().Format(loginlog.TimeLayout) + " to " + f.Until.UTC().Format(loginlog.TimeLayout)
	}
}

func describeFilter(f loginlog.Filter) []string {
	var parts []string
	if !f.Since.IsZero() {
		parts = append(parts, "since "+f.Since.UTC().Format(loginlog.TimeLayout))
	}
	if !f.Until.IsZero() {
		parts = append(parts, "until "+f.Until.UTC().Format(loginlog.TimeLayout))
	}
	for _, kv := range [][2]string{{"idp", f.IDP}, {"sp", f.SP}, {"hub", f.Hub}, {"user", f.User}} {
		if kv[1] != "" {
			parts = append(parts, kv[0]+"="+kv[1])
		}
	}
	if len(parts) == 0 {
		parts = append(parts, "none")
	}
	return parts
}

func (m tuiModel) viewDetail() string {
	e := m.selected
	fields := [][2]string{
		{"Time (UTC)", e.Time},
		{"EmployeeNumber", e.EmployeeNumber},
		{"EduPersonPrincipalName", e.EduPersonPrincipalName},
		{"CN", e.CN},
		{"SP", e.SP},
		{"Hub", e.HubOrDirect()},
		{"IdP", e.IDP},
		{"ID", e.ID},
	}
	if e.ExpiresAt > 0 {
		fields = append(fields, [2]string{"Expires (UTC)", time.Unix(e.ExpiresAt, 0).UTC().Format(loginlog.TimeLayout)})
	}

	var b strings.Builder
	for _, f := range fields {
		b.WriteString(detailLabel.Render(f[0]) + orDash(f[1]) + "\n")
	}
	return detailBox.Width(min(m.width-2, 100)).Render(strings.TrimRight(b.String(), "\n")) +
		"\n\n" + mutedStyle.Render("esc/enter back · q quit")
}

// roundDuration keeps sub-second scans readable ("240ms" rather than "0s").
func roundDuration(d time.Duration) time.Duration {
	if d < time.Second {
		return d.Round(time.Millisecond)
	}
	return d.Round(100 * time.Millisecond)
}
