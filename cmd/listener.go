package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/agent"
	coretools "github.com/ishi-o/golem/core/tools"
)

type terminalRendererContextKey struct{}

// terminalListener is deliberately a golem response listener rather than a
// model-specific stream callback. The core sends accumulated content, so the
// renderer emits only the newly arrived suffix.
type terminalListener struct {
	output io.Writer
	done   chan<- struct{}

	renderer *terminalRenderer
	doneOnce *sync.Once
}

func newTerminalListener(output io.Writer, done chan<- struct{}) terminalListener {
	return terminalListener{
		output:   output,
		done:     done,
		renderer: newTerminalRenderer(output),
		doneOnce: &sync.Once{},
	}
}

func (l terminalListener) OnStart(run *agent.RunContext) {
	if run == nil || l.renderer == nil {
		return
	}
	run.AddContext(func(ctx context.Context) context.Context {
		return context.WithValue(ctx, terminalRendererContextKey{}, l.renderer)
	})
}

func (l terminalListener) OnSubscribe() {
	if l.renderer != nil {
		l.renderer.Ready()
	}
}

func (l terminalListener) OnModel(model string) {
	if l.renderer != nil && strings.TrimSpace(model) != "" {
		l.renderer.Model(model)
	}
}

func (l terminalListener) OnReasoning(reasoning string) {
	if l.renderer != nil {
		l.renderer.Reasoning(reasoning)
	}
}

func (l terminalListener) OnUsage(model string, usage *schema.TokenUsage) {
	if l.renderer != nil {
		l.renderer.Usage(model, usage)
	}
}

func (l terminalListener) OnSubagent(event agent.SubagentEvent) {
	if l.renderer != nil {
		l.renderer.Subagent(event)
	}
}

func (l terminalListener) OnMessageQueued(requestID, display string) {
	if l.renderer != nil {
		l.renderer.MessageQueued(requestID, display)
	}
}

func (l terminalListener) OnQueuedMessageRead(requestIDs []string) {
	if l.renderer != nil {
		l.renderer.QueuedMessageRead(requestIDs)
	}
}

func (l terminalListener) OnError(err error) {
	if l.renderer != nil {
		l.renderer.Error(err)
		return
	}
	_, _ = fmt.Fprintf(l.output, "error: %v\n", err)
}

func (l terminalListener) OnFinished(outcome agent.Outcome) {
	if l.renderer != nil {
		l.renderer.Finish(outcome)
	} else {
		_, _ = fmt.Fprintln(l.output)
	}
	if l.done == nil {
		return
	}
	if l.doneOnce == nil {
		close(l.done)
		return
	}
	l.doneOnce.Do(func() { close(l.done) })
}

func (l terminalListener) ShouldContinue() bool { return true }

func (l terminalListener) OnContent(content string) {
	if l.renderer != nil {
		l.renderer.Content(content)
		return
	}
	_, _ = fmt.Fprint(l.output, content)
}

// scheduledDefaultListener observes scheduled task runs without sharing a
// terminalRenderer between them. A renderer owns accumulated stream state,
// while the scheduler may fire multiple tasks concurrently; each firing gets
// its own small observer and emits one completed/failed event.
type scheduledDefaultListener struct {
	agent.ListenerFuncs

	output  io.Writer
	writeMu *sync.Mutex
}

func newScheduledDefaultListener(output io.Writer) *scheduledDefaultListener {
	return &scheduledDefaultListener{output: output, writeMu: &sync.Mutex{}}
}

// attachScheduledListener adds the observer only to command runtimes backed
// by golem's Agent. The optional interface keeps cmd usable with lightweight
// runners supplied by embedders and tests.
func attachScheduledListener(config Config) {
	runner, ok := config.Runner.(DefaultListenerRunner)
	if !ok {
		return
	}
	runner.AddDefaultListener(newScheduledDefaultListener(config.Output))
}

func (l *scheduledDefaultListener) OnStart(run *agent.RunContext) {
	if l == nil || run == nil {
		return
	}
	request := run.Request()
	taskID := strings.TrimSpace(request.ScheduledTaskID)
	if taskID == "" {
		return
	}
	run.AddListener(&scheduledRunListener{
		output:  l.output,
		writeMu: l.writeMu,
		taskID:  taskID,
	})
	l.announce(fmt.Sprintf("[schedule] task %s started", taskID))
}

func (l *scheduledDefaultListener) announce(message string) {
	if l == nil || l.output == nil {
		return
	}
	if target, ok := l.output.(terminalRenderTarget); ok {
		target.sendTerminalEvent(terminalEvent{kind: terminalEventStatus, text: message})
		return
	}
	if l.writeMu == nil {
		_, _ = fmt.Fprintln(l.output, message)
		return
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	_, _ = fmt.Fprintln(l.output, message)
}

type scheduledRunListener struct {
	agent.ListenerFuncs

	output  io.Writer
	writeMu *sync.Mutex
	taskID  string

	mu       sync.Mutex
	content  string
	err      error
	model    string
	usage    *schema.TokenUsage
	finished bool
}

func (l *scheduledRunListener) OnModel(model string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.model = strings.TrimSpace(model)
	l.mu.Unlock()
}

func (l *scheduledRunListener) OnUsage(model string, usage *schema.TokenUsage) {
	if l == nil || usage == nil {
		return
	}
	l.mu.Lock()
	if strings.TrimSpace(model) != "" {
		l.model = strings.TrimSpace(model)
	}
	copy := *usage
	l.usage = &copy
	l.mu.Unlock()
}

func (l *scheduledRunListener) OnContent(content string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.content = content
	l.mu.Unlock()
}

func (l *scheduledRunListener) OnError(err error) {
	if l == nil || err == nil {
		return
	}
	l.mu.Lock()
	l.err = err
	l.mu.Unlock()
}

func (l *scheduledRunListener) OnFinished(outcome agent.Outcome) {
	if l == nil {
		return
	}
	l.mu.Lock()
	if l.finished {
		l.mu.Unlock()
		return
	}
	l.finished = true
	content := strings.TrimSpace(l.content)
	err := l.err
	model := l.model
	usage := l.usage
	l.mu.Unlock()

	event := terminalEvent{
		kind:      terminalEventScheduled,
		requestID: l.taskID,
		text:      content,
		model:     model,
		usage:     usage,
		outcome:   outcome,
	}
	if err != nil {
		event.err = err.Error()
	}
	if target, ok := l.output.(terminalRenderTarget); ok {
		target.sendTerminalEvent(event)
		return
	}

	status := strings.ToLower(strings.TrimSpace(string(outcome)))
	if status == "" {
		status = "finished"
	}
	message := fmt.Sprintf("[schedule] task %s %s", l.taskID, status)
	if err != nil {
		message += ": " + err.Error()
	}
	if content != "" {
		message += "\n" + content
	}
	l.write(message + "\n")
}

func (l *scheduledRunListener) write(message string) {
	if l == nil || l.output == nil {
		return
	}
	if l.writeMu == nil {
		_, _ = io.WriteString(l.output, message)
		return
	}
	l.writeMu.Lock()
	defer l.writeMu.Unlock()
	_, _ = io.WriteString(l.output, message)
}

var _ agent.ResponseListener = (*scheduledDefaultListener)(nil)
var _ agent.ResponseListener = (*scheduledRunListener)(nil)

// terminalRenderer understands the listener contract's accumulated content.
// It is append-only for plain output: replacing a growing multi-line frame in
// place makes terminal scrollback depend on cursor arithmetic. Interactive
// TTYs instead receive structured events and let the Bubble Tea model redraw
// the viewport safely.
type terminalRenderer struct {
	output io.Writer
	target terminalRenderTarget

	mu                sync.Mutex
	previous          string
	previousReasoning string
	subagentContent   map[string]string
	stream            string
	lineOpen          bool
	wrote             bool
	lastError         bool
}

func newTerminalRenderer(output io.Writer) *terminalRenderer {
	target, _ := output.(terminalRenderTarget)
	return &terminalRenderer{output: output, target: target, subagentContent: make(map[string]string)}
}

func (r *terminalRenderer) Content(content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if content == r.previous {
		return
	}
	if r.target != nil {
		r.previous = content
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventContent, text: content})
		return
	}
	r.lastError = false
	delta, accumulated := accumulatedDelta(r.previous, content)
	if delta == "" && accumulated {
		r.previous = content
		return
	}
	if r.previous != "" && !accumulated && r.lineOpen {
		r.newlineLocked()
	}
	r.beginStreamLocked("content")
	r.writeLocked(delta)
	r.previous = content
}

func (r *terminalRenderer) Reasoning(reasoning string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if reasoning == r.previousReasoning {
		return
	}
	if r.target != nil {
		r.previousReasoning = reasoning
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventReasoning, text: reasoning})
		return
	}
	if reasoning == "" {
		if r.previousReasoning != "" {
			r.newlineLocked()
		}
		r.previousReasoning = ""
		r.stream = ""
		return
	}
	delta, accumulated := accumulatedDelta(r.previousReasoning, reasoning)
	if delta == "" && accumulated {
		r.previousReasoning = reasoning
		return
	}
	if r.previousReasoning != "" && !accumulated && r.lineOpen {
		r.newlineLocked()
	}
	r.beginStreamLocked("reasoning")
	if r.previousReasoning == "" || !accumulated {
		r.writeLocked("[reasoning] ")
	}
	r.writeLocked(delta)
	r.previousReasoning = reasoning
	r.lastError = false
}

func (r *terminalRenderer) Usage(model string, usage *schema.TokenUsage) {
	if usage == nil {
		return
	}
	if r.target != nil {
		copy := *usage
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventUsage, model: model, usage: &copy})
		return
	}
	r.Event(formatUsage(model, usage))
}

func (r *terminalRenderer) Ready() {
	if r.target != nil {
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventReady})
		return
	}
	r.Event("[agent] ready")
}

func (r *terminalRenderer) Model(model string) {
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	if r.target != nil {
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventModel, model: model})
		return
	}
	r.Event("[model] " + model)
}

func (r *terminalRenderer) Subagent(event agent.SubagentEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.target != nil {
		if event.Usage != nil {
			copy := *event.Usage
			event.Usage = &copy
		}
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventSubagent, subagent: event})
		return
	}

	id := strings.TrimSpace(event.SubagentID)
	if id == "" {
		id = "unknown"
	}
	if event.Started() {
		description := compactText(event.Description, 180)
		if description == "" {
			r.eventLocked("[subagent] started " + id)
		} else {
			r.eventLocked(fmt.Sprintf("[subagent] started %s: %s", id, description))
		}
		return
	}

	if event.ContentSoFar != "" {
		previous := r.subagentContent[id]
		delta, accumulated := accumulatedDelta(previous, event.ContentSoFar)
		if delta != "" {
			if previous != "" && !accumulated && r.lineOpen {
				r.newlineLocked()
			}
			stream := "subagent:" + id
			r.beginStreamLocked(stream)
			if previous == "" || !accumulated {
				r.writeLocked("[subagent " + id + "] ")
			}
			r.writeLocked(delta)
		}
		r.subagentContent[id] = event.ContentSoFar
	}
	if event.Spent() {
		r.eventLocked(formatSubagentUsage(id, event.Model, event.Usage))
	}
	if event.Ended() {
		outcome := strings.ToLower(strings.TrimSpace(string(event.Outcome)))
		if outcome == "" {
			outcome = "finished"
		}
		r.eventLocked(fmt.Sprintf("[subagent] %s %s", id, outcome))
	}
}

func (r *terminalRenderer) MessageQueued(requestID, display string) {
	requestID = strings.TrimSpace(requestID)
	display = compactText(display, 180)
	if r.target != nil {
		r.target.sendTerminalEvent(terminalEvent{
			kind:      terminalEventMessageQueued,
			requestID: requestID,
			display:   display,
		})
		return
	}
	message := "[queue] message queued"
	if requestID != "" {
		message += " " + requestID
	}
	if display != "" {
		message += ": " + display
	}
	r.Event(message)
}

func (r *terminalRenderer) QueuedMessageRead(requestIDs []string) {
	ids := make([]string, 0, len(requestIDs))
	for _, id := range requestIDs {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return
	}
	if r.target != nil {
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventQueuedMessageRead, requestIDs: ids})
		return
	}
	r.Event("[queue] messages read: " + strings.Join(ids, ", "))
}

func (r *terminalRenderer) ToolStart(name, arguments string) {
	r.ToolStartCall("", name, arguments)
}

func (r *terminalRenderer) ToolStartCall(callID, name, arguments string) {
	kind := "tool"
	if isSkillTool(name) {
		kind = "skill"
	}
	if r.target != nil {
		r.target.sendTerminalEvent(terminalEvent{
			kind:      terminalEventToolStart,
			callID:    callID,
			name:      name,
			arguments: arguments,
		})
		return
	}
	message := fmt.Sprintf("[%s] -> %s", kind, strings.TrimSpace(name))
	if arguments = compactText(arguments, 240); arguments != "" && arguments != "{}" {
		message += " " + arguments
	}
	r.Event(message)
}

func (r *terminalRenderer) ToolEnd(name, result string, err error) {
	r.ToolEndCall("", name, result, err)
}

func (r *terminalRenderer) ToolEndCall(callID, name, result string, err error) {
	kind := "tool"
	if isSkillTool(name) {
		kind = "skill"
	}
	name = strings.TrimSpace(name)
	if r.target != nil {
		errText := ""
		if err != nil {
			errText = err.Error()
		}
		r.target.sendTerminalEvent(terminalEvent{
			kind:   terminalEventToolEnd,
			callID: callID,
			name:   name,
			result: result,
			err:    errText,
		})
		return
	}
	if err != nil {
		r.Event(fmt.Sprintf("[%s] xx %s: %s", kind, name, compactText(err.Error(), 240)))
		return
	}
	result = compactText(result, 240)
	if result == "" {
		r.Event(fmt.Sprintf("[%s] <- %s", kind, name))
		return
	}
	r.Event(fmt.Sprintf("[%s] <- %s: %s", kind, name, result))
}

func (r *terminalRenderer) Event(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.target != nil {
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventStatus, text: message})
		return
	}
	r.eventLocked(message)
}

func (r *terminalRenderer) eventLocked(message string) {
	r.newlineLocked()
	_, _ = fmt.Fprintln(r.output, message)
	r.stream = ""
	r.lineOpen = false
	r.wrote = true
	r.lastError = false
}

func (r *terminalRenderer) beginStreamLocked(stream string) {
	if r.stream != stream && r.lineOpen {
		r.newlineLocked()
	}
	r.stream = stream
}

func (r *terminalRenderer) writeLocked(text string) {
	if text == "" {
		return
	}
	_, _ = io.WriteString(r.output, text)
	r.lineOpen = !strings.HasSuffix(text, "\n")
	r.wrote = true
}

func (r *terminalRenderer) newlineLocked() {
	if r.lineOpen {
		_, _ = io.WriteString(r.output, "\n")
		r.lineOpen = false
		r.wrote = true
	}
}

func (r *terminalRenderer) Error(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.target != nil {
		message := "<nil>"
		if err != nil {
			message = err.Error()
		}
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventError, text: message})
		r.previous = ""
		r.previousReasoning = ""
		return
	}
	r.newlineLocked()
	_, _ = fmt.Fprintf(r.output, "error: %v\n", err)
	r.previous = ""
	r.previousReasoning = ""
	r.subagentContent = make(map[string]string)
	r.stream = ""
	r.lineOpen = false
	r.wrote = true
	r.lastError = true
}

func (r *terminalRenderer) Finish(outcomes ...agent.Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.target != nil {
		var outcome agent.Outcome
		if len(outcomes) > 0 {
			outcome = outcomes[0]
		}
		r.target.sendTerminalEvent(terminalEvent{kind: terminalEventFinished, outcome: outcome})
		r.previous = ""
		r.previousReasoning = ""
		return
	}
	if r.lastError {
		r.lastError = false
		r.previous = ""
		r.previousReasoning = ""
		r.subagentContent = make(map[string]string)
		r.stream = ""
		r.lineOpen = false
		r.wrote = false
		return
	}
	if !r.wrote {
		_, _ = io.WriteString(r.output, "\n")
	} else {
		r.newlineLocked()
	}
	r.previous = ""
	r.previousReasoning = ""
	r.subagentContent = make(map[string]string)
	r.stream = ""
	r.lineOpen = false
	r.wrote = false
}

// ToolRenderingMiddleware connects Golem's official Eino tool middleware
// hook to the active terminal renderer. It is a no-op for runs without a
// terminal listener, such as scheduled background work.
func ToolRenderingMiddleware() compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				renderer, _ := ctx.Value(terminalRendererContextKey{}).(*terminalRenderer)
				if renderer != nil && input != nil {
					renderer.ToolStart(input.Name, input.Arguments)
				}
				output, err := next(ctx, input)
				if renderer != nil && input != nil {
					result := ""
					if output != nil {
						result = output.Result
					}
					renderer.ToolEndCall(input.CallID, input.Name, result, err)
				}
				return output, err
			}
		},
	}
}

func accumulatedDelta(previous, current string) (delta string, accumulated bool) {
	if strings.HasPrefix(current, previous) {
		return current[len(previous):], true
	}
	return current, false
}

func compactText(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func formatUsage(model string, usage *schema.TokenUsage) string {
	model = strings.TrimSpace(model)
	if model == "" {
		model = "model"
	}
	message := fmt.Sprintf("[usage] %s prompt=%d completion=%d total=%d", model,
		usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens)
	if usage.PromptTokenDetails.CachedTokens > 0 {
		message += fmt.Sprintf(" cached=%d", usage.PromptTokenDetails.CachedTokens)
	}
	if usage.CompletionTokensDetails.ReasoningTokens > 0 {
		message += fmt.Sprintf(" reasoning=%d", usage.CompletionTokensDetails.ReasoningTokens)
	}
	return message
}

func formatSubagentUsage(id, model string, usage *schema.TokenUsage) string {
	message := formatUsage(model, usage)
	return strings.Replace(message, "[usage] ", "[subagent "+id+"] usage ", 1)
}

func isSkillTool(name string) bool {
	switch strings.TrimSpace(name) {
	case coretools.ToolNameListSkills, coretools.ToolNameReadSkillFile,
		coretools.ToolNameWriteSkill, coretools.ToolNameDeleteSkill:
		return true
	default:
		return false
	}
}

type listenerWithQuestions struct {
	agent.ListenerFuncs
	input  io.Reader
	output io.Writer
	reader lineReader
}

func (l listenerWithQuestions) OnStart(run *agent.RunContext) {
	reader := l.reader
	if reader == nil {
		reader = newBufferedLineReader(l.input, l.output)
	}
	run.AddQuestionHandler(terminalQuestions{reader: reader, output: l.output})
}
