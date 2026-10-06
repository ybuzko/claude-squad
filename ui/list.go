package ui

import (
	"claude-squad/log"
	"claude-squad/session"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

const readyIcon = "● "

// pausedIcon avoids ⏸ (U+23F8): it has an emoji form, and Windows Terminal draws it two
// columns wide while Go measures one, which wraps the row and garbles the list.
const pausedIcon = "‖ "
const blockedIcon = "! "
const doneIcon = "✓ "

var readyStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#51bd73", Dark: "#51bd73"})

var blockedStyle = lipgloss.NewStyle().
	Bold(true).
	Foreground(lipgloss.AdaptiveColor{Light: "#d9534f", Dark: "#ff6b6b"})

var doneStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"})

var blockedBadgeStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#ff6b6b")).
	Foreground(lipgloss.Color("#1a1a1a"))

var idleBadgeStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#51bd73")).
	Foreground(lipgloss.Color("#1a1a1a"))

var addedLinesStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#51bd73", Dark: "#51bd73"})

var removedLinesStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("#de613e"))

var pausedStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#888888", Dark: "#888888"})

var titleStyle = lipgloss.NewStyle().
	Padding(0, 1).
	Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#dddddd"})

var listDescStyle = lipgloss.NewStyle().
	Padding(0, 1).
	Foreground(lipgloss.AdaptiveColor{Light: "#A49FA5", Dark: "#777777"})

var selectedTitleStyle = lipgloss.NewStyle().
	Padding(0, 1).
	Background(lipgloss.Color("#dde4f0")).
	Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#1a1a1a"})

var selectedDescStyle = lipgloss.NewStyle().
	Padding(0, 1).
	Background(lipgloss.Color("#dde4f0")).
	Foreground(lipgloss.AdaptiveColor{Light: "#1a1a1a", Dark: "#1a1a1a"})

var mainTitle = lipgloss.NewStyle().
	Background(lipgloss.Color("62")).
	Foreground(lipgloss.Color("230"))

var scrollHintStyle = lipgloss.NewStyle().
	Foreground(lipgloss.AdaptiveColor{Light: "#A49FA5", Dark: "#777777"})

var autoYesStyle = lipgloss.NewStyle().
	Background(lipgloss.Color("#dde4f0")).
	Foreground(lipgloss.Color("#1a1a1a"))

type List struct {
	items       []*session.Instance
	selectedIdx int
	// offset is the index of the first item shown when the list is taller than its
	// viewport. String keeps the selected item inside the visible window.
	offset int
	// shownStart and shownEnd are the items the last String drew ([start, end)), and
	// shownTop the row the first of them starts on; ItemAtRow maps clicks with them.
	shownStart, shownEnd, shownTop int
	height, width                  int
	renderer                       *InstanceRenderer
	autoyes                        bool
	// linearMode enables the blocked/idle header counts and the urgency sort.
	linearMode bool
	// title is the header text; the archive list uses its own.
	title string

	// map of repo name to number of instances using it. Used to display the repo name only if there are
	// multiple repos in play.
	repos map[string]int
}

// SetLinearMode toggles the ticket-dispatcher presentation (header counts, urgency sort).
func (l *List) SetLinearMode(enabled bool) {
	l.linearMode = enabled
}

// statusRank orders instances by how urgently they need a human: blocked first, then
// idle (waiting for a prompt), then working, then paused, then done.
func statusRank(s session.Status) int {
	switch s {
	case session.Blocked:
		return 0
	case session.Ready:
		return 1
	case session.Running, session.Loading:
		return 2
	case session.Paused:
		return 3
	case session.Done:
		return 4
	}
	return 5
}

// SortByUrgency stably reorders the list by statusRank, keeping the current selection
// on the same instance. Returns true if the order changed.
func (l *List) SortByUrgency() bool {
	if len(l.items) < 2 {
		return false
	}
	selected := l.GetSelectedInstance()
	before := make([]*session.Instance, len(l.items))
	copy(before, l.items)
	sort.SliceStable(l.items, func(a, b int) bool {
		ia, ib := l.items[a], l.items[b]
		ra, rb := statusRank(ia.Status), statusRank(ib.Status)
		if ra != rb {
			return ra < rb
		}
		// Idle sessions: the one that most recently stopped working comes first.
		if ia.Status == session.Ready {
			return ia.StatusChangedAt.After(ib.StatusChangedAt)
		}
		return false
	})
	changed := false
	for i := range before {
		if before[i] != l.items[i] {
			changed = true
			break
		}
	}
	if changed && selected != nil {
		l.SelectInstance(selected)
	}
	return changed
}

// CountByStatus returns how many instances are blocked and how many are idle (ready).
func (l *List) CountByStatus() (blocked, idle int) {
	for _, item := range l.items {
		switch item.Status {
		case session.Blocked:
			blocked++
		case session.Ready:
			idle++
		}
	}
	return blocked, idle
}

// FindByTitle returns the instance with the given title, or nil.
func (l *List) FindByTitle(title string) *session.Instance {
	for _, item := range l.items {
		if item.Title == title {
			return item
		}
	}
	return nil
}

func NewList(spinner *spinner.Model, autoYes bool) *List {
	return &List{
		items:    []*session.Instance{},
		renderer: &InstanceRenderer{spinner: spinner},
		repos:    make(map[string]int),
		autoyes:  autoYes,
	}
}

// SetSize sets the height and width of the list.
func (l *List) SetSize(width, height int) {
	l.width = width
	l.height = height
	l.renderer.setWidth(width)
}

// SetSessionPreviewSize sets the height and width for the tmux sessions. This makes the stdout line have the correct
// width and height.
func (l *List) SetSessionPreviewSize(width, height int) (err error) {
	for i, item := range l.items {
		if !item.Started() || item.Paused() {
			continue
		}

		if innerErr := item.SetPreviewSize(width, height); innerErr != nil {
			err = errors.Join(
				err, fmt.Errorf("could not set preview size for instance %d: %v", i, innerErr))
		}
	}
	return
}

func (l *List) NumInstances() int {
	return len(l.items)
}

// InstanceRenderer handles rendering of session.Instance objects
type InstanceRenderer struct {
	spinner *spinner.Model
	width   int
}

func (r *InstanceRenderer) setWidth(width int) {
	r.width = AdjustPreviewWidth(width)
}

// statusGlyph is the icon shown after an instance's title for its status.
func (r *InstanceRenderer) statusGlyph(i *session.Instance, bg lipgloss.TerminalColor) string {
	var join string
	switch i.Status {
	case session.Running, session.Loading:
		join = fmt.Sprintf("%s ", r.spinner.View())
	case session.Ready:
		join = readyStyle.Background(bg).Render(readyIcon)
	case session.Paused:
		join = pausedStyle.Background(bg).Render(pausedIcon)
	case session.Blocked:
		join = blockedStyle.Background(bg).Render(blockedIcon)
	case session.Done:
		join = doneStyle.Background(bg).Render(doneIcon)
	default:
	}
	return join
}

// ɹ and ɻ are other options.
const branchIcon = "Ꮧ"

func (r *InstanceRenderer) Render(i *session.Instance, idx int, selected bool, hasMultipleRepos bool) string {
	prefix := fmt.Sprintf(" %d. ", idx)
	if idx >= 10 {
		prefix = prefix[:len(prefix)-1]
	}
	titleS := selectedTitleStyle
	descS := selectedDescStyle
	if !selected {
		titleS = titleStyle
		descS = listDescStyle
	}

	// add spinner next to title if it's running (or being restored from the archive)
	join := r.statusGlyph(i, titleS.GetBackground())
	if i.Restoring {
		join = fmt.Sprintf("%s ", r.spinner.View())
	}

	// First row: id, then the ticket's Linear state, then its due date; cut to fit.
	stateText := ""
	if i.IsTicket() && i.IssueState != "" {
		stateText = " · " + i.IssueState
	}
	plain := i.Title + stateText
	if due := dueLabel(i.IssueDueDate, time.Now()); due != "" {
		plain += " (" + due + ")"
	}
	widthAvail := r.width - 3 - runewidth.StringWidth(prefix) - 1
	if widthAvail > 0 && runewidth.StringWidth(plain) > widthAvail {
		plain = runewidth.Truncate(plain, widthAvail-3, "...")
	}
	// Color the state. Every piece carries the row's background explicitly: a styled
	// piece ends with a reset, which would otherwise drop the selection highlight for
	// the rest of the row.
	bg := titleS.GetBackground()
	base := lipgloss.NewStyle().Background(bg).Foreground(titleS.GetForeground())
	titleText := base.Render(prefix + " " + plain)
	if stateText != "" && len(plain) > len(i.Title) {
		end := min(len(i.Title)+len(stateText), len(plain))
		titleText = base.Render(prefix+" "+plain[:len(i.Title)]) +
			issueStateStyle(i.IssueState).Background(bg).Render(plain[len(i.Title):end]) +
			base.Render(plain[end:])
	}
	title := titleS.Render(lipgloss.JoinHorizontal(
		lipgloss.Left,
		lipgloss.Place(r.width-3, 1, lipgloss.Left, lipgloss.Center, titleText, lipgloss.WithWhitespaceBackground(bg)),
		base.Render(" "),
		join,
	))

	stat := i.GetDiffStats()

	var diff string
	var addedDiff, removedDiff string
	if stat == nil || stat.Error != nil || stat.IsEmpty() {
		// Don't show diff stats if there's an error or if they don't exist
		addedDiff = ""
		removedDiff = ""
		diff = ""
	} else {
		addedDiff = fmt.Sprintf("+%d", stat.Added)
		removedDiff = fmt.Sprintf("-%d ", stat.Removed)
		diff = lipgloss.JoinHorizontal(
			lipgloss.Center,
			addedLinesStyle.Background(descS.GetBackground()).Render(addedDiff),
			lipgloss.Style{}.Background(descS.GetBackground()).Foreground(descS.GetForeground()).Render(","),
			removedLinesStyle.Background(descS.GetBackground()).Render(removedDiff),
		)
	}

	// Ticket sessions show the ticket's title under the id; others show the branch.
	subtitle := i.Branch
	icon := branchIcon + "-"
	if i.IsTicket() && i.IssueTitle != "" {
		subtitle = i.IssueTitle
		icon = ""
	}

	remainingWidth := r.width
	remainingWidth -= runewidth.StringWidth(prefix)
	remainingWidth -= runewidth.StringWidth(icon)
	remainingWidth -= 1 // for the literal " " in the branchLine format string

	diffWidth := runewidth.StringWidth(addedDiff) + runewidth.StringWidth(removedDiff)
	if diffWidth > 0 {
		diffWidth += 1
	}

	// Use fixed width for diff stats to avoid layout issues
	remainingWidth -= diffWidth

	branch := subtitle
	if i.Started() && hasMultipleRepos {
		repoName, err := i.RepoName()
		if err != nil {
			log.ErrorLog.Printf("could not get repo name in instance renderer: %v", err)
		} else {
			branch += fmt.Sprintf(" (%s)", repoName)
		}
	}
	// Don't show branch if there's no space for it. Or show ellipsis if it's too long.
	branchWidth := runewidth.StringWidth(branch)
	if remainingWidth < 0 {
		branch = ""
	} else if remainingWidth < branchWidth {
		if remainingWidth < 3 {
			branch = ""
		} else {
			// We know the remainingWidth is at least 4 and branch is longer than that, so this is safe.
			branch = runewidth.Truncate(branch, remainingWidth-3, "...")
		}
	}
	remainingWidth -= runewidth.StringWidth(branch)

	// Add spaces to fill the remaining width.
	spaces := ""
	if remainingWidth > 0 {
		spaces = strings.Repeat(" ", remainingWidth)
	}

	branchLine := fmt.Sprintf("%s %s%s%s%s", strings.Repeat(" ", len(prefix)), icon, branch, spaces, diff)

	// join title and subtitle
	text := lipgloss.JoinVertical(
		lipgloss.Left,
		title,
		descS.Render(branchLine),
	)

	return text
}

// issueStateStyle colors a ticket's Linear state: blocked states (any state whose name
// contains "blocked") stand out, others stay quiet.
func issueStateStyle(state string) lipgloss.Style {
	if strings.Contains(strings.ToLower(state), "blocked") {
		return lipgloss.NewStyle().Foreground(blockedStyle.GetForeground())
	}
	return lipgloss.NewStyle().Foreground(listDescStyle.GetForeground())
}

// dueLabel renders a Linear due date (YYYY-MM-DD) as "due 10/5", adding the year when
// it is not the current one. Empty when there is no due date.
func dueLabel(date string, now time.Time) string {
	if date == "" {
		return ""
	}
	d, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "due " + date
	}
	if d.Year() != now.Year() {
		return fmt.Sprintf("due %d/%d/%02d", d.Month(), d.Day(), d.Year()%100)
	}
	return fmt.Sprintf("due %d/%d", d.Month(), d.Day())
}

// SetTitle replaces the header text (default " Instances ").
func (l *List) SetTitle(title string) {
	l.title = title
}

// Remove takes an instance out of the list without killing it, keeping the selection
// on the same instance where possible. Returns false if it was not in the list.
func (l *List) Remove(target *session.Instance) bool {
	idx := -1
	for i, inst := range l.items {
		if inst == target {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false
	}
	if repoName, err := target.RepoName(); err == nil {
		l.rmRepo(repoName)
	}
	l.items = append(l.items[:idx], l.items[idx+1:]...)
	if idx < l.selectedIdx || l.selectedIdx >= len(l.items) {
		l.selectedIdx--
	}
	if l.selectedIdx < 0 {
		l.selectedIdx = 0
	}
	return true
}

// Prepend adds an already-started instance at the top of the list (newest first).
func (l *List) Prepend(instance *session.Instance) {
	l.items = append([]*session.Instance{instance}, l.items...)
	if len(l.items) > 1 {
		l.selectedIdx++
	}
	if repoName, err := instance.RepoName(); err == nil {
		l.addRepo(repoName)
	}
}

func (l *List) String() string {
	titleText := " Instances "
	if l.title != "" {
		titleText = l.title
	}
	const autoYesText = " auto-yes "

	// Write the title.
	var b strings.Builder
	b.WriteString("\n")

	// Write title line
	// add padding of 2 because the border on list items adds some extra characters
	titleWidth := AdjustPreviewWidth(l.width) + 2
	var badge string
	switch {
	case l.linearMode:
		blocked, idle := l.CountByStatus()
		parts := []string{}
		if blocked > 0 {
			parts = append(parts, blockedBadgeStyle.Render(fmt.Sprintf(" %d blocked ", blocked)))
		}
		if idle > 0 {
			parts = append(parts, idleBadgeStyle.Render(fmt.Sprintf(" %d idle ", idle)))
		}
		badge = lipgloss.JoinHorizontal(lipgloss.Top, parts...)
	case l.autoyes:
		badge = autoYesStyle.Render(autoYesText)
	}
	if badge == "" {
		b.WriteString(lipgloss.Place(
			titleWidth, 1, lipgloss.Left, lipgloss.Bottom, mainTitle.Render(titleText)))
	} else {
		title := lipgloss.Place(
			titleWidth/2, 1, lipgloss.Left, lipgloss.Bottom, mainTitle.Render(titleText))
		right := lipgloss.Place(
			titleWidth-(titleWidth/2), 1, lipgloss.Right, lipgloss.Bottom, badge)
		b.WriteString(lipgloss.JoinHorizontal(
			lipgloss.Top, title, right))
	}

	b.WriteString("\n")
	b.WriteString("\n")

	// Render the visible window of the list.
	start, end := l.visibleRange(l.height - listHeaderLines)
	l.shownStart, l.shownEnd, l.shownTop = start, end, listHeaderLines
	if start > 0 {
		l.shownTop++
		b.WriteString(scrollHintStyle.Render(fmt.Sprintf("   ↑ %d more", start)))
		b.WriteString("\n")
	}
	for i := start; i < end; i++ {
		b.WriteString(l.renderer.Render(l.items[i], i+1, i == l.selectedIdx, len(l.repos) > 1))
		if i != end-1 {
			b.WriteString("\n")
		}
	}
	if end < len(l.items) {
		b.WriteString("\n")
		b.WriteString(scrollHintStyle.Render(fmt.Sprintf("   ↓ %d more", len(l.items)-end)))
	}

	// Never render taller than the viewport, whatever the terminal size.
	out := b.String()
	if l.height > 0 {
		if lines := strings.Split(out, "\n"); len(lines) > l.height {
			out = strings.Join(lines[:l.height], "\n")
		}
	}
	return lipgloss.Place(l.width, l.height, lipgloss.Left, lipgloss.Top, out)
}

const (
	// listHeaderLines is the blank line, title line and spacer above the first item.
	listHeaderLines = 3
	// itemLines is the height of one rendered item: title line and branch line.
	itemLines = 2
)

// ItemAtRow returns the index of the item drawn on the given row of the last String
// output (0 = the list's first row), or false if that row holds no item.
func (l *List) ItemAtRow(row int) (int, bool) {
	if row < l.shownTop {
		return 0, false
	}
	idx := l.shownStart + (row-l.shownTop)/itemLines
	if idx >= l.shownEnd || idx >= len(l.items) {
		return 0, false
	}
	return idx, true
}

// visibleRange returns the [start, end) slice of items that fits in avail lines,
// scrolled so the selected item is shown. When not everything fits, one line at
// each end is reserved for the "N more" hints.
func (l *List) visibleRange(avail int) (start, end int) {
	n := len(l.items)
	if n*itemLines <= avail || l.height <= 0 {
		l.offset = 0
		return 0, n
	}
	perPage := (avail - 2) / itemLines
	if perPage < 1 {
		perPage = 1
	}
	if l.selectedIdx < l.offset {
		l.offset = l.selectedIdx
	}
	if l.selectedIdx >= l.offset+perPage {
		l.offset = l.selectedIdx - perPage + 1
	}
	if l.offset > n-perPage {
		l.offset = n - perPage
	}
	if l.offset < 0 {
		l.offset = 0
	}
	return l.offset, l.offset + perPage
}

// Down selects the next item in the list.
func (l *List) Down() {
	if len(l.items) == 0 {
		return
	}
	if l.selectedIdx < len(l.items)-1 {
		l.selectedIdx++
	} else {
		l.selectedIdx = 0
	}
}

// Kill selects the next item in the list.
func (l *List) Kill() {
	if len(l.items) == 0 {
		return
	}
	targetInstance := l.items[l.selectedIdx]

	// Kill the tmux session
	if err := targetInstance.Kill(); err != nil {
		log.ErrorLog.Printf("could not kill instance: %v", err)
	}

	// If you delete the last one in the list, select the previous one.
	if l.selectedIdx == len(l.items)-1 {
		defer l.Up()
	}

	// Unregister the reponame.
	repoName, err := targetInstance.RepoName()
	if err != nil {
		log.ErrorLog.Printf("could not get repo name: %v", err)
	} else {
		l.rmRepo(repoName)
	}

	// Since there's items after this, the selectedIdx can stay the same.
	l.items = append(l.items[:l.selectedIdx], l.items[l.selectedIdx+1:]...)
}

func (l *List) Attach() (chan struct{}, error) {
	targetInstance := l.items[l.selectedIdx]
	return targetInstance.Attach()
}

// First selects the first item in the list.
func (l *List) First() {
	l.selectedIdx = 0
}

// Last selects the last item in the list.
func (l *List) Last() {
	if len(l.items) > 0 {
		l.selectedIdx = len(l.items) - 1
	}
}

// Up selects the prev item in the list.
func (l *List) Up() {
	if len(l.items) == 0 {
		return
	}
	if l.selectedIdx > 0 {
		l.selectedIdx--
	} else {
		l.selectedIdx = len(l.items) - 1
	}
}

func (l *List) addRepo(repo string) {
	if _, ok := l.repos[repo]; !ok {
		l.repos[repo] = 0
	}
	l.repos[repo]++
}

func (l *List) rmRepo(repo string) {
	if _, ok := l.repos[repo]; !ok {
		log.ErrorLog.Printf("repo %s not found", repo)
		return
	}
	l.repos[repo]--
	if l.repos[repo] == 0 {
		delete(l.repos, repo)
	}
}

// AddInstance adds a new instance to the list. It returns a finalizer function that should be called when the instance
// is started. If the instance was restored from storage or is paused, you can call the finalizer immediately.
// When creating a new one and entering the name, you want to call the finalizer once the name is done.
func (l *List) AddInstance(instance *session.Instance) (finalize func()) {
	l.items = append(l.items, instance)
	// The finalizer registers the repo name once the instance is started.
	return func() {
		repoName, err := instance.RepoName()
		if err != nil {
			log.ErrorLog.Printf("could not get repo name: %v", err)
			return
		}

		l.addRepo(repoName)
	}
}

// GetSelectedInstance returns the currently selected instance
func (l *List) GetSelectedInstance() *session.Instance {
	if len(l.items) == 0 {
		return nil
	}
	return l.items[l.selectedIdx]
}

// SetSelectedInstance sets the selected index. Noop if the index is out of bounds.
func (l *List) SetSelectedInstance(idx int) {
	if idx >= len(l.items) {
		return
	}
	l.selectedIdx = idx
}

// SelectInstance finds and selects the given instance in the list.
func (l *List) SelectInstance(target *session.Instance) {
	for i, inst := range l.items {
		if inst == target {
			l.SetSelectedInstance(i)
			return
		}
	}
}

// MoveUp swaps the selected instance with the one above it.
func (l *List) MoveUp() bool {
	if l.selectedIdx <= 0 || len(l.items) < 2 {
		return false
	}
	l.items[l.selectedIdx], l.items[l.selectedIdx-1] = l.items[l.selectedIdx-1], l.items[l.selectedIdx]
	l.selectedIdx--
	return true
}

// MoveDown swaps the selected instance with the one below it.
func (l *List) MoveDown() bool {
	if l.selectedIdx >= len(l.items)-1 || len(l.items) < 2 {
		return false
	}
	l.items[l.selectedIdx], l.items[l.selectedIdx+1] = l.items[l.selectedIdx+1], l.items[l.selectedIdx]
	l.selectedIdx++
	return true
}

// GetInstances returns all instances in the list
func (l *List) GetInstances() []*session.Instance {
	return l.items
}
