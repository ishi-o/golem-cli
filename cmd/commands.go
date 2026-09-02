package cmd

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/store"
	"github.com/spf13/cobra"
)

func newRunCommand(config Config) *cobra.Command {
	var requestID string
	var sessionID string
	command := &cobra.Command{
		Use:     "run [message]",
		Aliases: []string{"chat"},
		Short:   "Send one message to the agent",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if config.Runner == nil {
				return errors.New("golem run: no agent runner configured")
			}
			reader, err := newLineReader(config.Input, config.Output, "> ")
			if err != nil {
				return fmt.Errorf("golem run: initialize input editor: %w", err)
			}
			defer reader.Close()
			config.reader = reader
			if reader.Interactive() {
				config.Output = reader.Output()
			}
			message := ""
			if len(args) == 1 {
				message = args[0]
			} else {
				if !reader.Interactive() {
					_, _ = fmt.Fprint(config.Output, "> ")
				}
				message, err = reader.ReadLine()
				if err != nil && !errors.Is(err, io.EOF) {
					return err
				}
			}
			if strings.TrimSpace(message) == "" {
				return errors.New("golem run: message is empty")
			}
			if sessionID == "" {
				sessionID = config.Session
			}
			if requestID == "" {
				requestID = sessionID
			}
			return fireAndWait(config, message, sessionID, requestID)
		},
	}
	command.Flags().StringVar(&requestID, "request-id", "", "identifier used by cancel")
	command.Flags().StringVar(&sessionID, "session", "", "conversation session to continue")
	return command
}

// newChatCommand remains as a small compatibility helper for callers that
// used the command package internally before `run` became the canonical name.
func newChatCommand(config Config) *cobra.Command { return newRunCommand(config) }

func newSessionCommand(config Config) *cobra.Command {
	var requestPrefix string
	command := &cobra.Command{
		Use:   "session [SESSION_ID]",
		Short: "Create or resume an interactive conversation session",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			sessionID := ""
			if len(args) == 1 {
				sessionID = args[0]
			}
			return runInteractiveSession(config, requestPrefix, sessionID)
		},
	}
	command.Flags().StringVar(&requestPrefix, "request-id-prefix", "", "prefix for request ids used by cancel")
	command.AddCommand(newSessionListCommand(config))
	return command
}

func runInteractiveSession(config Config, requestPrefix, sessionID string) error {
	if config.Runner == nil {
		return errors.New("golem session: no agent runner configured")
	}

	resume := strings.TrimSpace(sessionID) != ""
	var err error
	if !resume {
		sessionID, err = newSessionID()
		if err != nil {
			return fmt.Errorf("golem session: generate session id: %w", err)
		}
	} else {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			return errors.New("golem session: session id is empty")
		}
		if err := validateStoredSession(config, sessionID); err != nil {
			return err
		}
	}

	_, _ = fmt.Fprintf(config.Output, "session %s (type /exit to leave)\n", sessionID)
	if resume {
		if err := printSessionHistory(config, sessionID); err != nil {
			return err
		}
	}
	reader, err := newLineReader(config.Input, config.Output, "> ")
	if err != nil {
		return fmt.Errorf("golem session: initialize input editor: %w", err)
	}
	defer reader.Close()
	config.reader = reader
	if reader.Interactive() {
		config.Output = reader.Output()
	}
	for turn := 1; ; {
		if !reader.Interactive() {
			_, _ = fmt.Fprint(config.Output, "> ")
		}
		line, err := reader.ReadLine()
		line = strings.TrimSpace(line)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if line == "/exit" || line == "/quit" || line == ":q" {
			return nil
		}
		if handled, commandErr := executeSessionCommand(config, line); handled {
			if commandErr != nil {
				_, _ = fmt.Fprintf(config.Output, "error: %v\n", commandErr)
			}
			if errors.Is(err, io.EOF) {
				return nil
			}
			continue
		}
		if line != "" {
			requestID := fmt.Sprintf("%s-%d", sessionID, turn)
			if requestPrefix != "" {
				requestID = fmt.Sprintf("%s-%d", requestPrefix, turn)
			}
			if err := fireAndWait(config, line, sessionID, requestID); err != nil {
				return err
			}
			turn++
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
	}
}

func validateStoredSession(config Config, sessionID string) error {
	if config.SessionStore.List == nil {
		return errors.New("golem session: no session store configured")
	}
	sessions, err := config.SessionStore.List()
	if err != nil {
		return err
	}
	for _, storedID := range sessions {
		if storedID == sessionID {
			return nil
		}
	}
	return fmt.Errorf("golem session: session %q does not exist; use `golem session` to create one", sessionID)
}

func printSessionHistory(config Config, sessionID string) error {
	if config.SessionStore.History == nil {
		return errors.New("golem session: no session store configured")
	}
	messages, err := config.SessionStore.History(sessionID)
	if err != nil {
		return err
	}
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "message"
		}
		if _, err := fmt.Fprintf(config.Output, "[%s] %s\n", role, message.Content); err != nil {
			return err
		}
	}
	return nil
}

func newSessionID() (string, error) {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "session-" + hex.EncodeToString(bytes), nil
}

func newSessionListCommand(config Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List stored sessions",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if config.SessionStore.List == nil {
				return errors.New("golem session list: no session store configured")
			}
			sessions, err := config.SessionStore.List()
			if err != nil {
				return err
			}
			sort.Strings(sessions)
			if len(sessions) == 0 {
				_, err := fmt.Fprintln(config.Output, "no sessions")
				return err
			}
			for _, session := range sessions {
				if _, err := fmt.Fprintln(config.Output, session); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

func fireAndWait(config Config, message, sessionID, requestID string) error {
	done := make(chan struct{})
	listener := newTerminalListener(config.Output, done)
	request := agent.NewRequest(agent.ChatScenario, message,
		agent.WithRequestID(requestID),
		agent.WithIdentity(config.UserID, sessionID, "cli"),
		agent.WithConversation(sessionID, sessionID, requestID),
		agent.WithListener(listener),
	)
	request.Listeners = append(request.Listeners, listenerWithQuestions{input: config.Input, output: config.Output, reader: config.reader})
	if err := config.Runner.Fire(request); err != nil {
		return err
	}
	<-done
	return nil
}

func newCancelCommand(config Config) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel REQUEST_ID",
		Short: "Cancel a running request",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if config.Runner == nil {
				return errors.New("golem cancel: no agent runner configured")
			}
			if !config.Runner.Cancel(args[0]) {
				return fmt.Errorf("request %q is not running", args[0])
			}
			_, _ = fmt.Fprintf(config.Output, "cancelled %s\n", args[0])
			return nil
		},
	}
}

func newConfigCommand(config Config) *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Configure the local CLI",
		RunE:  func(*cobra.Command, []string) error { return runConfigShow(config) },
	}
	command.AddCommand(newConfigPathCommand(config), newConfigShowCommand(config), newConfigSetCommand(config))
	return command
}

func runConfigShow(config Config) error {
	if config.SettingsStore.Load == nil {
		return errors.New("golem config: no config store configured")
	}
	values, err := config.SettingsStore.Load()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(config.Output, "api-key: %s\nmodel: %s\nbase-url: %s\nsqlite-path: %s\nstorage-location: %s\n",
		maskSecret(values.APIKey), values.Model, values.BaseURL, values.SQLitePath, values.StorageLocation)
	return err
}

func newConfigPathCommand(config Config) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the local config file path",
		RunE: func(_ *cobra.Command, _ []string) error {
			if config.SettingsStore.Path == nil {
				return errors.New("golem config: no config store configured")
			}
			_, err := fmt.Fprintln(config.Output, config.SettingsStore.Path())
			return err
		},
	}
}

func newConfigShowCommand(config Config) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show non-secret local config values",
		RunE:  func(*cobra.Command, []string) error { return runConfigShow(config) },
	}
}

func newConfigSetCommand(config Config) *cobra.Command {
	var apiKey, model, baseURL, sqlitePath, storageLocation string
	var clearAPIKey bool
	command := &cobra.Command{
		Use:   "set",
		Short: "Set API/model and local storage values",
		RunE: func(command *cobra.Command, _ []string) error {
			if config.SettingsStore.Load == nil || config.SettingsStore.Save == nil {
				return errors.New("golem config: no config store configured")
			}
			if !command.Flags().Changed("api-key") && !command.Flags().Changed("model") &&
				!command.Flags().Changed("base-url") && !command.Flags().Changed("sqlite-path") &&
				!command.Flags().Changed("storage-location") && !clearAPIKey {
				return errors.New("golem config set: provide at least one value")
			}
			values, err := config.SettingsStore.Load()
			if err != nil {
				return err
			}
			if command.Flags().Changed("api-key") {
				values.APIKey = apiKey
			}
			if clearAPIKey {
				values.APIKey = ""
			}
			if command.Flags().Changed("model") {
				values.Model = model
			}
			if command.Flags().Changed("base-url") {
				values.BaseURL = baseURL
			}
			if command.Flags().Changed("sqlite-path") {
				values.SQLitePath = sqlitePath
			}
			if command.Flags().Changed("storage-location") {
				values.StorageLocation = storageLocation
			}
			if err := config.SettingsStore.Save(values); err != nil {
				return err
			}
			_, err = fmt.Fprintln(config.Output, "configuration saved")
			return err
		},
	}
	command.Flags().StringVar(&apiKey, "api-key", "", "OpenAI-compatible API key")
	command.Flags().BoolVar(&clearAPIKey, "clear-api-key", false, "remove the stored API key")
	command.Flags().StringVar(&model, "model", "", "model name")
	command.Flags().StringVar(&baseURL, "base-url", "", "OpenAI-compatible API base URL")
	command.Flags().StringVar(&sqlitePath, "sqlite-path", "", "SQLite database path")
	command.Flags().StringVar(&storageLocation, "storage-location", "", "workspace storage directory")
	return command
}

func newMCPCommand(config Config) *cobra.Command {
	command := &cobra.Command{
		Use:   "mcp",
		Short: "Manage local MCP servers",
		RunE:  func(*cobra.Command, []string) error { return runMCPList(config) },
	}
	command.AddCommand(newMCPListCommand(config), newMCPAddCommand(config), newMCPRemoveCommand(config))
	return command
}

func runMCPList(config Config) error {
	if config.MCPRegistry.List == nil {
		return errors.New("golem mcp: no registry configured")
	}
	servers, err := config.MCPRegistry.List()
	if err != nil {
		return err
	}
	sort.Slice(servers, func(i, j int) bool { return servers[i].Name < servers[j].Name })
	for _, server := range servers {
		status := "disabled"
		if server.Enabled {
			status = "enabled"
		}
		if _, err := fmt.Fprintf(config.Output, "%s\t%s\t%s\n", server.Name, status, server.URL); err != nil {
			return err
		}
	}
	return nil
}

func newMCPListCommand(config Config) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured MCP servers",
		RunE:  func(*cobra.Command, []string) error { return runMCPList(config) },
	}
}

func newSkillsCommand(config Config) *cobra.Command {
	command := &cobra.Command{
		Use:   "skills",
		Short: "List local skills",
		RunE:  func(*cobra.Command, []string) error { return runSkillsList(config) },
	}
	command.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List local skills",
		RunE:  func(*cobra.Command, []string) error { return runSkillsList(config) },
	})
	return command
}

func runSkillsList(config Config) error {
	if config.SkillRegistry.List == nil {
		return errors.New("golem skills: no skills registry configured")
	}
	skills, err := config.SkillRegistry.List()
	if err != nil {
		return err
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	for _, skill := range skills {
		if _, err := fmt.Fprintf(config.Output, "%s\t%s\n", skill.Name, skill.Description); err != nil {
			return err
		}
	}
	return nil
}

// executeSessionCommand runs the same management commands exposed at the
// root, without leaving the interactive session. It returns handled=true for
// every slash command, including an unknown one, so a typo cannot be sent to
// the model as an ordinary prompt.
func executeSessionCommand(config Config, line string) (handled bool, err error) {
	args, err := splitCommandLine(line)
	if err != nil {
		return true, err
	}
	if len(args) == 0 || !strings.HasPrefix(args[0], "/") {
		return false, nil
	}
	var command *cobra.Command
	switch args[0] {
	case "/config":
		command = newConfigCommand(config)
	case "/skills":
		command = newSkillsCommand(config)
	case "/mcp":
		command = newMCPCommand(config)
	default:
		return true, fmt.Errorf("unknown session command %q (use /config, /skills, /mcp, or /exit)", args[0])
	}
	command.SetOut(config.Output)
	command.SetErr(config.Output)
	command.SetArgs(args[1:])
	return true, command.Execute()
}

func splitCommandLine(line string) ([]string, error) {
	var args []string
	var token strings.Builder
	var quote rune
	escaped := false
	started := false
	for _, r := range strings.TrimSpace(line) {
		switch {
		case escaped:
			token.WriteRune(r)
			escaped = false
			started = true
		case r == '\\' && quote != '\'':
			escaped = true
			started = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				token.WriteRune(r)
			}
			started = true
		case r == '\'' || r == '"':
			quote = r
			started = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if started {
				args = append(args, token.String())
				token.Reset()
				started = false
			}
		default:
			token.WriteRune(r)
			started = true
		}
	}
	if escaped {
		return nil, errors.New("unfinished escape in session command")
	}
	if quote != 0 {
		return nil, errors.New("unfinished quote in session command")
	}
	if started {
		args = append(args, token.String())
	}
	return args, nil
}

func newMCPAddCommand(config Config) *cobra.Command {
	var headers []string
	var title, description string
	command := &cobra.Command{
		Use:   "add NAME URL",
		Short: "Add or replace a Streamable HTTP MCP server",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			if config.MCPRegistry.Save == nil {
				return errors.New("golem mcp: no registry configured")
			}
			parsedHeaders := make(map[string]string, len(headers))
			for _, raw := range headers {
				name, value, ok := strings.Cut(raw, "=")
				if !ok || strings.TrimSpace(name) == "" {
					return fmt.Errorf("invalid header %q; use NAME=VALUE", raw)
				}
				parsedHeaders[strings.TrimSpace(name)] = value
			}
			server := store.MCPServerConfig{
				OwnerID:     config.UserID,
				Name:        args[0],
				Transport:   store.MCPTransportStreamableHTTP,
				URL:         args[1],
				Headers:     parsedHeaders,
				Title:       title,
				Description: description,
				Enabled:     true,
			}
			if err := config.MCPRegistry.Save(server); err != nil {
				return err
			}
			_, err := fmt.Fprintf(config.Output, "saved MCP server %s\n", args[0])
			return err
		},
	}
	command.Flags().StringArrayVarP(&headers, "header", "H", nil, "HTTP header in NAME=VALUE form (repeatable)")
	command.Flags().StringVar(&title, "title", "", "client title reported to the MCP server")
	command.Flags().StringVar(&description, "description", "", "description shown in tool metadata")
	return command
}

func newMCPRemoveCommand(config Config) *cobra.Command {
	return &cobra.Command{
		Use:   "remove NAME",
		Short: "Remove a configured MCP server",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if config.MCPRegistry.Delete == nil {
				return errors.New("golem mcp: no registry configured")
			}
			if err := config.MCPRegistry.Delete(args[0]); err != nil {
				return err
			}
			_, err := fmt.Fprintf(config.Output, "removed MCP server %s\n", args[0])
			return err
		},
	}
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "(not set)"
	}
	if len(value) <= 8 {
		return "********"
	}
	return value[:4] + "..." + value[len(value)-4:]
}

func newVersionCommand(output io.Writer) *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print the CLI version", Run: func(*cobra.Command, []string) { _, _ = fmt.Fprintln(output, "golem", Version) }}
}
