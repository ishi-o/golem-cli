package cmd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ishi-o/golem-cli/cmd"
	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/store"
	"github.com/stretchr/testify/require"
)

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Fire(request agent.Request) error {
	close(r.started)
	go func() {
		<-r.release
		for _, listener := range request.Listeners {
			listener.OnFinished(agent.OutcomeCompleted)
		}
	}()
	return nil
}

func (*blockingRunner) Cancel(string) bool { return false }

func TestChatWaitsForRunCompletion(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	var output strings.Builder
	root := cmd.NewRoot(cmd.Config{
		Runner:  runner,
		Output:  &output,
		UserID:  "test-user",
		Session: "test-session",
	})
	root.SetArgs([]string{"chat", "hello"})

	done := make(chan error, 1)
	go func() { done <- root.Execute() }()

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("chat did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("chat returned before the listener finished: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(runner.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("chat did not return after the listener finished")
	}
	require.Contains(t, output.String(), "\n")
}

type recordingRunner struct {
	requests []agent.Request
}

func (r *recordingRunner) Fire(request agent.Request) error {
	r.requests = append(r.requests, request)
	for _, listener := range request.Listeners {
		listener.OnFinished(agent.OutcomeCompleted)
	}
	return nil
}

func (*recordingRunner) Cancel(string) bool { return false }

func TestSessionReusesConversation(t *testing.T) {
	runner := &recordingRunner{}
	var output strings.Builder
	root := cmd.NewRoot(cmd.Config{
		Runner:  runner,
		Input:   strings.NewReader("first\nsecond\n/exit\n"),
		Output:  &output,
		UserID:  "test-user",
		Session: "default",
	})
	root.SetArgs([]string{"session"})

	require.NoError(t, root.Execute())
	require.Len(t, runner.requests, 2)
	parts := strings.Fields(output.String())
	require.GreaterOrEqual(t, len(parts), 2)
	sessionID := parts[1]
	require.True(t, strings.HasPrefix(sessionID, "session-"))
	for _, request := range runner.requests {
		require.Equal(t, sessionID, request.ConversationID)
		require.Equal(t, sessionID, request.ChatID)
	}
	require.Equal(t, sessionID+"-1", runner.requests[0].RequestID)
	require.Equal(t, sessionID+"-2", runner.requests[1].RequestID)
	require.Contains(t, output.String(), "session "+sessionID)
}

func TestSessionRunsSlashCommands(t *testing.T) {
	runner := &recordingRunner{}
	var output strings.Builder
	var saved cmd.SettingsValues
	root := cmd.NewRoot(cmd.Config{
		Runner: runner,
		Input: strings.NewReader(
			"/config set --model 'new model'\n" +
				"/skills\n" +
				"/mcp\n" +
				"hello\n/exit\n",
		),
		Output:  &output,
		UserID:  "test-user",
		Session: "default",
		SettingsStore: cmd.SettingsStore{
			Load: func() (cmd.SettingsValues, error) {
				return cmd.SettingsValues{Model: "old model"}, nil
			},
			Save: func(values cmd.SettingsValues) error {
				saved = values
				return nil
			},
		},
		SkillRegistry: cmd.SkillRegistry{
			List: func() ([]cmd.SkillInfo, error) {
				return []cmd.SkillInfo{{Name: "release", Description: "prepare a release"}}, nil
			},
		},
		MCPRegistry: cmd.MCPRegistry{
			List: func() ([]store.MCPServerConfig, error) {
				return []store.MCPServerConfig{{Name: "local-tools", URL: "http://127.0.0.1:3000/mcp", Enabled: true}}, nil
			},
		},
	})
	root.SetArgs([]string{"session"})

	require.NoError(t, root.Execute())
	require.Equal(t, "new model", saved.Model)
	require.Len(t, runner.requests, 1)
	require.Contains(t, output.String(), "configuration saved")
	require.Contains(t, output.String(), "release\tprepare a release")
	require.Contains(t, output.String(), "local-tools\tenabled\thttp://127.0.0.1:3000/mcp")
}

func TestSessionListAndResume(t *testing.T) {
	var output strings.Builder
	root := cmd.NewRoot(cmd.Config{
		Output: &output,
		SessionStore: cmd.SessionStore{
			List: func() ([]string, error) {
				return []string{"session-b", "session-a"}, nil
			},
			History: func(sessionID string) ([]cmd.SessionMessage, error) {
				require.Equal(t, "session-a", sessionID)
				return []cmd.SessionMessage{
					{Role: "user", Content: "hello"},
					{Role: "assistant", Content: "hi"},
				}, nil
			},
		},
	})

	root.SetArgs([]string{"session", "list"})
	require.NoError(t, root.Execute())
	require.Equal(t, "session-a\nsession-b\n", output.String())

	output.Reset()
	runner := &recordingRunner{}
	root = cmd.NewRoot(cmd.Config{
		Runner: runner,
		Input:  strings.NewReader("/exit\n"),
		Output: &output,
		SessionStore: cmd.SessionStore{
			List: func() ([]string, error) {
				return []string{"session-a"}, nil
			},
			History: func(sessionID string) ([]cmd.SessionMessage, error) {
				require.Equal(t, "session-a", sessionID)
				return []cmd.SessionMessage{
					{Role: "user", Content: "hello"},
					{Role: "assistant", Content: "hi"},
				}, nil
			},
		},
	})
	root.SetArgs([]string{"session", "session-a"})
	require.NoError(t, root.Execute())
	require.Equal(t, "session session-a (type /exit to leave)\n[user] hello\n[assistant] hi\n> ", output.String())
	require.Empty(t, runner.requests)

	root = cmd.NewRoot(cmd.Config{
		Runner: &recordingRunner{},
		Input:  strings.NewReader("/exit\n"),
		Output: &output,
		SessionStore: cmd.SessionStore{
			List: func() ([]string, error) { return []string{"session-a"}, nil },
		},
	})
	root.SetArgs([]string{"session", "not-generated-by-golem"})
	require.ErrorContains(t, root.Execute(), "does not exist")
}
