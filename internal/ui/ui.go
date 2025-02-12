package ui

import (
	"fmt"
	"os"
	"strconv"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	humanize "github.com/dustin/go-humanize"

	"github.com/soundmonster/gh-flush/internal/client"
)

type uiMode int

const (
	loadingNotifications uiMode = iota
	flushingNotifications
	done
)

type model struct {
	uiMode              uiMode
	flushClient         *client.Client
	notificationResults []client.NotificationResult
	numTotal            int
	numProcessed        int
	numFlushed          int
	width               int
	height              int
	channelTo           chan string
	channelFrom         chan string
	spinner             spinner.Model
	progress            progress.Model
	keys                keyMap
	help                help.Model
}

var (
	red     = lipgloss.ANSIColor(1)
	green   = lipgloss.ANSIColor(2)
	yellow  = lipgloss.ANSIColor(3)
	blue    = lipgloss.ANSIColor(4)
	magenta = lipgloss.ANSIColor(5)
	gray    = lipgloss.ANSIColor(7)
	white   = lipgloss.ANSIColor(15)
)
var (
	loadingStyle = lipgloss.NewStyle().Margin(1, 1)
	doneStyle    = lipgloss.NewStyle().Margin(1, 1)
	deleteMark   = lipgloss.NewStyle().Foreground(red).SetString("⨉")
	checkMark    = lipgloss.NewStyle().Foreground(green).SetString("✓")
	repoStyle    = lipgloss.NewStyle().Foreground(magenta).Italic(true)
	subjectStyle = lipgloss.NewStyle().Foreground(white)
	deletedStyle = lipgloss.NewStyle().Foreground(gray).Strikethrough(true)
	userStyle    = lipgloss.NewStyle().Foreground(gray)
	tsStyle      = lipgloss.NewStyle().Foreground(blue).Italic(true)
)

type keyMap struct {
	Quit key.Binding
}

var defaultKeyMap = keyMap{
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c", "esc"),
		key.WithHelp("q/esc", "quit"),
	),
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{{k.Quit}}
}

func newModel(flushClient *client.Client) model {
	p := progress.New(
		progress.WithDefaultGradient(),
		progress.WithWidth(60),
		progress.WithoutPercentage(),
	)
	s := spinner.New()
	s.Style = lipgloss.NewStyle().Foreground(magenta)
	s.Spinner = spinner.Dot
	return model{
		uiMode:              loadingNotifications,
		flushClient:         flushClient,
		notificationResults: []client.NotificationResult{},
		channelTo:           make(chan string),
		channelFrom:         make(chan string),
		spinner:             s,
		progress:            p,
		keys:                defaultKeyMap,
		help:                help.New(),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(fetchNotifications(m), m.spinner.Tick)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, defaultKeyMap.Quit):
			// TODO make sure to quit immediately and abort all pending deletions
			return m, tea.Quit
		}
	case processedNotificationMsg:
		res := client.NotificationResult(msg)
		m.numProcessed++
		if res.Deleted {
			m.numFlushed++
		}
		m.notificationResults = append(m.notificationResults, res)

		// Update progress bar
		progressCmd := m.progress.SetPercent(float64(m.numProcessed) / float64(m.numTotal))

		return m, tea.Batch(
			progressCmd,
			tea.Println(formatNotificationResult(m, res)),
			recvProcessed(m), // download the next notification
		)
	case finishedMsg:
		// Everything's been processed. We're done!
		m.uiMode = done
		return m, tea.Quit // exit the program
	case notificationsFetchedMsg:
		m.uiMode = flushingNotifications
		m.numTotal = m.flushClient.NotificationCount()
		m.flushClient.ProcessNotifications()

		return m, recvProcessed(m)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case progress.FrameMsg:
		newModel, cmd := m.progress.Update(msg)
		if newModel, ok := newModel.(progress.Model); ok {
			m.progress = newModel
		}
		return m, cmd
	}
	return m, nil
}

func (m model) View() string {
	n := m.numTotal
	w := lipgloss.Width(fmt.Sprintf("%d", n))

	helpView := ""
	var result string
	switch m.uiMode {
	case loadingNotifications:
		helpView = m.help.View(m.keys)
		result = loadingStyle.Render(fmt.Sprintf("%s 🚽 Loading notifications ...", m.spinner.View()))
	case flushingNotifications:
		helpView = m.help.View(m.keys)
		notificationCount := fmt.Sprintf(" %*d/%*d", w, m.numProcessed, w, n)
		result = fmt.Sprintf("\n\n%s %s", m.progress.View(), notificationCount)
	case done:
		boldStyle := lipgloss.NewStyle().Bold(true)
		processed := boldStyle.Render(strconv.Itoa(m.numProcessed))
		flushed := boldStyle.Render(strconv.Itoa(m.numFlushed))
		done := boldStyle.Render("Done!")
		result = doneStyle.Render(fmt.Sprintf("🎉 %s Processed %s notifications, flushed %s 🚽", done, processed, flushed))
	}
	return result + "\n" + helpView
}

func tag(s string, c lipgloss.TerminalColor) string {
	return lipgloss.NewStyle().Foreground(c).Render(fmt.Sprintf("[%s]", s))
}

func formatNotificationResult(m model, res client.NotificationResult) string {
	var action string
	var subject string
	if res.Deleted {
		action = deleteMark.Render()
		subject = deletedStyle.Render(res.Notification.Subject.Title)
	} else {
		action = checkMark.Render()
		subject = subjectStyle.Render(res.Notification.Subject.Title)
	}
	repo := repoStyle.Render(res.Notification.Repository.FullName)
	user := ""
	if res.PR != nil {
		user = userStyle.Render(" by " + res.PR.User.Login)
	}
	ts := tsStyle.Render(" " + humanize.Time(res.Notification.UpdatedAt))

	tags := ""
	if res.BotPR {
		tags += " " + tag("bot", yellow)
	}
	if res.ClosedPR {
		tags += " " + tag("closed", red)
	}
	if res.Read {
		tags += " " + tag("read", magenta)
	}
	result := fmt.Sprintf("%s %s in %s%s%s%s", action, subject, repo, user, ts, tags)
	if m.width < lipgloss.Width(result) {
		lineBreak := "\n  "
		result = fmt.Sprintf("%s %s%sin %s%s%s%s", action, subject, lineBreak, repo, user, ts, tags)
	}
	return result
}

type processedNotificationMsg client.NotificationResult
type finishedMsg bool

func recvProcessed(m model) tea.Cmd {
	notification, ok := m.flushClient.GetNotificationResult()
	return func() tea.Msg {
		if ok {
			return processedNotificationMsg(notification)
		} else {
			return finishedMsg(true)
		}
	}
}

type notificationsFetchedMsg bool

func fetchNotifications(m model) tea.Cmd {
	return func() tea.Msg {
		m.flushClient.FetchNotifications()
		return notificationsFetchedMsg(true)
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func Run(flushClient *client.Client) {
	if _, err := tea.NewProgram(newModel(flushClient)).Run(); err != nil {
		fmt.Println("Error running program:", err)
		os.Exit(1)
	}
}
