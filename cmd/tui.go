package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	glamouransi "github.com/charmbracelet/glamour/ansi"
	glamourstyles "github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"
	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/agent"
)

var terminalBodyColor = lipgloss.AdaptiveColor{Light: "#1F2937", Dark: "#F8FAFC"}

type terminalEventKind uint8

const (
	terminalEventText terminalEventKind = iota
	terminalEventHistory
	terminalEventUser
	terminalEventQuestion
	terminalEventReady
	terminalEventModel
	terminalEventContent
	terminalEventReasoning
	terminalEventUsage
	terminalEventSubagent
	terminalEventMessageQueued
	terminalEventQueuedMessageRead
	terminalEventToolStart
	terminalEventToolEnd
	terminalEventError
	terminalEventFinished
	terminalEventStatus
)

// terminalEvent is the structured bridge between Golem callbacks and the
// Bubble Tea model. Keeping the event structured is important: a tool result
// is a foldable block, not a string that has to be parsed back out of a
// terminal stream.
type terminalEvent struct {
	kind terminalEventKind
	text string

	model string
	role  string
	usage *schema.TokenUsage

	subagent agent.SubagentEvent

	requestID  string
	display    string
	requestIDs []string
	options    []string

	callID    string
	name      string
	arguments string
	result    string
	err       string

	outcome agent.Outcome
}

type terminalRenderTarget interface {
	sendTerminalEvent(terminalEvent)
}

type terminalLineResult struct {
	line string
	err  error
}

// terminalUI is the lifetime of one interactive terminal. It stays alive
// while a request is running, so listener events can update the same view
// that owns the input editor.
type terminalUI struct {
	program *tea.Program
	lines   chan terminalLineResult
	done    chan struct{}

	writer *terminalUIWriter

	mu     sync.Mutex
	err    error
	closed bool
}

func newTerminalUI(input io.Reader, output io.Writer) *terminalUI {
	lines := make(chan terminalLineResult, 1)
	model := newTerminalModel(lines)
	program := tea.NewProgram(model,
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
	)
	ui := &terminalUI{
		program: program,
		lines:   lines,
		done:    make(chan struct{}),
	}
	ui.writer = &terminalUIWriter{ui: ui}
	go func() {
		_, err := program.Run()
		if err == nil {
			err = io.EOF
		}
		ui.mu.Lock()
		ui.err = err
		ui.closed = true
		ui.mu.Unlock()
		select {
		case lines <- terminalLineResult{err: err}:
		default:
		}
		close(ui.done)
	}()
	return ui
}

func (u *terminalUI) sendTerminalEvent(event terminalEvent) {
	if u == nil || u.program == nil {
		return
	}
	u.program.Send(event)
}

func (u *terminalUI) readLine() (string, error) {
	result := <-u.lines
	return result.line, result.err
}

func (u *terminalUI) close() error {
	if u == nil {
		return nil
	}
	u.mu.Lock()
	closed := u.closed
	u.mu.Unlock()
	if !closed {
		u.program.Quit()
	}
	<-u.done
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.err
}

type terminalUIWriter struct {
	ui *terminalUI
}

func (w *terminalUIWriter) Write(p []byte) (int, error) {
	if w == nil || w.ui == nil {
		return len(p), nil
	}
	w.ui.sendTerminalEvent(terminalEvent{kind: terminalEventText, text: string(p)})
	return len(p), nil
}

func (w *terminalUIWriter) sendTerminalEvent(event terminalEvent) {
	if w == nil || w.ui == nil {
		return
	}
	w.ui.sendTerminalEvent(event)
}

type terminalBlockKind uint8

const (
	terminalBlockNotice terminalBlockKind = iota
	terminalBlockUser
	terminalBlockQuestion
	terminalBlockAnswer
	terminalBlockReasoning
	terminalBlockTool
	terminalBlockSkill
	terminalBlockSubagent
	terminalBlockError
)

type terminalBlock struct {
	kind terminalBlockKind
	id   string

	title     string
	body      string
	arguments string
	details   string
	preview   string
	footer    string
	model     string
	usage     *schema.TokenUsage

	collapsed bool
	done      bool
	failed    bool
}

type terminalModel struct {
	input    textarea.Model
	viewport viewport.Model
	lines    chan terminalLineResult

	width  int
	height int

	blocks []terminalBlock

	answerIndex    int
	reasoningIndex int
	subagents      map[string]int
	tools          map[string]int
	focusIndex     int

	modelName      string
	usage          *schema.TokenUsage
	usageModel     string
	status         string
	busy           bool
	questionActive bool

	markdown       *glamour.TermRenderer
	markdownWidth  int
	markdownFailed bool
}

func newTerminalModel(lines chan terminalLineResult) *terminalModel {
	input := textarea.New()
	input.Placeholder = "Ask golem anything…"
	input.Prompt = "  › "
	input.ShowLineNumbers = false
	input.SetHeight(3)
	input.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")
	focused, blurred := textarea.DefaultStyles()
	focused.Base = lipgloss.NewStyle().Foreground(terminalBodyColor)
	focused.CursorLine = lipgloss.NewStyle().Background(lipgloss.AdaptiveColor{Light: "255", Dark: "236"})
	focused.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("62"))
	focused.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"})
	focused.Text = lipgloss.NewStyle().Foreground(terminalBodyColor)
	blurred.Base = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"})
	input.FocusedStyle = focused
	input.BlurredStyle = blurred

	return &terminalModel{
		input:          input,
		viewport:       viewport.New(0, 0),
		lines:          lines,
		width:          80,
		height:         24,
		answerIndex:    -1,
		reasoningIndex: -1,
		subagents:      make(map[string]int),
		tools:          make(map[string]int),
		focusIndex:     -1,
		status:         "ready",
	}
}

func (m *terminalModel) Init() tea.Cmd {
	return m.input.Focus()
}

func (m *terminalModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
		m.rebuildViewport()
		return m, nil
	case terminalEvent:
		m.applyEvent(msg)
		m.rebuildViewport()
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	default:
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(message)
		return m, cmd
	}
}

func (m *terminalModel) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		return m, tea.Quit
	case "enter":
		if m.busy && !m.questionActive {
			return m, nil
		}
		line := m.input.Value()
		if strings.TrimSpace(line) == "" {
			return m, nil
		}
		m.appendUser(line)
		if m.questionActive {
			m.questionActive = false
			m.status = "thinking"
			m.input.Blur()
		}
		m.input.Reset()
		m.rebuildViewport()
		return m, func() tea.Msg {
			m.lines <- terminalLineResult{line: line}
			return nil
		}
	case "ctrl+o":
		m.toggleFocusedFold()
		m.rebuildViewport()
		return m, nil
	case "alt+o":
		m.toggleAllFolds()
		m.rebuildViewport()
		return m, nil
	case "tab":
		m.focusNextFoldable(1)
		m.rebuildViewport()
		return m, nil
	case "shift+tab":
		m.focusNextFoldable(-1)
		m.rebuildViewport()
		return m, nil
	case "esc":
		m.focusIndex = -1
		m.rebuildViewport()
		return m, nil
	case "pgup", "pageup":
		m.viewport.PageUp()
		return m, nil
	case "pgdown", "pagedown":
		m.viewport.PageDown()
		return m, nil
	case "ctrl+up":
		m.viewport.ScrollUp(3)
		return m, nil
	case "ctrl+down":
		m.viewport.ScrollDown(3)
		return m, nil
	case "ctrl+l":
		m.viewport.GotoBottom()
		return m, nil
	}
	if m.busy && !m.questionActive {
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *terminalModel) View() string {
	m.updateLayout()
	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "62", Dark: "86"}).Render("✦ golem")
	status := m.status
	if status == "" {
		status = "ready"
	}
	header = lipgloss.JoinHorizontal(lipgloss.Bottom, header, "  ",
		lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"}).Render(status))

	body := m.viewport.View()
	if strings.TrimSpace(body) == "" {
		body = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"}).Render("No messages yet. Start a conversation below.")
	}

	inputTitle := "message"
	if m.busy {
		inputTitle = "working…"
	}
	inputTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "62", Dark: "86"}).Render(inputTitle)
	inputBody := lipgloss.JoinVertical(lipgloss.Left, inputTitle, m.input.View())
	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "62", Dark: "62"}).
		Padding(0, 1).
		Width(maxInt(1, m.width-2)).
		Render(inputBody)

	footerParts := []string{"enter send", "ctrl+j newline", "tab focus", "ctrl+o fold", "pgup/pgdn scroll"}
	if m.modelName != "" {
		footerParts = append(footerParts, "model "+m.modelName)
	}
	if m.usage != nil {
		footerParts = append(footerParts, usageFooter(m.usageModel, m.usage))
	}
	footer := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"}).Render("╰─ " + strings.Join(footerParts, "  ·  "))

	return lipgloss.JoinVertical(lipgloss.Left, header, body, inputBox, footer)
}

func (m *terminalModel) applyEvent(event terminalEvent) {
	switch event.kind {
	case terminalEventText:
		m.appendNotice("info", strings.TrimSpace(event.text))
	case terminalEventHistory:
		m.applyHistory(event.role, event.text)
	case terminalEventUser:
		m.appendUser(event.text)
	case terminalEventQuestion:
		m.appendBlock(terminalBlock{
			kind:    terminalBlockQuestion,
			title:   "question",
			body:    event.text,
			details: questionOptions(event.options),
			done:    true,
		})
		m.questionActive = true
		m.status = "waiting for input"
		m.input.Focus()
	case terminalEventReady:
		m.busy = true
		m.questionActive = false
		m.status = "thinking"
		m.answerIndex = -1
		m.reasoningIndex = -1
		m.subagents = make(map[string]int)
		m.tools = make(map[string]int)
		m.modelName = ""
		m.usage = nil
		m.usageModel = ""
		m.input.Blur()
	case terminalEventModel:
		m.modelName = strings.TrimSpace(event.model)
		m.status = "generating"
	case terminalEventContent:
		if strings.TrimSpace(event.text) == "" {
			return
		}
		index := m.ensureAnswer()
		m.blocks[index].body = event.text
		m.blocks[index].preview = compactText(event.text, 120)
	case terminalEventReasoning:
		if strings.TrimSpace(event.text) == "" {
			return
		}
		index := m.ensureReasoning()
		m.blocks[index].body = event.text
		m.blocks[index].preview = compactText(event.text, 120)
	case terminalEventUsage:
		if event.usage != nil {
			usage := *event.usage
			m.usage = &usage
			m.usageModel = event.model
		}
	case terminalEventSubagent:
		m.applySubagent(event.subagent)
	case terminalEventMessageQueued:
		text := "message queued"
		if event.requestID != "" {
			text += " " + event.requestID
		}
		if event.display != "" {
			text += ": " + event.display
		}
		m.appendNotice("queued", text)
	case terminalEventQueuedMessageRead:
		if len(event.requestIDs) > 0 {
			m.appendNotice("queued", "messages read: "+strings.Join(event.requestIDs, ", "))
		}
	case terminalEventToolStart:
		m.applyToolStart(event)
	case terminalEventToolEnd:
		m.applyToolEnd(event)
	case terminalEventError:
		m.appendBlock(terminalBlock{kind: terminalBlockError, title: "error", body: event.text, failed: true, done: true})
		m.busy = false
		m.questionActive = false
		m.status = "failed"
		m.answerIndex = -1
		m.reasoningIndex = -1
		m.subagents = make(map[string]int)
		m.tools = make(map[string]int)
		m.input.Focus()
	case terminalEventFinished:
		if m.answerIndex >= 0 {
			m.blocks[m.answerIndex].done = true
			m.answerIndex = -1
		}
		if m.reasoningIndex >= 0 {
			m.blocks[m.reasoningIndex].done = true
			m.reasoningIndex = -1
		}
		m.busy = false
		m.questionActive = false
		m.status = strings.ToLower(strings.TrimSpace(string(event.outcome)))
		if m.status == "" {
			m.status = "finished"
		}
		m.subagents = make(map[string]int)
		m.tools = make(map[string]int)
		m.input.Focus()
	case terminalEventStatus:
		m.appendNotice("event", event.text)
	}
}

func (m *terminalModel) appendUser(line string) {
	m.appendBlock(terminalBlock{
		kind:  terminalBlockUser,
		title: "you",
		body:  strings.TrimSpace(line),
		done:  true,
	})
}

func (m *terminalModel) appendNotice(title, body string) {
	if body == "" {
		return
	}
	m.appendBlock(terminalBlock{kind: terminalBlockNotice, title: title, body: body, done: true})
}

func (m *terminalModel) applyHistory(role, body string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "user":
		m.appendUser(body)
	case "assistant":
		m.appendBlock(terminalBlock{kind: terminalBlockAnswer, title: "answer", body: body, done: true})
	case "reasoning":
		m.appendBlock(terminalBlock{kind: terminalBlockReasoning, title: "reasoning", body: body, collapsed: true, done: true})
	default:
		m.appendNotice(roleOrDefault(role, "message"), body)
	}
}

func roleOrDefault(role, fallback string) string {
	if role = strings.TrimSpace(role); role != "" {
		return role
	}
	return fallback
}

func (m *terminalModel) appendBlock(block terminalBlock) int {
	m.blocks = append(m.blocks, block)
	return len(m.blocks) - 1
}

func (m *terminalModel) ensureAnswer() int {
	if m.answerIndex < 0 {
		m.answerIndex = m.appendBlock(terminalBlock{
			kind:  terminalBlockAnswer,
			title: "answer",
		})
	}
	return m.answerIndex
}

func (m *terminalModel) ensureReasoning() int {
	if m.reasoningIndex < 0 {
		m.reasoningIndex = m.appendBlock(terminalBlock{
			kind:      terminalBlockReasoning,
			title:     "reasoning",
			collapsed: true,
		})
	}
	return m.reasoningIndex
}

func (m *terminalModel) applySubagent(event agent.SubagentEvent) {
	id := strings.TrimSpace(event.SubagentID)
	if id == "" {
		id = "unknown"
	}
	index, ok := m.subagents[id]
	if !ok {
		index = m.appendBlock(terminalBlock{
			kind:      terminalBlockSubagent,
			id:        id,
			title:     "subagent · " + id,
			collapsed: true,
			footer:    "starting",
		})
		m.subagents[id] = index
	}
	block := &m.blocks[index]
	if event.Model != "" {
		block.model = strings.TrimSpace(event.Model)
	}
	if event.Usage != nil {
		usage := *event.Usage
		block.usage = &usage
	}
	if event.Description != "" {
		block.preview = compactText(event.Description, 100)
	}
	if event.ContentSoFar != "" {
		block.body = event.ContentSoFar
		block.preview = compactText(event.ContentSoFar, 100)
	}
	if event.Spent() {
		block.footer = usageFooter(block.model, block.usage)
	}
	if event.Ended() {
		block.done = true
		outcome := strings.ToLower(strings.TrimSpace(string(event.Outcome)))
		if outcome == "" {
			outcome = "finished"
		}
		if summary := usageFooter(block.model, block.usage); summary != "" {
			outcome += " · " + summary
		} else if block.model != "" {
			outcome += " · " + block.model
		}
		block.footer = outcome
		block.failed = event.Outcome != agent.OutcomeCompleted
	}
}

func (m *terminalModel) applyToolStart(event terminalEvent) {
	name := strings.TrimSpace(event.name)
	if name == "" {
		name = "tool"
	}
	key := strings.TrimSpace(event.callID)
	if key == "" {
		key = fmt.Sprintf("%s#%d", name, len(m.tools)+1)
	}
	kind := terminalBlockTool
	if isSkillTool(name) {
		kind = terminalBlockSkill
	}
	index := m.appendBlock(terminalBlock{
		kind:      kind,
		id:        key,
		title:     name,
		arguments: event.arguments,
		details:   toolDetails(event.arguments, "", ""),
		preview:   compactText(event.arguments, 100),
		footer:    "running",
		collapsed: true,
	})
	m.tools[key] = index
}

func (m *terminalModel) applyToolEnd(event terminalEvent) {
	key := strings.TrimSpace(event.callID)
	index, ok := m.tools[key]
	if !ok {
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if (m.blocks[i].kind == terminalBlockTool || m.blocks[i].kind == terminalBlockSkill) &&
				!m.blocks[i].done && (event.name == "" || m.blocks[i].title == event.name) {
				index, ok = i, true
				break
			}
		}
	}
	if !ok {
		m.applyToolStart(event)
		for i := len(m.blocks) - 1; i >= 0; i-- {
			if (m.blocks[i].kind == terminalBlockTool || m.blocks[i].kind == terminalBlockSkill) && !m.blocks[i].done {
				index, ok = i, true
				break
			}
		}
	}
	if !ok {
		return
	}
	block := &m.blocks[index]
	block.done = true
	block.failed = event.err != ""
	block.details = toolDetails(block.arguments, event.result, event.err)
	block.preview = compactText(event.result, 100)
	if event.err != "" {
		block.footer = "failed"
	} else {
		block.footer = "done"
	}
	for callID, callIndex := range m.tools {
		if callIndex == index {
			delete(m.tools, callID)
		}
	}
}

func (m *terminalModel) toggleFocusedFold() {
	if m.focusIndex < 0 || m.focusIndex >= len(m.blocks) || !m.blocks[m.focusIndex].foldable() {
		m.focusNextFoldable(1)
	}
	if m.focusIndex >= 0 && m.focusIndex < len(m.blocks) && m.blocks[m.focusIndex].foldable() {
		m.blocks[m.focusIndex].collapsed = !m.blocks[m.focusIndex].collapsed
	}
}

func (m *terminalModel) toggleAllFolds() {
	allCollapsed := true
	for _, block := range m.blocks {
		if block.foldable() && !block.collapsed {
			allCollapsed = false
			break
		}
	}
	for i := range m.blocks {
		if m.blocks[i].foldable() {
			m.blocks[i].collapsed = !allCollapsed
		}
	}
}

func (m *terminalModel) focusNextFoldable(direction int) {
	if direction == 0 {
		direction = 1
	}
	for step := 0; step < len(m.blocks); step++ {
		index := m.focusIndex + direction
		if index < 0 {
			index = len(m.blocks) - 1
		}
		if index >= len(m.blocks) {
			index = 0
		}
		m.focusIndex = index
		if m.blocks[index].foldable() {
			return
		}
	}
	m.focusIndex = -1
}

func (b terminalBlock) foldable() bool {
	switch b.kind {
	case terminalBlockReasoning, terminalBlockTool, terminalBlockSkill, terminalBlockSubagent:
		return true
	default:
		return false
	}
}

func (m *terminalModel) updateLayout() {
	if m.width < 20 {
		m.width = 80
	}
	if m.height < 8 {
		m.height = 24
	}
	inputWidth := maxInt(20, m.width-6)
	m.input.SetWidth(inputWidth)
	m.input.SetHeight(3)
	m.viewport.Width = maxInt(1, m.width-2)
	m.viewport.Height = maxInt(1, m.height-8)
}

func (m *terminalModel) rebuildViewport() {
	follow := m.viewport.AtBottom() || len(m.blocks) == 0
	m.viewport.SetContent(m.renderBlocks())
	if follow {
		m.viewport.GotoBottom()
	}
}

func (m *terminalModel) renderBlocks() string {
	if len(m.blocks) == 0 {
		return ""
	}
	views := make([]string, 0, len(m.blocks))
	for index, block := range m.blocks {
		if rendered := m.renderBlock(index, block); rendered != "" {
			views = append(views, rendered)
		}
	}
	return strings.Join(views, "\n\n")
}

func (m *terminalModel) renderBlock(index int, block terminalBlock) string {
	contentWidth := maxInt(24, m.width-6)
	switch block.kind {
	case terminalBlockAnswer:
		body := m.renderMarkdown(block.body, contentWidth)
		if body == "" {
			return ""
		}
		label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "62", Dark: "86"}).Render("✦ answer")
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.AdaptiveColor{Light: "62", Dark: "62"}).
			Padding(0, 1).
			Width(maxInt(1, m.width-2)).
			Render(lipgloss.JoinVertical(lipgloss.Left, label, body))
	case terminalBlockUser:
		label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "35", Dark: "78"}).Render("you")
		body := lipgloss.NewStyle().Foreground(terminalBodyColor).Render(block.body)
		return lipgloss.NewStyle().BorderLeft(true).BorderForeground(lipgloss.AdaptiveColor{Light: "35", Dark: "78"}).PaddingLeft(2).Render(lipgloss.JoinVertical(lipgloss.Left, label, body))
	case terminalBlockQuestion:
		label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "62", Dark: "86"}).Render("? question")
		body := lipgloss.NewStyle().Foreground(terminalBodyColor).Render(block.body)
		content := lipgloss.JoinVertical(lipgloss.Left, label, body)
		if block.details != "" {
			content = lipgloss.JoinVertical(lipgloss.Left, content, lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "110", Dark: "250"}).Render(block.details))
		}
		return lipgloss.NewStyle().BorderLeft(true).BorderForeground(lipgloss.AdaptiveColor{Light: "62", Dark: "86"}).PaddingLeft(2).Render(content)
	case terminalBlockReasoning:
		return m.renderFoldable(index, block, "◇", "reasoning", lipgloss.AdaptiveColor{Light: "136", Dark: "221"}, m.renderMarkdown(block.body, contentWidth))
	case terminalBlockTool:
		return m.renderFoldable(index, block, "⚙", "tool · "+block.title, lipgloss.AdaptiveColor{Light: "24", Dark: "117"}, block.details)
	case terminalBlockSkill:
		return m.renderFoldable(index, block, "✦", "skill · "+block.title, lipgloss.AdaptiveColor{Light: "127", Dark: "219"}, block.details)
	case terminalBlockSubagent:
		return m.renderFoldable(index, block, "◎", block.title, lipgloss.AdaptiveColor{Light: "30", Dark: "159"}, m.renderMarkdown(block.body, contentWidth))
	case terminalBlockError:
		label := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"}).Render("× " + block.title)
		body := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "124", Dark: "203"}).Render(block.body)
		return lipgloss.NewStyle().BorderLeft(true).BorderForeground(lipgloss.AdaptiveColor{Light: "160", Dark: "203"}).PaddingLeft(2).Render(lipgloss.JoinVertical(lipgloss.Left, label, body))
	case terminalBlockNotice:
		label := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"}).Render("· " + block.title)
		body := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "110", Dark: "250"}).Render(block.body)
		return lipgloss.NewStyle().BorderLeft(true).BorderForeground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"}).PaddingLeft(2).Render(lipgloss.JoinVertical(lipgloss.Left, label, body))
	default:
		return ""
	}
}

func (m *terminalModel) renderFoldable(index int, block terminalBlock, icon, title string, color lipgloss.TerminalColor, body string) string {
	marker := "▾"
	if block.collapsed {
		marker = "▸"
	}
	state := strings.TrimSpace(block.footer)
	if state == "" {
		state = "live"
	}
	if block.failed {
		state = "failed"
	}
	if m.focusIndex == index {
		title = "• " + title
	}
	header := lipgloss.NewStyle().Bold(true).Foreground(color).Render(fmt.Sprintf("%s %s %s", marker, icon, title))
	meta := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"}).Render(state)
	if block.preview != "" && block.collapsed {
		meta = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "245", Dark: "243"}).Render(state + " · " + block.preview)
	}
	header = lipgloss.JoinHorizontal(lipgloss.Bottom, header, "  ", meta)
	if block.collapsed || strings.TrimSpace(body) == "" {
		return header
	}
	details := lipgloss.NewStyle().
		BorderLeft(true).
		BorderForeground(color).
		PaddingLeft(2).
		Foreground(terminalBodyColor).
		Render(body)
	return lipgloss.JoinVertical(lipgloss.Left, header, details)
}

func (m *terminalModel) renderMarkdown(body string, width int) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if m.markdown == nil || m.markdownWidth != width {
		if m.markdown != nil {
			_ = m.markdown.Close()
		}
		renderer, err := glamour.NewTermRenderer(
			glamour.WithStyles(terminalMarkdownStyles()),
			glamour.WithWordWrap(width),
		)
		if err != nil {
			m.markdown = nil
			m.markdownFailed = true
		} else {
			m.markdown = renderer
			m.markdownWidth = width
			m.markdownFailed = false
		}
	}
	if m.markdown == nil || m.markdownFailed {
		return body
	}
	rendered, err := m.markdown.Render(body)
	if err != nil {
		return body
	}
	return strings.TrimSpace(rendered)
}

func terminalMarkdownStyles() glamouransi.StyleConfig {
	if lipgloss.HasDarkBackground() {
		style := glamourstyles.DarkStyleConfig
		style.Document.Color = terminalColorPointer("#F8FAFC")
		style.Text.Color = terminalColorPointer("#F8FAFC")
		return style
	}
	style := glamourstyles.LightStyleConfig
	style.Document.Color = terminalColorPointer("#1F2937")
	style.Text.Color = terminalColorPointer("#1F2937")
	return style
}

func terminalColorPointer(value string) *string { return &value }

func toolDetails(arguments, result, errText string) string {
	arguments = prettyJSONOrText(arguments)
	result = prettyJSONOrText(result)
	parts := make([]string, 0, 2)
	if strings.TrimSpace(arguments) != "" && strings.TrimSpace(arguments) != "{}" {
		parts = append(parts, "arguments:\n"+arguments)
	}
	if strings.TrimSpace(result) != "" {
		parts = append(parts, "result:\n"+result)
	}
	if errText != "" {
		parts = append(parts, "error:\n"+errText)
	}
	return strings.Join(parts, "\n\n")
}

func questionOptions(options []string) string {
	lines := make([]string, 0, len(options))
	for index, option := range options {
		lines = append(lines, fmt.Sprintf("%d. %s", index+1, option))
	}
	return strings.Join(lines, "\n")
}

func prettyJSONOrText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if formatted, err := json.MarshalIndent(json.RawMessage(value), "", "  "); err == nil {
		return string(formatted)
	}
	return value
}

func usageFooter(model string, usage *schema.TokenUsage) string {
	if usage == nil {
		return ""
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = "model"
	}
	value := fmt.Sprintf("%s: %d in / %d out / %d total", model, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	if usage.PromptTokenDetails.CachedTokens > 0 {
		value += fmt.Sprintf(" / %d cached", usage.PromptTokenDetails.CachedTokens)
	}
	if usage.CompletionTokensDetails.ReasoningTokens > 0 {
		value += fmt.Sprintf(" / %d reasoning", usage.CompletionTokensDetails.ReasoningTokens)
	}
	return value
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}
