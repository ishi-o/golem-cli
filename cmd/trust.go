package cmd

import (
	"fmt"
	"io"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ConfirmWorkspaceTrust presents the workspace approval in the same terminal
// UI language as the session. It deliberately refuses non-TTY input so a
// redirected command cannot turn a missing approval into an implicit yes.
func ConfirmWorkspaceTrust(input io.Reader, output io.Writer, workspace string) (bool, error) {
	if _, ok := terminalInputFile(input); !ok || !isTerminalWriter(output) {
		return false, fmt.Errorf("workspace %q needs approval from an interactive terminal", workspace)
	}

	model := workspaceTrustModel{
		workspace: workspace,
		width:     80,
		height:    24,
	}
	program := tea.NewProgram(model,
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
	)
	final, err := program.Run()
	if err != nil {
		return false, fmt.Errorf("run workspace trust prompt: %w", err)
	}
	result, ok := final.(workspaceTrustModel)
	if !ok {
		return false, nil
	}
	return result.approved, nil
}

type workspaceTrustModel struct {
	workspace string
	width     int
	height    int
	approved  bool
}

func (m workspaceTrustModel) Init() tea.Cmd { return nil }

func (m workspaceTrustModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := message.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	case tea.KeyMsg:
		switch strings.ToLower(msg.String()) {
		case "y", "enter":
			m.approved = true
			return m, tea.Quit
		case "n", "esc", "q", "ctrl+c":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m workspaceTrustModel) View() string {
	width := m.width
	if width < 40 {
		width = 40
	}
	boxWidth := width - 8
	if boxWidth > 84 {
		boxWidth = 84
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.AdaptiveColor{Light: "62", Dark: "86"}).
		Padding(1, 2).
		Width(boxWidth).
		Render(lipgloss.JoinVertical(lipgloss.Left,
			lipgloss.NewStyle().Bold(true).Foreground(lipgloss.AdaptiveColor{Light: "62", Dark: "86"}).Render("✦ Trust workspace"),
			"",
			lipgloss.NewStyle().Foreground(terminalBodyColor).Render("Allow golem to read and modify files in:"),
			lipgloss.NewStyle().Bold(true).Foreground(terminalBodyColor).Render(m.workspace),
			"",
			lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "110", Dark: "250"}).Render("y / Enter  allow    n / Esc  deny"),
		))
	height := m.height
	if height < 8 {
		height = 8
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
