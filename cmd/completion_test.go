package cmd

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func TestSessionCompletionUsesStoredIDs(t *testing.T) {
	command := newSessionCommand(Config{
		SessionStore: SessionStore{
			List: func() ([]string, error) {
				return []string{"session-z", "session-a", "other"}, nil
			},
		},
	})

	completions, directive := command.ValidArgsFunction(command, nil, "session-")
	require.Equal(t, []string{"session-a", "session-z"}, completions)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)

	completions, directive = command.ValidArgsFunction(command, []string{"session-a"}, "")
	require.Empty(t, completions)
	require.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
}
