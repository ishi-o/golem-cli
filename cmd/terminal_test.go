package cmd

import (
	"io"
	"strings"
	"testing"

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
