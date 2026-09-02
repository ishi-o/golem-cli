package cmd

import (
	"bufio"
	"io"
	"os"
	"strings"
)

// lineReader is the small input surface shared by sessions and inline
// question handlers. Interactive terminals use the Bubble Tea editor; pipes
// and tests use the buffered implementation.
type lineReader interface {
	ReadLine() (string, error)
	Output() io.Writer
	Interactive() bool
	Close() error
}

// submitHandlerReader is implemented by interactive readers that can route a
// submission directly to the live agent run. Buffered readers intentionally
// do not implement it: their caller consumes the next line synchronously.
type submitHandlerReader interface {
	SetSubmitHandler(func(string) bool)
}

type lineSubmitter interface {
	submitLine(string)
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
	ui *terminalUI
}

func (r *terminalLineReader) ReadLine() (string, error) { return r.ui.readLine() }

func (r *terminalLineReader) SetSubmitHandler(handler func(string) bool) {
	if r == nil || r.ui == nil {
		return
	}
	r.ui.setSubmitHandler(handler)
}

func (r *terminalLineReader) submitLine(line string) {
	if r == nil || r.ui == nil {
		return
	}
	r.ui.submitLine(line)
}

func (r *terminalLineReader) Output() io.Writer { return r.ui.writer }

func (*terminalLineReader) Interactive() bool { return true }

func (r *terminalLineReader) Close() error {
	if r == nil || r.ui == nil {
		return nil
	}
	return r.ui.close()
}

func newLineReader(input io.Reader, output io.Writer, prompt string) (lineReader, error) {
	_, inputIsTerminal := terminalInputFile(input)
	if !inputIsTerminal || !isTerminalWriter(output) {
		return newBufferedLineReader(input, output), nil
	}

	ui := newTerminalUI(input, output)
	return &terminalLineReader{
		ui: ui,
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
