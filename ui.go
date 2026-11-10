package main

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type filterMode int

const (
	filterAll filterMode = iota
	filterActive
	filterFailed
)

func (f filterMode) String() string {
	return [...]string{"all", "active", "failed"}[f]
}

type autoMode int

const (
	autoOff autoMode = iota
	autoMine
	autoAll
)

func (a autoMode) String() string {
	return [...]string{"off", "mine", "all"}[a]
}

func parseAutoMode(s string) (autoMode, bool) {
	switch s {
	case "off", "":
		return autoOff, true
	case "mine":
		return autoMine, true
	case "all":
		return autoAll, true
	}
	return autoOff, false
}

type runsMsg struct {
	me    string
	runs  []Run
	extra []Run // watched runs that fell outside the per-repo window
	repos int
	warn  string
	err   error
}

type tickMsg struct{}

type model struct {
	cfg    config
	client *Client

	runs    []Run
	visible []Run
	repos   int
	cursor  int
	offset  int

	width, height int

	loading    bool
	err        error
	warn       string
	flash      string
	lastUpdate time.Time

	// watched holds runs to notify about when they finish, keyed by run ID.
	watched map[int64]Run
	// finished summarises watched runs that ended since the last keypress; it
	// keeps the tab showing done/error until you come back and press a key.
	finished     []string
	finishedFail bool
	lastStatus   string

	// Autowatch: auto picks which new runs to watch, me is the login that
	// counts as "mine". known holds every run ID seen so far, so a run that
	// started and finished between two refreshes is still reported. skip holds
	// runs you unwatched by hand, so autowatch doesn't add them back.
	auto      autoMode
	autoSince time.Time
	me        string
	known     map[int64]bool
	loaded    bool
	skip      map[int64]bool

	filter    filterMode
	search    textinput.Model
	searching bool
	spinner   spinner.Model
}

func newModel(cfg config, client *Client) model {
	ti := textinput.New()
	ti.Prompt = "/"
	ti.Placeholder = "filter repo, workflow, title, ref"
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	auto, _ := parseAutoMode(cfg.Auto)
	return model{
		cfg: cfg, client: client, search: ti, spinner: sp, loading: true,
		watched: map[int64]Run{}, known: map[int64]bool{}, skip: map[int64]bool{},
		auto: auto, autoSince: time.Now(), me: cfg.User,
	}
}

func (m model) fetch() tea.Cmd {
	watched := make([]Run, 0, len(m.watched))
	for _, r := range m.watched {
		watched = append(watched, r)
	}
	needMe := m.me == ""
	return func() tea.Msg {
		var me string
		if needMe {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			me, _ = m.client.Me(ctx)
			cancel()
		}
		repos, err := discoverRepos(m.cfg, m.client)
		if err != nil {
			return runsMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		runs, warn := m.client.AllRuns(ctx, repos, m.cfg.PerRepo)
		seen := make(map[int64]bool, len(runs))
		for _, r := range runs {
			seen[r.ID] = true
		}
		var extra []Run
		for _, w := range watched {
			if !seen[w.ID] {
				if r, err := m.client.Run(ctx, w.Repo, w.ID); err == nil {
					extra = append(extra, r)
				}
			}
		}
		return runsMsg{me: me, runs: runs, extra: extra, repos: len(repos), warn: warn}
	}
}

func (m model) tick() tea.Cmd {
	if m.cfg.Interval <= 0 {
		return nil
	}
	return tea.Tick(m.cfg.Interval, func(time.Time) tea.Msg { return tickMsg{} })
}

// Spinner ticks double as a clock so elapsed times of running jobs update.
func (m model) Init() tea.Cmd {
	return tea.Batch(m.fetch(), m.spinner.Tick, m.tick())
}

func (m *model) applyFilter() {
	var selected int64
	if m.cursor < len(m.visible) {
		selected = m.visible[m.cursor].ID
	}
	q := strings.ToLower(strings.TrimSpace(m.search.Value()))
	m.visible = m.visible[:0:0]
	for _, r := range m.runs {
		switch m.filter {
		case filterActive:
			if !r.Active() {
				continue
			}
		case filterFailed:
			if r.Status != "failure" {
				continue
			}
		}
		if q != "" {
			hay := strings.ToLower(strings.Join([]string{r.Repo, r.WorkflowID, r.Title, r.Ref, r.Event}, " "))
			if !strings.Contains(hay, q) {
				continue
			}
		}
		m.visible = append(m.visible, r)
	}
	m.cursor = 0
	for i, r := range m.visible {
		if r.ID == selected {
			m.cursor = i
			break
		}
	}
	m.clamp()
}

func (m *model) listHeight() int {
	// header + column header + footer
	h := m.height - 3
	if h < 1 {
		h = 1
	}
	return h
}

func (m *model) clamp() {
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	h := m.listHeight()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+h {
		m.offset = m.cursor - h + 1
	}
	if m.offset < 0 {
		m.offset = 0
	}
}

func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	return cmd.Start()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clamp()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tickMsg:
		if m.loading {
			return m, m.tick()
		}
		m.loading = true
		return m, tea.Batch(m.fetch(), m.tick())

	case runsMsg:
		m.loading = false
		m.err = msg.err
		if msg.err == nil {
			m.runs, m.repos, m.warn = msg.runs, msg.repos, msg.warn
			m.lastUpdate = time.Now()
			if msg.me != "" {
				m.me = msg.me
			}
			m.autoWatch(msg.runs)
			m.checkWatched(append(msg.runs, msg.extra...))
			m.applyFilter()
			m.reportStatus()
		}
		return m, nil

	case tea.KeyMsg:
		if m.searching {
			switch msg.String() {
			case "enter":
				m.searching = false
				m.search.Blur()
				return m, nil
			case "esc":
				m.searching = false
				m.search.Blur()
				m.search.SetValue("")
				m.applyFilter()
				return m, nil
			}
			var cmd tea.Cmd
			m.search, cmd = m.search.Update(msg)
			m.applyFilter()
			return m, cmd
		}

		m.flash = ""
		// Any key acknowledges finished runs, like returning to a Claude tab.
		if len(m.finished) > 0 {
			m.finished, m.finishedFail = nil, false
			defer m.reportStatus()
		}
		switch msg.String() {
		case "q", "ctrl+c":
			return m, tea.Quit
		case "esc":
			if m.search.Value() != "" {
				m.search.SetValue("")
				m.applyFilter()
			}
		case "up", "k":
			m.cursor--
		case "down", "j":
			m.cursor++
		case "pgup", "ctrl+u":
			m.cursor -= m.listHeight()
		case "pgdown", "ctrl+d":
			m.cursor += m.listHeight()
		case "home", "g":
			m.cursor = 0
		case "end", "G":
			m.cursor = len(m.visible) - 1
		case "r":
			if !m.loading {
				m.loading = true
				return m, m.fetch()
			}
		case "f", "tab":
			m.filter = (m.filter + 1) % 3
			m.applyFilter()
		case "w":
			if r, ok := m.selected(); ok {
				m.toggleWatch(r)
				m.reportStatus()
			}
		case "W":
			m.auto = (m.auto + 1) % 3
			m.autoSince = time.Now()
			m.autoWatch(m.runs)
			m.reportStatus()
			switch {
			case m.auto == autoOff:
				m.flash = "autowatch off"
			case m.auto == autoMine && m.me == "":
				m.flash = "autowatch: mine, but your login isn't known yet; pass -user"
			case m.auto == autoMine:
				m.flash = "autowatch: runs triggered by " + m.me
			default:
				m.flash = "autowatch: all runs"
			}
		case "/":
			m.searching = true
			return m, m.search.Focus()
		case "enter", "o":
			if r, ok := m.selected(); ok {
				m.open(r.HTMLURL)
			}
		case "a":
			if r, ok := m.selected(); ok {
				m.open(m.client.base + "/" + r.Repo + "/actions")
			}
		case "p":
			if r, ok := m.selected(); ok {
				m.open(m.client.base + "/" + r.Repo)
			}
		}
		m.clamp()
	}
	return m, nil
}

func runName(r Run) string {
	repo := r.Repo
	if i := strings.LastIndex(repo, "/"); i >= 0 {
		repo = repo[i+1:]
	}
	wf := strings.TrimSuffix(strings.TrimSuffix(r.WorkflowID, ".yml"), ".yaml")
	return fmt.Sprintf("%s %s #%d", repo, wf, r.Index)
}

func outcome(status string) string {
	switch status {
	case "success":
		return "passed"
	case "failure":
		return "failed"
	}
	return status
}

func (m *model) toggleWatch(r Run) {
	if _, ok := m.watched[r.ID]; ok {
		delete(m.watched, r.ID)
		m.skip[r.ID] = true
		m.flash = "stopped watching " + runName(r)
		return
	}
	if !r.Active() {
		m.flash = runName(r) + " already " + outcome(r.Status)
		return
	}
	delete(m.skip, r.ID)
	m.watched[r.ID] = r
	m.flash = "watching " + runName(r) + ", you'll be notified when it finishes"
}

func (m *model) autoMatches(r Run) bool {
	switch m.auto {
	case autoAll:
		return true
	case autoMine:
		return m.me != "" && strings.EqualFold(r.TriggeredBy(), m.me)
	}
	return false
}

// autoWatch adds matching runs to the watch list. Active runs are watched
// until they finish. A run seen for the first time already finished (it
// started and ended between refreshes) is added too, so checkWatched reports
// it right away.
func (m *model) autoWatch(runs []Run) {
	for _, r := range runs {
		_, watched := m.watched[r.ID]
		if m.auto != autoOff && !watched && !m.skip[r.ID] && m.autoMatches(r) {
			isNew := m.loaded && !m.known[r.ID] && !r.Created.Before(m.autoSince)
			if r.Active() || isNew {
				m.watched[r.ID] = r
			}
		}
		m.known[r.ID] = true
	}
	m.loaded = true
}

// checkWatched notifies about watched runs that have finished and stops
// watching them. Several runs finishing in one refresh give one notification.
func (m *model) checkWatched(runs []Run) {
	var ended []Run
	for _, r := range runs {
		if _, ok := m.watched[r.ID]; !ok {
			continue
		}
		if r.Active() {
			m.watched[r.ID] = r
			continue
		}
		delete(m.watched, r.ID)
		ended = append(ended, r)
	}
	if len(ended) == 0 {
		return
	}

	var done []string
	failed := 0
	for _, r := range ended {
		done = append(done, runName(r)+" "+outcome(r.Status))
		if r.Status != "success" {
			failed++
		}
	}
	m.finished = append(m.finished, done...)
	m.finishedFail = m.finishedFail || failed > 0
	m.flash = strings.Join(done, ", ")

	var title, body string
	if len(ended) == 1 {
		r := ended[0]
		icon, _ := statusIcon(r.Status)
		title = fmt.Sprintf("%s %s %s", icon, runName(r), outcome(r.Status))
		body = r.Title
		if r.Ref != "" {
			body += " (" + r.Ref + ")"
		}
	} else {
		icon := "✓"
		title = fmt.Sprintf("%d runs finished", len(ended))
		if failed > 0 {
			icon = "✗"
			title += fmt.Sprintf(", %d failed", failed)
		}
		title = icon + " " + title
		body = strings.Join(done, ", ")
	}
	sendNotification(m.cfg.Notify, title, body)
}

// summarize joins names, listing at most three.
func summarize(names []string) string {
	if len(names) <= 3 {
		return strings.Join(names, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(names[:3], ", "), len(names)-3)
}

// reportStatus mirrors the watch state onto the terminal tab (OSC 7501),
// sending only when it changes.
func (m *model) reportStatus() {
	if !statusSupported(m.cfg.Status) {
		return
	}
	state, msg := statusClear, ""
	switch {
	case len(m.watched) > 0:
		state = statusWorking
		var names []string
		for _, r := range m.watched {
			names = append(names, runName(r)+" "+r.Status)
		}
		sort.Strings(names)
		msg = "watching " + summarize(names)
		if len(m.finished) > 0 {
			msg += "; " + summarize(m.finished)
		}
	case len(m.finished) > 0:
		state = statusDone
		if m.finishedFail {
			state = statusError
		}
		msg = summarize(m.finished)
	}
	key := state + "\x00" + msg
	if key == m.lastStatus {
		return
	}
	m.lastStatus = key
	sendProgramStatus(state, msg)
}

func (m *model) open(u string) {
	if err := openURL(u); err != nil {
		m.flash = "open failed: " + err.Error()
	} else {
		m.flash = "opened " + u
	}
}

func (m model) selected() (Run, bool) {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return Run{}, false
	}
	return m.visible[m.cursor], true
}

// --- rendering ---

var (
	styleHeader  = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleColHead = lipgloss.NewStyle().Foreground(lipgloss.Color("8")).Bold(true)
	styleSel     = lipgloss.NewStyle().Background(lipgloss.Color("236")).Bold(true)
	styleErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
	styleWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleKey     = lipgloss.NewStyle().Foreground(lipgloss.Color("12"))
	styleWatch   = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
)

func statusIcon(status string) (string, lipgloss.Color) {
	switch status {
	case "success":
		return "✓", "10"
	case "failure":
		return "✗", "9"
	case "running":
		return "●", "11"
	case "waiting":
		return "◷", "12"
	case "blocked":
		return "■", "13"
	case "cancelled":
		return "⊘", "8"
	case "skipped":
		return "»", "8"
	}
	return "?", "8"
}

func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	s = runewidth.Truncate(s, w, "…")
	return runewidth.FillRight(s, w)
}

func fmtDur(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func fmtAge(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

type column struct {
	title string
	width int
	value func(Run) string
}

func (m model) columns() []column {
	cols := []column{
		{"REPO", 24, func(r Run) string { return r.Repo }},
		{"WORKFLOW", 18, func(r Run) string { return strings.TrimSuffix(strings.TrimSuffix(r.WorkflowID, ".yml"), ".yaml") }},
		{"#", 6, func(r Run) string { return fmt.Sprint(r.Index) }},
		{"REF", 18, func(r Run) string { return r.Ref }},
		{"EVENT", 13, func(r Run) string { return r.Event }},
		{"AGE", 8, func(r Run) string { return fmtAge(r.Created.Time) }},
		{"TIME", 7, func(r Run) string { return fmtDur(r.Elapsed()) }},
	}
	// Status icon and watch marker (3) plus single spaces between columns.
	used := 3
	for _, c := range cols {
		used += c.width + 1
	}
	title := m.width - used
	if title < 10 {
		// Narrow terminal: drop columns from the right until the title fits.
		for len(cols) > 2 && title < 10 {
			used -= cols[len(cols)-1].width + 1
			cols = cols[:len(cols)-1]
			title = m.width - used
		}
	}
	return append(cols, column{"TITLE", title, func(r Run) string { return r.Title }})
}

func (m model) View() string {
	if m.width == 0 {
		return ""
	}
	var b strings.Builder

	// Header
	var running, failed int
	for _, r := range m.runs {
		if r.Active() {
			running++
		}
		if r.Status == "failure" {
			failed++
		}
	}
	left := styleHeader.Render("Forgejo Actions") + styleDim.Render("  "+m.cfg.URL)
	right := fmt.Sprintf("filter:%s  %d repos  %s  %s",
		styleKey.Render(m.filter.String()), m.repos,
		styleWarn.Render(fmt.Sprintf("%d active", running)),
		styleErr.Render(fmt.Sprintf("%d failed", failed)))
	if n := len(m.watched); n > 0 {
		right = styleWatch.Render(fmt.Sprintf("◉ %d watched", n)) + "  " + right
	}
	if m.auto != autoOff {
		right = styleWatch.Render("auto:"+m.auto.String()) + "  " + right
	}
	if m.loading {
		right = m.spinner.View() + " " + right
	} else if !m.lastUpdate.IsZero() {
		right += styleDim.Render("  " + m.lastUpdate.Format("15:04:05"))
	}
	if lipgloss.Width(left)+lipgloss.Width(right)+1 > m.width {
		left = styleHeader.Render("Actions")
	}
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	b.WriteString(left + strings.Repeat(" ", gap) + right + "\n")

	cols := m.columns()
	head := "   "
	for _, c := range cols {
		head += fit(c.title, c.width) + " "
	}
	b.WriteString(styleColHead.Render(strings.TrimRight(head, " ")) + "\n")

	// Rows
	h := m.listHeight()
	lines := 0
	switch {
	case m.err != nil && len(m.runs) == 0:
		b.WriteString(styleErr.Render("error: "+m.err.Error()) + "\n")
		lines++
	case len(m.visible) == 0 && !m.loading:
		b.WriteString(styleDim.Render("no runs match") + "\n")
		lines++
	}
	for i := m.offset; i < len(m.visible) && lines < h; i++ {
		r := m.visible[i]
		icon, color := statusIcon(r.Status)
		if r.Status == "running" {
			icon = strings.TrimSpace(m.spinner.View())
		}
		var cells []string
		for _, c := range cols {
			cells = append(cells, fit(c.value(r), c.width))
		}
		text := strings.Join(cells, " ")
		iconStyle := lipgloss.NewStyle().Foreground(color)
		mark := "  "
		if _, ok := m.watched[r.ID]; ok {
			mark = "◉ "
		}
		if i == m.cursor {
			b.WriteString(iconStyle.Inherit(styleSel).Render(icon) + styleWatch.Inherit(styleSel).Render(mark) + styleSel.Render(text))
		} else {
			b.WriteString(iconStyle.Render(icon) + styleWatch.Render(mark) + text)
		}
		b.WriteString("\n")
		lines++
	}
	for ; lines < h; lines++ {
		b.WriteString("\n")
	}

	// Footer
	switch {
	case m.searching:
		b.WriteString(m.search.View())
	case m.flash != "":
		b.WriteString(styleDim.Render(runewidth.Truncate(m.flash, m.width, "…")))
	case m.err != nil:
		b.WriteString(styleErr.Render(runewidth.Truncate("error: "+m.err.Error(), m.width, "…")))
	default:
		help := "↑↓ move  enter open run  w watch  W autowatch  a repo actions  p repo  f filter  / search  r refresh  q quit"
		if v := m.search.Value(); v != "" {
			help = "search: " + v + " (esc clears)  " + help
		}
		if m.warn != "" {
			help = m.warn + "  " + help
		}
		b.WriteString(styleDim.Render(runewidth.Truncate(help, m.width, "…")))
	}
	return b.String()
}
