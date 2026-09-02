// Package mcp adapts Streamable HTTP MCP servers to golem's MCPBuilder
// interface. The MCP protocol, Eino tool conversion, and tool search are
// provided by maintained Eino/Eino-ext integrations; this package only owns
// local CLI policy such as server visibility, SSRF checks, and lifecycle.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	einomcp "github.com/cloudwego/eino-ext/components/tool/mcp"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/store"
	golemtools "github.com/ishi-o/golem/core/tools"
	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

const (
	defaultRequestTimeout = 2 * time.Minute
	clientName            = "golem-cli"
	clientVersion         = "1.0.0"
)

// Config configures the MCP builder for the local runtime.
type Config struct {
	Servers store.MCPServerConfigStore

	// TrustedHosts permits non-TLS HTTP endpoints in addition to loopback
	// endpoints. Entries may be hostnames, host:port pairs, or *.example.com.
	TrustedHosts []string

	Timeout time.Duration
	Logger  *slog.Logger
	// HTTPClient optionally supplies the transport used for MCP requests.
	// A nil value uses a bounded default client. Supplying one is useful to
	// embedders that already centralize proxies, tracing, or test transports.
	HTTPClient *http.Client
}

// Builder connects the MCP servers visible to a user for one run.
type Builder struct {
	servers      store.MCPServerConfigStore
	trustedHosts []string
	timeout      time.Duration
	logger       *slog.Logger
	httpClient   *http.Client
}

// New creates an MCP builder.
func New(config Config) *Builder {
	if config.Timeout <= 0 {
		config.Timeout = defaultRequestTimeout
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &Builder{
		servers:      config.Servers,
		trustedHosts: append([]string(nil), config.TrustedHosts...),
		timeout:      config.Timeout,
		logger:       config.Logger,
		httpClient:   config.HTTPClient,
	}
}

var _ golemtools.MCPBuilder = (*Builder)(nil)

// Build implements tools.MCPBuilder. A server that is unavailable is
// isolated from the rest of the run: a local agent should still answer using
// its built-in tools when one optional MCP endpoint is down.
func (b *Builder) Build(ctx context.Context, userID, chatID string) (golemtools.MCPTools, error) {
	if b == nil || b.servers == nil {
		return golemtools.MCPTools{}, nil
	}
	configs, err := b.servers.ListAccessibleTo(ctx, userID, store.MCPServerConfigAccessIdentifiers(userID, chatID))
	if err != nil {
		return golemtools.MCPTools{}, fmt.Errorf("list MCP servers: %w", err)
	}

	set := &clientSet{}
	var remoteTools []tool.InvokableTool
	for _, config := range configs {
		if !config.Enabled {
			continue
		}
		if config.Transport != "" && config.Transport != store.MCPTransportStreamableHTTP {
			b.logger.Warn("skipping MCP server with unsupported transport", "server", config.Name, "transport", config.Transport)
			continue
		}

		tools, closer, err := connect(ctx, config, b.trustedHosts, b.timeout, b.httpClient)
		if err != nil {
			b.logger.Warn("MCP server initialization failed", "server", config.Name, "err", err)
			continue
		}
		set.clients = append(set.clients, closer)
		remoteTools = appendUniqueTools(remoteTools, tools...)
	}
	if len(remoteTools) == 0 {
		return golemtools.MCPTools{Closer: set}, nil
	}

	// Eino's official tool search is packaged as ADK middleware. Golem v1's
	// agent loop does not expose an ADK middleware hook, so use the middleware's
	// public BeforeAgent hook to obtain its official meta-tool. The static
	// golem loop still receives all MCP tools; only the search implementation is
	// reused here, and no local search algorithm is maintained.
	search, err := newEinoToolSearch(ctx, remoteTools)
	if err != nil {
		_ = set.Close()
		return golemtools.MCPTools{}, err
	}
	visible := append([]tool.InvokableTool(nil), remoteTools...)
	visible = append(visible, search)
	return golemtools.MCPTools{Tools: visible, Closer: set}, nil
}

// clientSet owns all per-run MCP sessions. Golem closes it when the run ends.
type clientSet struct {
	clients []io.Closer
	once    sync.Once
}

func (s *clientSet) Close() error {
	if s == nil {
		return nil
	}
	var result error
	s.once.Do(func() {
		for _, closer := range s.clients {
			if err := closer.Close(); err != nil {
				result = errors.Join(result, err)
			}
		}
	})
	return result
}

// connect uses Eino-ext to turn an initialized mcp-go client into Eino tools.
// The small protocol-version retry is kept here because some older servers
// reject a newer initialize request instead of negotiating down.
func connect(
	ctx context.Context,
	config store.MCPServerConfig,
	trustedHosts []string,
	timeout time.Duration,
	baseClient *http.Client,
) ([]tool.InvokableTool, io.Closer, error) {
	endpoint, err := validateEndpoint(config.URL, trustedHosts)
	if err != nil {
		return nil, nil, err
	}

	versions := []string{mcp.LATEST_PROTOCOL_VERSION, "2025-03-26", "2024-11-05"}
	var lastErr error
	for _, version := range versions {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		mcpClient, err := newClient(endpoint, config, trustedHosts, timeout, baseClient)
		if err != nil {
			return nil, nil, err
		}
		if err := mcpClient.Start(ctx); err != nil {
			_ = mcpClient.Close()
			lastErr = err
			continue
		}
		request := mcp.InitializeRequest{}
		request.Params.ProtocolVersion = version
		request.Params.ClientInfo = mcp.Implementation{
			Name:    clientTitle(config),
			Version: clientVersionOrDefault(config.Version),
		}
		if _, err := mcpClient.Initialize(ctx, request); err != nil {
			_ = mcpClient.Close()
			lastErr = err
			continue
		}

		baseTools, err := einomcp.GetTools(ctx, &einomcp.Config{
			Cli:           mcpClient,
			CustomHeaders: config.Headers,
		})
		if err != nil {
			_ = mcpClient.Close()
			return nil, nil, fmt.Errorf("discover MCP tools: %w", err)
		}
		scopedTools, err := scopeTools(config.Name, baseTools)
		if err != nil {
			_ = mcpClient.Close()
			return nil, nil, err
		}
		return scopedTools, mcpClient, nil
	}
	return nil, nil, fmt.Errorf("initialize MCP server: %w", lastErr)
}

func newClient(
	endpoint *url.URL,
	config store.MCPServerConfig,
	trustedHosts []string,
	timeout time.Duration,
	baseClient *http.Client,
) (*client.Client, error) {
	httpClient := cloneHTTPClient(baseClient, timeout)
	previousRedirect := httpClient.CheckRedirect
	httpClient.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if _, err := validateEndpoint(next.URL.String(), trustedHosts); err != nil {
			return fmt.Errorf("MCP redirect rejected: %w", err)
		}
		if previousRedirect != nil {
			return previousRedirect(next, via)
		}
		return nil
	}

	options := []transport.StreamableHTTPCOption{
		transport.WithHTTPBasicClient(httpClient),
	}
	if len(config.Headers) != 0 {
		options = append(options, transport.WithHTTPHeaders(config.Headers))
	}
	return client.NewStreamableHttpClient(endpoint.String(), options...)
}

func cloneHTTPClient(base *http.Client, timeout time.Duration) *http.Client {
	if base == nil {
		return &http.Client{Timeout: timeout}
	}
	clone := *base
	if clone.Timeout <= 0 {
		clone.Timeout = timeout
	}
	return &clone
}

// scopedTool preserves the remote tool implementation while namespacing its
// Eino metadata. MCP servers commonly reuse names such as "search".
type scopedTool struct {
	tool tool.InvokableTool
	info *schema.ToolInfo
}

func (t *scopedTool) Info(context.Context) (*schema.ToolInfo, error) { return t.info, nil }

func (t *scopedTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	return t.tool.InvokableRun(ctx, argumentsInJSON, opts...)
}

func scopeTools(serverName string, candidates []tool.BaseTool) ([]tool.InvokableTool, error) {
	result := make([]tool.InvokableTool, 0, len(candidates))
	for _, candidate := range candidates {
		invokable, ok := candidate.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("MCP tool %T is not invokable", candidate)
		}
		info, err := invokable.Info(context.Background())
		if err != nil {
			return nil, fmt.Errorf("read MCP tool info: %w", err)
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			continue
		}
		copy := *info
		remoteName := info.Name
		copy.Name = exportedToolName(serverName, remoteName)
		description := strings.TrimSpace(info.Desc)
		if description == "" {
			description = "MCP tool " + remoteName
		}
		copy.Desc = fmt.Sprintf("MCP server %q. %s", serverName, description)
		if info.Extra != nil {
			copy.Extra = make(map[string]any, len(info.Extra)+2)
			for key, value := range info.Extra {
				copy.Extra[key] = value
			}
		} else {
			copy.Extra = make(map[string]any, 2)
		}
		copy.Extra["mcp_server"] = serverName
		copy.Extra["mcp_tool"] = remoteName
		result = append(result, &scopedTool{tool: invokable, info: &copy})
	}
	return result, nil
}

func appendUniqueTools(existing []tool.InvokableTool, candidates ...tool.InvokableTool) []tool.InvokableTool {
	seen := make(map[string]struct{}, len(existing)+len(candidates))
	for _, candidate := range existing {
		if info, err := candidate.Info(context.Background()); err == nil && info != nil {
			seen[info.Name] = struct{}{}
		}
	}
	for _, candidate := range candidates {
		info, err := candidate.Info(context.Background())
		if err != nil || info == nil {
			continue
		}
		if _, ok := seen[info.Name]; ok {
			continue
		}
		seen[info.Name] = struct{}{}
		existing = append(existing, candidate)
	}
	return existing
}

func newEinoToolSearch(ctx context.Context, tools []tool.InvokableTool) (tool.InvokableTool, error) {
	dynamicTools := make([]tool.BaseTool, len(tools))
	for i, candidate := range tools {
		dynamicTools[i] = candidate
	}
	middleware, err := toolsearch.New(ctx, &toolsearch.Config{DynamicTools: dynamicTools})
	if err != nil {
		return nil, fmt.Errorf("create Eino tool search middleware: %w", err)
	}
	_, runContext, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	if err != nil {
		return nil, fmt.Errorf("initialize Eino tool search middleware: %w", err)
	}
	for _, candidate := range runContext.Tools {
		info, err := candidate.Info(ctx)
		if err != nil || info == nil || info.Name != "tool_search" {
			continue
		}
		search, ok := candidate.(tool.InvokableTool)
		if !ok {
			return nil, fmt.Errorf("Eino tool search %T is not invokable", candidate)
		}
		return search, nil
	}
	return nil, errors.New("Eino tool search middleware did not provide tool_search")
}

func validateEndpoint(raw string, trustedHosts []string) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("parse MCP URL: %w", err)
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return nil, fmt.Errorf("MCP URL scheme must be http or https")
	}
	if endpoint.Host == "" || endpoint.Hostname() == "" {
		return nil, fmt.Errorf("MCP URL has no host")
	}
	if endpoint.User != nil || endpoint.Fragment != "" {
		return nil, fmt.Errorf("MCP URL must not contain userinfo or a fragment")
	}
	if endpoint.Scheme == "http" && !trustedHost(endpoint, trustedHosts) {
		return nil, fmt.Errorf("plain HTTP MCP endpoint %q is not trusted", endpoint.Host)
	}
	return endpoint, nil
}

func trustedHost(endpoint *url.URL, trustedHosts []string) bool {
	host := strings.ToLower(endpoint.Hostname())
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	if host == "localhost" {
		return true
	}
	for _, raw := range trustedHosts {
		entry := strings.ToLower(strings.TrimSpace(raw))
		entry = strings.TrimPrefix(entry, "http://")
		entry = strings.TrimPrefix(entry, "https://")
		entry = strings.TrimSuffix(entry, "/")
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "*.") {
			if strings.HasSuffix(host, strings.TrimPrefix(entry, "*")) {
				return true
			}
			continue
		}
		if entry == host || entry == strings.ToLower(endpoint.Host) {
			return true
		}
	}
	return false
}

func exportedToolName(server, remote string) string {
	server = safeName(server)
	remote = safeName(remote)
	if server == "" {
		server = "server"
	}
	if remote == "" {
		remote = "tool"
	}
	name := "mcp_" + server + "_" + remote
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func safeName(value string) string {
	var b strings.Builder
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_-")
}

func clientTitle(config store.MCPServerConfig) string {
	if strings.TrimSpace(config.Title) != "" {
		return config.Title
	}
	if strings.TrimSpace(config.Name) != "" {
		return config.Name
	}
	return clientName
}

func clientVersionOrDefault(value string) string {
	if strings.TrimSpace(value) == "" {
		return clientVersion
	}
	return value
}
