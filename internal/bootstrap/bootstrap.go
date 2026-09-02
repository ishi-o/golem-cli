// Package bootstrap builds the default runtime for the CLI from environment
// variables, so the command wiring can share setup without making provider
// and driver dependencies part of the public core module.
package bootstrap

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/compose"
	cliMCP "github.com/ishi-o/golem-cli/internal/mcp"
	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/schedule"
	"github.com/ishi-o/golem/core/store"
	"github.com/ishi-o/golem/core/subagent"
	"github.com/ishi-o/golem/core/tools"
	sqlxstore "github.com/ishi-o/golem/store/sqlx"
	"github.com/jmoiron/sqlx"
	"modernc.org/sqlite"
)

// The pure-Go driver keeps the CLI cgo-free so release binaries cross-
// compile. sqlx resolves bindvars by driver name and only knows "sqlite3",
// so the driver is registered under that name.
func init() { sql.Register("sqlite3", &sqlite.Driver{}) }

const (
	apiKeyEnv  = "OPENAI_API_KEY"
	modelEnv   = "OPENAI_MODEL"
	baseURLEnv = "OPENAI_BASE_URL"
	sqliteEnv  = "GOLEM_SQLITE_PATH"
)

// Runtime owns the resources created for an application process.
type Runtime struct {
	Agent *agent.Agent
	// Runner fires the user's scheduled tasks.
	Runner *schedule.Runner
	// sandbox is the env-selected shell sandbox (GOLEM_SANDBOX); nil when off.
	// The Docker backend's Close removes its containers.
	sandbox tools.Sandbox
	db      *sqlx.DB
	// mcpServers is the same SQLX repository used by the live agent. Keeping
	// it here lets management commands inside a session take effect on the
	// next turn instead of waiting for a process restart.
	mcpServers store.MCPServerConfigStore
	// scheduler is owned only when the CLI created the local implementation.
	scheduler *localScheduler
}

// Option configures the runtime during construction.
type Option func(*options)

type options struct {
	// Scheduler lets an embedding application replace the CLI's local cron
	// implementation. The default is a local robfig/cron scheduler.
	scheduler        schedule.Scheduler
	withoutScheduler bool
	toolMiddlewares  []compose.ToolMiddleware
}

// WithScheduler replaces the default local scheduler with an application
// supplied implementation.
func WithScheduler(s schedule.Scheduler) Option {
	return func(o *options) { o.scheduler = s }
}

// WithoutScheduler disables schedule tools. This is primarily useful to
// embedders that do not want a long-lived scheduler in their process.
func WithoutScheduler() Option {
	return func(o *options) { o.withoutScheduler = true }
}

// WithToolMiddleware adds an Eino tool middleware to every run composed by
// this runtime. A middleware can use values placed in the per-run context by
// an agent listener, which keeps presentation concerns out of scheduled
// background runs.
func WithToolMiddleware(middleware compose.ToolMiddleware) Option {
	return func(o *options) {
		if middleware.Invokable != nil || middleware.Streamable != nil ||
			middleware.EnhancedInvokable != nil || middleware.EnhancedStreamable != nil {
			o.toolMiddlewares = append(o.toolMiddlewares, middleware)
		}
	}
}

// New creates the default runtime. The model uses the OpenAI-compatible
// chat-completions protocol; OPENAI_BASE_URL may point at another compatible
// service. SQLite is the deliberately boring default store for local apps.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger, opts ...Option) (*Runtime, error) {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Normalize(); err != nil {
		return nil, fmt.Errorf("bootstrap: normalize config: %w", err)
	}

	settings, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	apiKey := firstNonEmpty(os.Getenv(apiKeyEnv), settings.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("bootstrap: %s is required", apiKeyEnv)
	}
	modelName := firstNonEmpty(os.Getenv(modelEnv), settings.Model)
	if modelName == "" {
		return nil, fmt.Errorf("bootstrap: %s is required", modelEnv)
	}
	workspace, err := normalizeDirectory(cfg.Storage.Location)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: resolve workspace: %w", err)
	}
	cfg.Storage.Location = workspace
	workspaces, err := workspaceFactory(workspace)
	if err != nil {
		return nil, err
	}

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,
		Model:   modelName,
		BaseURL: firstNonEmpty(os.Getenv(baseURLEnv), settings.BaseURL),
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: create chat model: %w", err)
	}

	dbPath := firstNonEmpty(os.Getenv(sqliteEnv), settings.SQLitePath)
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Storage.Location, "golem.db")
	}
	db, err := openSQLite(ctx, dbPath)
	if err != nil {
		return nil, err
	}

	backend, err := sqlxstore.New(db, sqlxstore.WithDialect(sqlxstore.DialectSQLite))
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bootstrap: create sqlite store: %w", err)
	}
	if err := backend.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bootstrap: migrate sqlite store: %w", err)
	}
	if err := syncMCPServers(ctx, backend.MCPServerConfigs(), settings.MCPServers); err != nil {
		_ = db.Close()
		return nil, err
	}

	mcpBuilder := cliMCP.New(cliMCP.Config{
		Servers:      backend.MCPServerConfigs(),
		TrustedHosts: cfg.AI.Tools.MCP.TrustedHosts,
		Logger:       logger,
	})
	providerOptions := []tools.ProviderOption{tools.WithLogger(logger)}
	for _, middleware := range o.toolMiddlewares {
		providerOptions = append(providerOptions, tools.WithToolMiddleware(middleware))
	}
	workspaceMiddleware, err := workspaceToolsMiddleware(workspace, backend, cfg.AI.Tools.PublishFile.BaseURL)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	// Keep this adapter after caller middleware so terminal renderers remain
	// the outer hook even when a workspace tool is handled locally.
	providerOptions = append(providerOptions, tools.WithToolMiddleware(workspaceMiddleware))
	provider := tools.NewProvider(cfg, workspaces, backend, mcpBuilder, providerOptions...)
	runtime := &Runtime{db: db, mcpServers: backend.MCPServerConfigs()}
	ready := false
	defer func() {
		if !ready {
			_ = runtime.Close(context.Background())
		}
	}()
	sandbox, sandboxTools, err := newSandbox(backend, workspaces, logger)
	if err != nil {
		return nil, err
	}
	if sandbox != nil {
		runtime.sandbox = sandbox
		if err := tools.RegisterBuiltins(provider, tools.Builtins{Sandbox: sandbox, SandboxConfig: sandboxTools}); err != nil {
			return nil, fmt.Errorf("bootstrap: register sandbox tools: %w", err)
		}
	}
	runtime.Agent = agent.New(
		chatModel,
		backend, // sqlx.Store implements golem's chatmemory.Repository
		provider,
		cfg,
		agent.WithBackend(backend),
		agent.WithLogger(logger),
		agent.WithModelName(modelName),
	)
	// Subagents are the one built-in family that needs the Agent itself, so
	// core cannot add them from tools.Provider. Register them after Agent
	// construction and before any run can be fired.
	subagent.Register(provider, runtime.Agent, cfg, nil, logger)
	if !o.withoutScheduler && o.scheduler != nil {
		// The runner fires the agent, so it is built after it and stopped
		// before it: Close must allow no new firing while old ones drain.
		runner, err := schedule.New(backend.ScheduledTasks(), runtime.Agent, schedule.Config{
			Prompt:    cfg.AI.ScheduledTaskPrompt,
			Scheduler: o.scheduler,
		}, logger)
		if err != nil {
			runtime.Runner = runner
			return nil, fmt.Errorf("bootstrap: create scheduler: %w", err)
		}
		runtime.Runner = runner
		schedule.RegisterBuiltins(provider, schedule.NewTools(runner, backend.ScheduledTasks()))
		if err := runner.Start(ctx); err != nil {
			return nil, fmt.Errorf("bootstrap: start scheduler: %w", err)
		}
	}
	if !o.withoutScheduler && o.scheduler == nil {
		local := newLocalScheduler()
		runtime.scheduler = local
		runner, err := schedule.New(backend.ScheduledTasks(), runtime.Agent, schedule.Config{
			Prompt:    cfg.AI.ScheduledTaskPrompt,
			Scheduler: local,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: create local scheduler: %w", err)
		}
		runtime.Runner = runner
		schedule.RegisterBuiltins(provider, schedule.NewTools(runner, backend.ScheduledTasks()))
		if err := runner.Start(ctx); err != nil {
			return nil, fmt.Errorf("bootstrap: start local scheduler: %w", err)
		}
	}
	ready = true
	return runtime, nil
}

// ListMCPServers lists the local MCP records currently used by this runtime.
func (r *Runtime) ListMCPServers() ([]store.MCPServerConfig, error) {
	if r == nil || r.mcpServers == nil {
		return nil, errors.New("bootstrap: MCP store is not configured")
	}
	return r.mcpServers.ListByOwner(context.Background(), "local")
}

// SaveMCPServer updates both the local config file and the live SQLX store.
// The next agent turn will discover the new server through the same builder.
func (r *Runtime) SaveMCPServer(server store.MCPServerConfig) error {
	if r == nil || r.mcpServers == nil {
		return errors.New("bootstrap: MCP store is not configured")
	}
	if err := SaveMCPServer(server); err != nil {
		return err
	}
	servers, err := ListMCPServers()
	if err != nil {
		return err
	}
	for _, configured := range servers {
		if configured.OwnerID == "local" && configured.Name == strings.TrimSpace(server.Name) {
			return r.mcpServers.Save(context.Background(), configured)
		}
	}
	return fmt.Errorf("bootstrap: saved MCP server %q was not found", server.Name)
}

// DeleteMCPServer updates both the local config file and the live SQLX store.
func (r *Runtime) DeleteMCPServer(name string) error {
	if r == nil || r.mcpServers == nil {
		return errors.New("bootstrap: MCP store is not configured")
	}
	if err := DeleteMCPServer(name); err != nil {
		return err
	}
	return r.mcpServers.DeleteByOwnerAndName(context.Background(), "local", strings.TrimSpace(name))
}

// Close stops active runs and closes the application-owned database.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.Runner != nil {
		r.Runner.Stop()
	}
	if r.scheduler != nil {
		r.scheduler.Stop()
	}
	if r.Agent != nil {
		if err := r.Agent.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if r.sandbox != nil {
		if err := r.sandbox.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if r.db != nil {
		if err := r.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func openSQLite(ctx context.Context, path string) (*sqlx.DB, error) {
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("bootstrap: create sqlite directory: %w", err)
		}
	}
	db, err := sqlx.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: open sqlite: %w", err)
	}
	// A single connection keeps SQLite transactions predictable and also
	// supports :memory: when a caller uses it for a local smoke test.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bootstrap: ping sqlite: %w", err)
	}
	return db, nil
}

func syncMCPServers(ctx context.Context, servers store.MCPServerConfigStore, desired []store.MCPServerConfig) error {
	if servers == nil {
		return nil
	}
	const localOwner = "local"
	existing, err := servers.ListByOwner(ctx, localOwner)
	if err != nil {
		return fmt.Errorf("bootstrap: list stored MCP servers: %w", err)
	}
	wanted := make(map[string]struct{}, len(desired))
	for _, server := range desired {
		owner := server.OwnerID
		if owner == "" {
			owner = localOwner
		}
		if owner == localOwner {
			wanted[server.Name] = struct{}{}
		}
		if err := servers.Save(ctx, server); err != nil {
			return fmt.Errorf("bootstrap: store MCP server %q: %w", server.Name, err)
		}
	}
	for _, server := range existing {
		if _, ok := wanted[server.Name]; ok {
			continue
		}
		if err := servers.DeleteByOwnerAndName(ctx, localOwner, server.Name); err != nil {
			return fmt.Errorf("bootstrap: remove stale MCP server %q: %w", server.Name, err)
		}
	}
	return nil
}
