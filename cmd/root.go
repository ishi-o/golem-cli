// Package cmd builds the golem command line with Cobra. The command package
// owns terminal interaction only; callers inject the core runner so the CLI
// does not choose a model provider or persistence backend on their behalf.
package cmd

import (
	"io"
	"log/slog"
	"strings"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/store"
	"github.com/spf13/cobra"

	_ "embed"
)

//go:embed version.txt
var versionFile string

// Version is the CLI version reported by `golem version`
var Version = "v" + versionFile

// Runner is the small portion of the core runtime the CLI needs.
type Runner interface {
	Fire(agent.Request) error
	Cancel(requestID string) bool
}

// SettingsValues is the local configuration surface used by `golem config`.
// Keeping it small makes it easy for embedders to provide a secure config
// store of their own.
type SettingsValues struct {
	APIKey          string
	Model           string
	BaseURL         string
	SQLitePath      string
	StorageLocation string
}

// SettingsStore supplies the config subcommands without making cmd depend on
// the bootstrap package.
type SettingsStore struct {
	Path func() string
	Load func() (SettingsValues, error)
	Save func(SettingsValues) error
}

// MCPRegistry supplies local MCP configuration commands. The runtime copies
// these records into golem's SQLX store when it starts.
type MCPRegistry struct {
	List   func() ([]store.MCPServerConfig, error)
	Save   func(store.MCPServerConfig) error
	Delete func(name string) error
}

// SkillInfo is the display shape used by the local skills command.
type SkillInfo struct {
	Name        string
	Description string
}

// SkillRegistry supplies the local skills command. The agent itself still
// receives golem's full skill tool family for every run.
type SkillRegistry struct {
	List func() ([]SkillInfo, error)
}

// SessionMessage is the display shape used when resuming a session.
type SessionMessage struct {
	Role    string
	Content string
}

// SessionStore supplies persisted conversation ids and history to the CLI.
type SessionStore struct {
	List    func() ([]string, error)
	History func(string) ([]SessionMessage, error)
}

// Config configures the command tree.
type Config struct {
	Runner        Runner
	UserID        string
	Session       string
	Input         io.Reader
	Output        io.Writer
	Logger        *slog.Logger
	SettingsStore SettingsStore
	MCPRegistry   MCPRegistry
	SkillRegistry SkillRegistry
	SessionStore  SessionStore

	reader lineReader
}

// NewRoot creates the root command and its subcommands.
func NewRoot(config Config) *cobra.Command {
	if config.Input == nil {
		config.Input = strings.NewReader("")
	}
	if config.Output == nil {
		config.Output = io.Discard
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.UserID == "" {
		config.UserID = "local"
	}
	if config.Session == "" {
		config.Session = "local"
	}

	root := &cobra.Command{
		Use:          "golem",
		Short:        "Run a golem agent",
		SilenceUsage: true,
		Args:         cobra.NoArgs,
		RunE:         func(*cobra.Command, []string) error { return runInteractiveSession(config, "", "") },
	}
	root.AddCommand(
		newRunCommand(config),
		newSessionCommand(config),
		newCancelCommand(config),
		newConfigCommand(config),
		newMCPCommand(config),
		newSkillsCommand(config),
		newVersionCommand(config.Output),
	)
	return root
}
