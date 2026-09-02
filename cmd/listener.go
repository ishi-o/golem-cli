package cmd

import (
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/agent"
)

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

func (l terminalListener) OnStart(*agent.RunContext)          {}
func (l terminalListener) OnSubscribe()                       {}
func (l terminalListener) OnModel(string)                     {}
func (l terminalListener) OnReasoning(string)                 {}
func (l terminalListener) OnUsage(string, *schema.TokenUsage) {}
func (l terminalListener) OnSubagent(agent.SubagentEvent)     {}
func (l terminalListener) OnMessageQueued(string, string)     {}
func (l terminalListener) OnQueuedMessageRead([]string)       {}
func (l terminalListener) OnError(err error) {
	if l.renderer != nil {
		l.renderer.Error(err)
		return
	}
	_, _ = fmt.Fprintf(l.output, "error: %v\n", err)
}
func (l terminalListener) OnFinished(agent.Outcome) {
	if l.renderer != nil {
		l.renderer.Finish()
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

// terminalRenderer understands the listener contract's accumulated content.
// It is append-only: replacing a growing multi-line frame in place makes
// terminal scrollback depend on cursor arithmetic and becomes unreadable as a
// response grows. Appending the new suffix works equally well for a TTY,
// redirected output, and the x/term line editor.
type terminalRenderer struct {
	output io.Writer

	mu        sync.Mutex
	previous  string
	lastError bool
}

func newTerminalRenderer(output io.Writer) *terminalRenderer {
	return &terminalRenderer{output: output}
}

func (r *terminalRenderer) Content(content string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if content == r.previous {
		return
	}
	r.lastError = false
	if strings.HasPrefix(content, r.previous) {
		_, _ = io.WriteString(r.output, content[len(r.previous):])
	} else {
		if r.previous != "" && !strings.HasSuffix(r.previous, "\n") {
			_, _ = io.WriteString(r.output, "\n")
		}
		_, _ = io.WriteString(r.output, content)
	}
	r.previous = content
}

func (r *terminalRenderer) Error(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.previous != "" && !strings.HasSuffix(r.previous, "\n") {
		_, _ = io.WriteString(r.output, "\n")
	}
	_, _ = fmt.Fprintf(r.output, "error: %v\n", err)
	r.previous = ""
	r.lastError = true
}

func (r *terminalRenderer) Finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastError {
		r.lastError = false
		r.previous = ""
		return
	}
	if r.previous == "" || !strings.HasSuffix(r.previous, "\n") {
		_, _ = io.WriteString(r.output, "\n")
	}
	r.previous = ""
}

type listenerWithQuestions struct {
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
func (listenerWithQuestions) OnSubscribe()                       {}
func (listenerWithQuestions) OnModel(string)                     {}
func (listenerWithQuestions) OnReasoning(string)                 {}
func (listenerWithQuestions) OnContent(string)                   {}
func (listenerWithQuestions) OnUsage(string, *schema.TokenUsage) {}
func (listenerWithQuestions) OnSubagent(agent.SubagentEvent)     {}
func (listenerWithQuestions) OnMessageQueued(string, string)     {}
func (listenerWithQuestions) OnQueuedMessageRead([]string)       {}
func (listenerWithQuestions) OnError(error)                      {}
func (listenerWithQuestions) OnFinished(agent.Outcome)           {}
func (listenerWithQuestions) ShouldContinue() bool               { return true }
