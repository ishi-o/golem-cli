package cmd

import (
	"context"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/agent"
	"github.com/stretchr/testify/require"
)

func TestTerminalRendererAppendsAccumulatedContent(t *testing.T) {
	var output strings.Builder
	renderer := newTerminalRenderer(&output)

	renderer.Content("hello")
	renderer.Content("hello\nworld")
	renderer.Content("hello\nworld!")
	renderer.Finish()

	require.Equal(t, "hello\nworld!\n", output.String())
	require.NotContains(t, output.String(), "\033[")
}

func TestTerminalRendererSeparatesUnexpectedReset(t *testing.T) {
	var output strings.Builder
	renderer := newTerminalRenderer(&output)

	renderer.Content("first")
	renderer.Content("replacement")
	renderer.Finish()

	require.Equal(t, "first\nreplacement\n", output.String())
}

func TestBufferedLineReaderKeepsEOFLine(t *testing.T) {
	reader := newBufferedLineReader(strings.NewReader("hello"), &strings.Builder{})

	line, err := reader.ReadLine()
	require.ErrorIs(t, err, io.EOF)
	require.Equal(t, "hello", line)
}

func TestTerminalListenerRendersRunEvents(t *testing.T) {
	var output strings.Builder
	renderer := newTerminalRenderer(&output)
	listener := terminalListener{renderer: renderer}

	listener.OnModel("gpt-test")
	listener.OnReasoning("checking the workspace")
	listener.OnReasoning("checking the workspace before editing")
	listener.OnUsage("gpt-test", &schema.TokenUsage{
		PromptTokens: 12, CompletionTokens: 8, TotalTokens: 20,
		PromptTokenDetails:      schema.PromptTokenDetails{CachedTokens: 3},
		CompletionTokensDetails: schema.CompletionTokensDetails{ReasoningTokens: 2},
	})
	listener.OnSubagent(agent.SubagentEvent{SubagentID: "sub-1", Description: "inspect files"})
	listener.OnSubagent(agent.SubagentEvent{SubagentID: "sub-1", ContentSoFar: "found"})
	listener.OnSubagent(agent.SubagentEvent{SubagentID: "sub-1", ContentSoFar: "found the issue"})
	listener.OnSubagent(agent.SubagentEvent{SubagentID: "sub-1", ContentSoFar: "found the issue", Outcome: agent.OutcomeCompleted})
	listener.OnMessageQueued("req-2", "follow-up")
	listener.OnQueuedMessageRead([]string{"req-2"})
	renderer.ToolStart("ListSkills", `{"workspace":"local"}`)
	renderer.ToolEnd("ListSkills", `{"skills":[{"name":"release"}]}`, nil)
	renderer.ToolStart("ReadFile", `{"file":"README.md"}`)
	renderer.ToolEnd("ReadFile", "file contents", nil)
	listener.OnContent("final answer")
	listener.OnFinished(agent.OutcomeCompleted)

	got := output.String()
	for _, expected := range []string{
		"[model] gpt-test",
		"[reasoning] checking the workspace before editing",
		"[usage] gpt-test prompt=12 completion=8 total=20 cached=3 reasoning=2",
		"[subagent] started sub-1: inspect files",
		"[subagent sub-1] found the issue",
		"[subagent] sub-1 completed",
		"[queue] message queued req-2: follow-up",
		"[queue] messages read: req-2",
		"[skill] -> ListSkills",
		"[skill] <- ListSkills:",
		"[tool] -> ReadFile",
		"[tool] <- ReadFile: file contents",
		"final answer",
	} {
		require.Contains(t, got, expected)
	}
	require.NotContains(t, got, "\033[")
}

func TestScheduledRunListenerReportsResult(t *testing.T) {
	var output strings.Builder
	listener := &scheduledRunListener{output: &output, taskID: "task-1"}
	listener.OnModel("gpt-test")
	listener.OnUsage("gpt-test", &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7})
	listener.OnContent("the scheduled work is done")
	listener.OnFinished(agent.OutcomeCompleted)

	require.Equal(t, "[schedule] task task-1 completed\nthe scheduled work is done\n", output.String())
}

func TestTerminalModelRendersScheduledResult(t *testing.T) {
	model := newTerminalModel(make(chan terminalLineResult, 1))
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model.applyEvent(terminalEvent{
		kind:      terminalEventScheduled,
		requestID: "task-1",
		text:      "# Finished\n\nThe scheduled task ran.",
		model:     "gpt-test",
		usage:     &schema.TokenUsage{PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7},
		outcome:   agent.OutcomeCompleted,
	})
	model.rebuildViewport()

	view := model.View()
	require.Contains(t, view, "⏰ scheduled · task-1")
	require.Contains(t, view, "completed · gpt-test: 4 in / 3 out / 7 total")
	require.Contains(t, view, "Finished")
}

func TestToolRenderingMiddlewareUsesRunRenderer(t *testing.T) {
	var output strings.Builder
	renderer := newTerminalRenderer(&output)
	ctx := context.WithValue(context.Background(), terminalRendererContextKey{}, renderer)
	middleware := ToolRenderingMiddleware()
	endpoint := middleware.Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: "ok"}, nil
	})

	_, err := endpoint(ctx, &compose.ToolInput{Name: "ListSkills", Arguments: "{}"})
	require.NoError(t, err)
	require.Contains(t, output.String(), "[skill] -> ListSkills")
	require.Contains(t, output.String(), "[skill] <- ListSkills: ok")
}

func TestTerminalModelRendersFoldableBlocksAndMarkdown(t *testing.T) {
	model := newTerminalModel(make(chan terminalLineResult, 1))
	model.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	model.applyEvent(terminalEvent{kind: terminalEventUser, text: "show me the result"})
	model.applyEvent(terminalEvent{kind: terminalEventReasoning, text: "I should inspect the tool output first."})
	model.applyEvent(terminalEvent{
		kind:      terminalEventToolStart,
		callID:    "call-1",
		name:      "ListSkills",
		arguments: `{"workspace":"local"}`,
	})
	model.applyEvent(terminalEvent{
		kind:   terminalEventToolEnd,
		callID: "call-1",
		name:   "ListSkills",
		result: `{"skills":[{"name":"release"}]}`,
	})
	model.applyEvent(terminalEvent{kind: terminalEventSubagent, subagent: agent.SubagentEvent{
		SubagentID: "sub-1", Description: "inspect", Model: "worker", Usage: &schema.TokenUsage{
			PromptTokens: 4, CompletionTokens: 3, TotalTokens: 7,
		},
	}})
	model.applyEvent(terminalEvent{kind: terminalEventSubagent, subagent: agent.SubagentEvent{
		SubagentID: "sub-1", ContentSoFar: "**found it**", Outcome: agent.OutcomeCompleted,
	}})
	model.applyEvent(terminalEvent{kind: terminalEventContent, text: "# Done\n\nThe **release** skill is available."})
	model.applyEvent(terminalEvent{
		kind:  terminalEventUsage,
		model: "gpt-test",
		usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	})
	model.rebuildViewport()

	view := model.View()
	require.Contains(t, view, "✦ answer")
	require.Contains(t, view, "Done")
	require.Contains(t, view, "▸ ✦ skill · ListSkills")
	require.NotContains(t, view, "arguments:")
	require.Contains(t, view, "gpt-test: 10 in / 5 out / 15 total")
	require.Contains(t, view, "▸ ◎ subagent · sub-1")
	require.Contains(t, view, "completed · worker: 4 in / 3 out / 7 total")

	model.focusIndex = 2
	model.toggleFocusedFold()
	model.rebuildViewport()
	view = model.View()
	require.Contains(t, view, "arguments:")
	require.Contains(t, view, "release")
	require.Equal(t, 1, strings.Count(view, "arguments:"))
}

func TestTerminalModelQuestionUsesArrowSelection(t *testing.T) {
	lines := make(chan terminalLineResult, 1)
	model := newTerminalModel(lines)
	model.applyEvent(terminalEvent{
		kind:    terminalEventQuestion,
		text:    "Which environment?",
		options: []string{"local", "remote"},
	})

	require.True(t, model.questionActive)
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	require.Nil(t, command)
	_, command = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.NotNil(t, command)
	command()

	result := <-lines
	require.Equal(t, "remote", result.line)
	require.NoError(t, result.err)
	require.False(t, model.questionActive)
	require.Contains(t, model.renderBlocks(), "remote")
}

func TestTerminalModelUsesOneInputPromptMarker(t *testing.T) {
	model := newTerminalModel(make(chan terminalLineResult, 1))
	view := model.View()
	require.Equal(t, 1, strings.Count(view, "›"))
}

func TestTerminalModelQueuesInputWhileBusy(t *testing.T) {
	lines := make(chan terminalLineResult, 1)
	model := newTerminalModel(lines)
	model.busy = true
	model.input.Focus()

	var submitted string
	model.submitHandler = func(line string) bool {
		submitted = line
		return true
	}
	model.input.SetValue("follow up while thinking")

	_, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	require.Nil(t, command)
	require.Equal(t, "follow up while thinking", submitted)
	select {
	case result := <-lines:
		t.Fatalf("queued input was returned as a new line: %#v", result)
	default:
	}
	require.Empty(t, model.input.Value())
}

func TestTerminalModelScrollsWithMouseWheel(t *testing.T) {
	model := newTerminalModel(make(chan terminalLineResult, 1))
	model.viewport.Width = 20
	model.viewport.Height = 3
	model.viewport.SetContent("one\ntwo\nthree\nfour\nfive\nsix")

	_, command := model.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelDown,
	})
	require.Nil(t, command)
	require.Equal(t, 3, model.viewport.YOffset)

	_, command = model.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonWheelUp,
	})
	require.Nil(t, command)
	require.Equal(t, 0, model.viewport.YOffset)
}

type queueTestReader struct {
	output  io.Writer
	handler func(string) bool
}

func (r *queueTestReader) ReadLine() (string, error) { return "", io.EOF }
func (r *queueTestReader) Output() io.Writer         { return r.output }
func (*queueTestReader) Interactive() bool           { return true }
func (*queueTestReader) Close() error                { return nil }
func (r *queueTestReader) SetSubmitHandler(handler func(string) bool) {
	r.handler = handler
}
func (*queueTestReader) submitLine(string) {}

type queueTestRunner struct {
	reader    *queueTestReader
	fired     []agent.Request
	queued    []agent.Request
	display   string
	queueDone chan struct{}
}

func (r *queueTestRunner) Fire(request agent.Request) error {
	r.fired = append(r.fired, request)
	if r.reader.handler != nil {
		r.reader.handler("follow up")
		<-r.queueDone
	}
	for _, listener := range request.Listeners {
		listener.OnFinished(agent.OutcomeCompleted)
	}
	return nil
}

func (r *queueTestRunner) FireOrQueue(request agent.Request, text func() string, display string) bool {
	r.queued = append(r.queued, request)
	r.display = display + "|" + text()
	close(r.queueDone)
	return true
}

func (*queueTestRunner) Cancel(string) bool { return false }

func TestFireAndWaitOffersFollowUpToLiveRun(t *testing.T) {
	reader := &queueTestReader{output: &strings.Builder{}}
	runner := &queueTestRunner{reader: reader}
	runner.queueDone = make(chan struct{})
	config := Config{
		Runner: runner,
		Input:  strings.NewReader(""),
		Output: reader.output,
		UserID: "test-user",
		reader: reader,
	}

	require.NoError(t, fireAndWait(config, "first", "session-1", "request-1"))
	require.Len(t, runner.fired, 1)
	require.Len(t, runner.queued, 1)
	require.Equal(t, "request-1-queued-1", runner.queued[0].RequestID)
	require.Equal(t, "session-1", runner.queued[0].ConversationID)
	require.Equal(t, "follow up|follow up", runner.display)
	require.Nil(t, reader.handler)
}
