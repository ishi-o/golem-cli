package cmd

import (
	"bufio"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// lineReader is the small input surface shared by sessions and inline
// question handlers. The terminal implementation is backed by x/term, which
// handles raw mode, history, and cursor movement; pipes and tests use the
// buffered implementation.
type lineReader interface {
	ReadLine() (string, error)
	Output() io.Writer
	Interactive() bool
	Close() error
}

type bufferedLineReader struct {
	reader *bufio.Reader
	output io.Writer
}

func newBufferedLineReader(input io.Reader, output io.Writer) lineReader {
	return &bufferedLineReader{reader: bufferedReader(input), output: output}
}

func (r *bufferedLineReader) ReadLine() (string, error) {
	line, err := r.reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), err
}

func (r *bufferedLineReader) Output() io.Writer { return r.output }

func (*bufferedLineReader) Interactive() bool { return false }

func (*bufferedLineReader) Close() error { return nil }

type terminalLineReader struct {
	terminal *term.Terminal
	fd       int
	state    *term.State
	closed   bool
}

func (r *terminalLineReader) ReadLine() (string, error) { return r.terminal.ReadLine() }

func (r *terminalLineReader) Output() io.Writer { return r.terminal }

func (*terminalLineReader) Interactive() bool { return true }

func (r *terminalLineReader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	return term.Restore(r.fd, r.state)
}

type terminalReadWriter struct {
	input  io.Reader
	output io.Writer
}

func (r terminalReadWriter) Read(p []byte) (int, error)  { return r.input.Read(p) }
func (r terminalReadWriter) Write(p []byte) (int, error) { return r.output.Write(p) }

func newLineReader(input io.Reader, output io.Writer, prompt string) (lineReader, error) {
	inputFile, inputIsTerminal := terminalInputFile(input)
	if !inputIsTerminal || !isTerminalWriter(output) {
		return newBufferedLineReader(input, output), nil
	}

	state, err := term.MakeRaw(int(inputFile.Fd()))
	if err != nil {
		return nil, err
	}
	return &terminalLineReader{
		terminal: term.NewTerminal(terminalReadWriter{input: input, output: output}, prompt),
		fd:       int(inputFile.Fd()),
		state:    state,
	}, nil
}

func terminalInputFile(input io.Reader) (*os.File, bool) {
	file, ok := input.(*os.File)
	if !ok {
		return nil, false
	}
	info, err := file.Stat()
	return file, err == nil && info.Mode()&os.ModeCharDevice != 0
}

func isTerminalWriter(output io.Writer) bool {
	file, ok := output.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
