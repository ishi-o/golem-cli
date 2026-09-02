package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/ishi-o/golem-cli/cmd"
	"github.com/ishi-o/golem-cli/internal/bootstrap"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	settings, err := bootstrap.LoadSettings()
	if err != nil {
		logger.Error("load configuration", "err", err)
		os.Exit(1)
	}
	cfg := settings.Config
	ctx := context.Background()
	var runtime *bootstrap.Runtime
	if commandNeedsRuntime(os.Args[1:]) {
		runtime, err = bootstrap.New(ctx, cfg, logger,
			bootstrap.WithToolMiddleware(cmd.ToolRenderingMiddleware()))
		if err != nil {
			logger.Error("bootstrap command runtime", "err", err, "config", cfg.String())
			os.Exit(1)
		}
		defer func() {
			if err := runtime.Close(ctx); err != nil {
				logger.Error("close command runtime", "err", err)
			}
		}()
	}

	var runner cmd.Runner
	if runtime != nil {
		runner = runtime.Agent
	}
	mcpRegistry := cmd.MCPRegistry{
		List:   bootstrap.ListMCPServers,
		Save:   bootstrap.SaveMCPServer,
		Delete: bootstrap.DeleteMCPServer,
	}
	if runtime != nil {
		mcpRegistry = cmd.MCPRegistry{
			List:   runtime.ListMCPServers,
			Save:   runtime.SaveMCPServer,
			Delete: runtime.DeleteMCPServer,
		}
	}

	root := cmd.NewRoot(cmd.Config{
		Runner: runner,
		Input:  os.Stdin,
		Output: os.Stdout,
		Logger: logger,
		UserID: "local",
		// `run` keeps its stable default for one-shot continuation; `session`
		// generates a new id unless an existing generated id is supplied.
		Session: "local",
		SettingsStore: cmd.SettingsStore{
			Path: bootstrap.ConfigPath,
			Load: func() (cmd.SettingsValues, error) {
				values, err := bootstrap.LoadSettingsValues()
				if err != nil {
					return cmd.SettingsValues{}, err
				}
				return cmd.SettingsValues{
					APIKey:          values.APIKey,
					Model:           values.Model,
					BaseURL:         values.BaseURL,
					SQLitePath:      values.SQLitePath,
					StorageLocation: values.StorageLocation,
				}, nil
			},
			Save: func(values cmd.SettingsValues) error {
				return bootstrap.SaveSettingsValues(bootstrap.SettingsValues{
					APIKey:          values.APIKey,
					Model:           values.Model,
					BaseURL:         values.BaseURL,
					SQLitePath:      values.SQLitePath,
					StorageLocation: values.StorageLocation,
				})
			},
		},
		MCPRegistry: mcpRegistry,
		SkillRegistry: cmd.SkillRegistry{
			List: func() ([]cmd.SkillInfo, error) {
				items, err := bootstrap.ListSkills("local")
				if err != nil {
					return nil, err
				}
				result := make([]cmd.SkillInfo, 0, len(items))
				for _, item := range items {
					result = append(result, cmd.SkillInfo{Name: item.Name, Description: item.Description})
				}
				return result, nil
			},
		},
		SessionStore: cmd.SessionStore{
			List: bootstrap.ListSessions,
			History: func(sessionID string) ([]cmd.SessionMessage, error) {
				items, err := bootstrap.LoadSessionHistory(sessionID)
				if err != nil {
					return nil, err
				}
				result := make([]cmd.SessionMessage, 0, len(items))
				for _, item := range items {
					result = append(result, cmd.SessionMessage{Role: item.Role, Content: item.Content})
				}
				return result, nil
			},
		},
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		logger.Error("command failed", "err", err, "config", cfg.String())
		os.Exit(1)
	}
}

func commandNeedsRuntime(args []string) bool {
	if len(args) == 0 {
		return true
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" || args[0] == "version" {
		return false
	}
	switch strings.TrimSpace(args[0]) {
	case "run", "chat", "cancel":
		for _, arg := range args[1:] {
			if arg == "--help" || arg == "-h" {
				return false
			}
		}
		return true
	case "session":
		for _, arg := range args[1:] {
			if arg == "--help" || arg == "-h" {
				return false
			}
		}
		if len(args) > 1 && args[1] == "list" {
			return false
		}
		return true
	default:
		return false
	}
}
