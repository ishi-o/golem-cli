package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/ishi-o/golem/core/store"
	"github.com/stretchr/testify/require"
)

type testMCPStore struct {
	configs []store.MCPServerConfig
}

func (s *testMCPStore) Save(_ context.Context, config store.MCPServerConfig) error {
	s.configs = append(s.configs, config)
	return nil
}
func (s *testMCPStore) ListByOwner(_ context.Context, ownerID string) ([]store.MCPServerConfig, error) {
	var result []store.MCPServerConfig
	for _, config := range s.configs {
		if config.OwnerID == ownerID {
			result = append(result, config)
		}
	}
	return result, nil
}
func (s *testMCPStore) GetByOwnerAndName(_ context.Context, ownerID, name string) (*store.MCPServerConfig, error) {
	for _, config := range s.configs {
		if config.OwnerID == ownerID && config.Name == name {
			copy := config
			return &copy, nil
		}
	}
	return nil, nil
}
func (s *testMCPStore) ExistsByOwnerAndName(_ context.Context, ownerID, name string) (bool, error) {
	config, _ := s.GetByOwnerAndName(context.Background(), ownerID, name)
	return config != nil, nil
}
func (s *testMCPStore) DeleteByOwnerAndName(_ context.Context, ownerID, name string) error {
	for i, config := range s.configs {
		if config.OwnerID == ownerID && config.Name == name {
			s.configs = append(s.configs[:i], s.configs[i+1:]...)
			return nil
		}
	}
	return nil
}
func (s *testMCPStore) ListSharedWith(_ context.Context, identifiers []string) ([]store.MCPServerConfig, error) {
	var result []store.MCPServerConfig
	for _, config := range s.configs {
		for _, sharedWith := range config.SharedWith {
			for _, identifier := range identifiers {
				if sharedWith == identifier {
					result = append(result, config)
					break
				}
			}
		}
	}
	return result, nil
}
func (s *testMCPStore) ListAccessibleTo(_ context.Context, ownerID string, identifiers []string) ([]store.MCPServerConfig, error) {
	shared, err := s.ListSharedWith(context.Background(), identifiers)
	if err != nil {
		return nil, err
	}
	result := append([]store.MCPServerConfig(nil), shared...)
	for _, config := range s.configs {
		if config.OwnerID == ownerID {
			result = append(result, config)
		}
	}
	return result, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func mcpResponse(status int, id int64, result any, session bool) *http.Response {
	var body string
	if status != http.StatusAccepted {
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
		body = string(data)
	}
	headers := make(http.Header)
	headers.Set("Content-Type", "application/json")
	if session {
		headers.Set("Mcp-Session-Id", "test-session")
	}
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestBuilderDiscoversAndCallsStreamableHTTPTools(t *testing.T) {
	var methods []string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodDelete {
			return mcpResponse(http.StatusNoContent, 0, nil, false), nil
		}
		var input struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			return nil, err
		}
		methods = append(methods, input.Method)
		if input.Method == "notifications/initialized" {
			return mcpResponse(http.StatusAccepted, 0, nil, true), nil
		}
		var result any
		switch input.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18"}
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{
					"name":        "echo",
					"description": "Echo a message",
					"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"message": map[string]any{"type": "string"}}},
				},
				map[string]any{"name": "no_args", "description": "A tool without an input schema"},
			}}
		case "tools/call":
			message, _ := input.Params["arguments"].(map[string]any)["message"].(string)
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "echo: " + message}}}
		default:
			result = map[string]any{}
		}
		return mcpResponse(http.StatusOK, input.ID, result, true), nil
	})

	registry := &testMCPStore{configs: []store.MCPServerConfig{{
		OwnerID: "local", Name: "demo", Transport: store.MCPTransportStreamableHTTP,
		URL: "http://127.0.0.1/mcp", Enabled: true,
	}}}
	builder := New(Config{Servers: registry, HTTPClient: &http.Client{Transport: transport}})
	tools, err := builder.Build(context.Background(), "local", "chat")
	require.NoError(t, err)
	require.Len(t, tools.Tools, 3)

	var echo tool.InvokableTool
	for _, candidate := range tools.Tools {
		info, infoErr := candidate.Info(context.Background())
		require.NoError(t, infoErr)
		if info.Name == "mcp_demo_echo" {
			echo = candidate
		}
	}
	require.NotNil(t, echo)
	info, err := echo.Info(context.Background())
	require.NoError(t, err)
	require.NotNil(t, info.ParamsOneOf)
	output, err := echo.InvokableRun(context.Background(), `{"message":"hello"}`)
	require.NoError(t, err)
	require.Contains(t, output, "echo: hello")
	require.NoError(t, tools.Closer.Close())

	require.Contains(t, methods, "initialize")
	require.Contains(t, methods, "notifications/initialized")
	require.Contains(t, methods, "tools/list")
	require.Contains(t, methods, "tools/call")
}

func TestBuilderUsesEinoToolSearch(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodDelete {
			return mcpResponse(http.StatusNoContent, 0, nil, false), nil
		}
		var input struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
			return nil, err
		}
		var result any
		switch input.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18"}
		case "notifications/initialized":
			return mcpResponse(http.StatusAccepted, 0, nil, true), nil
		case "tools/list":
			result = map[string]any{"tools": []any{
				map[string]any{"name": "calendar", "description": "Read calendar events"},
				map[string]any{"name": "mail", "description": "Send email"},
			}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "called"}}}
		}
		return mcpResponse(http.StatusOK, input.ID, result, true), nil
	})

	registry := &testMCPStore{configs: []store.MCPServerConfig{{
		OwnerID: "local", Name: "demo", Transport: store.MCPTransportStreamableHTTP,
		URL: "http://127.0.0.1/mcp", Enabled: true,
	}}}
	builder := New(Config{Servers: registry, HTTPClient: &http.Client{Transport: transport}})
	tools, err := builder.Build(context.Background(), "local", "chat")
	require.NoError(t, err)
	require.Len(t, tools.Tools, 3)
	var search tool.InvokableTool
	for _, candidate := range tools.Tools {
		info, infoErr := candidate.Info(context.Background())
		require.NoError(t, infoErr)
		if info.Name == "tool_search" {
			search = candidate
		}
	}
	require.NotNil(t, search)
	searchOutput, err := search.InvokableRun(context.Background(), `{"query":"calendar"}`)
	require.NoError(t, err)
	require.Contains(t, searchOutput, "mcp_demo_calendar")
	require.NoError(t, tools.Closer.Close())
}

func TestValidateEndpoint(t *testing.T) {
	for _, test := range []struct {
		name    string
		url     string
		trusted []string
		wantErr bool
	}{
		{name: "loopback HTTP", url: "http://127.0.0.1:1234/mcp"},
		{name: "TLS", url: "https://mcp.example.com/mcp"},
		{name: "untrusted HTTP", url: "http://mcp.example.com/mcp", wantErr: true},
		{name: "trusted HTTP", url: "http://mcp.example.com/mcp", trusted: []string{"mcp.example.com"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateEndpoint(test.url, test.trusted)
			if test.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

var _ store.MCPServerConfigStore = (*testMCPStore)(nil)
