package cmd

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceTrustModelUsesBubbleTeaKeys(t *testing.T) {
	model := workspaceTrustModel{workspace: "/tmp/project", width: 80, height: 24}
	view := model.View()
	require.Contains(t, view, "Trust workspace")
	require.Contains(t, view, "/tmp/project")
	require.Contains(t, view, "y / Enter")

	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	require.NotNil(t, command)
	approved, ok := updated.(workspaceTrustModel)
	require.True(t, ok)
	require.True(t, approved.approved)

	updated, command = model.Update(tea.KeyMsg{Type: tea.KeyEscape})
	require.NotNil(t, command)
	denied, ok := updated.(workspaceTrustModel)
	require.True(t, ok)
	require.False(t, denied.approved)
	require.True(t, strings.Contains(denied.View(), "Trust workspace"))
}

