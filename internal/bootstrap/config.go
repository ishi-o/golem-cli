package bootstrap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	coreconfig "github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/store"
)

const configFileEnv = "GOLEM_CONFIG_FILE"

// SettingsValues is the small set of values most people need to configure
// before the first run. It is deliberately separate from coreconfig.Config:
// API credentials belong to the model client, not to golem's public runtime
// configuration.
type SettingsValues struct {
	APIKey          string
	Model           string
	BaseURL         string
	SQLitePath      string
	StorageLocation string
}

// Settings combines the core runtime configuration with values consumed by
// the CLI bootstrap layer.
type Settings struct {
	Config     coreconfig.Config
	APIKey     string
	Model      string
	BaseURL    string
	SQLitePath string
	MCPServers []store.MCPServerConfig
}

// fileConfig is the on-disk, user-owned configuration. The file is kept
// private so changing its representation does not become part of the core
// library API.
type fileConfig struct {
	APIKey          string          `json:"api_key,omitempty"`
	Model           string          `json:"model,omitempty"`
	BaseURL         string          `json:"base_url,omitempty"`
	SQLitePath      string          `json:"sqlite_path,omitempty"`
	StorageLocation string          `json:"storage_location,omitempty"`
	MCPServers      []fileMCPServer `json:"mcp_servers,omitempty"`
}

type fileMCPServer struct {
	ID          string            `json:"id,omitempty"`
	OwnerID     string            `json:"owner_id,omitempty"`
	Name        string            `json:"name"`
	Transport   string            `json:"transport,omitempty"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	Title       string            `json:"title,omitempty"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	WebsiteURL  string            `json:"website_url,omitempty"`
	Enabled     *bool             `json:"enabled,omitempty"`
	SharedWith  []string          `json:"shared_with,omitempty"`
}

// Load reads the environment-backed core configuration. It also honors the
// settings file for values that are useful to a local CLI, so `config
// set` is effective on the next invocation without requiring shell exports.
func Load() (coreconfig.Config, error) {
	settings, err := LoadSettings()
	if err != nil {
		return coreconfig.Config{}, err
	}
	return settings.Config, nil
}

// LoadSettings reads the user config first and lets environment variables
// override it. Environment variables remain the best fit for CI and secret
// managers; the file is the convenient local default.
func LoadSettings() (Settings, error) {
	file, err := readFileConfig()
	if err != nil {
		return Settings{}, err
	}

	storageLocation := firstNonEmpty(os.Getenv("GOLEM_STORAGE_LOCATION"), file.StorageLocation)
	c := coreconfig.Config{
		Locale: os.Getenv("GOLEM_LOCALE"),
		Storage: coreconfig.Storage{
			Location: storageLocation,
			BaseURL:  os.Getenv("GOLEM_STORAGE_BASE_URL"),
			CdnURL:   os.Getenv("GOLEM_STORAGE_CDN_URL"),
		},
		AI: coreconfig.AI{
			Admins:         splitList(os.Getenv("GOLEM_ADMINS")),
			GuideThreshold: envInt("GOLEM_GUIDE_THRESHOLD"),
			Tools: coreconfig.Tools{
				AskUserQuestion: coreconfig.AskUserQuestion{
					Enabled: envBool("GOLEM_ASK_USER_ENABLED", true),
					TTL:     envDuration("GOLEM_ASK_USER_TTL"),
				},
				PublishFile: coreconfig.PublishFile{BaseURL: os.Getenv("GOLEM_PUBLISH_BASE_URL")},
				MCP:         coreconfig.MCP{TrustedHosts: splitList(os.Getenv("GOLEM_MCP_TRUSTED_HOSTS"))},
			},
		},
	}
	if err := c.Normalize(); err != nil {
		return Settings{}, err
	}

	servers := make([]store.MCPServerConfig, 0, len(file.MCPServers))
	for _, server := range file.MCPServers {
		servers = append(servers, normalizeMCPServer(server))
	}
	return Settings{
		Config:     c,
		APIKey:     firstNonEmpty(os.Getenv(apiKeyEnv), file.APIKey),
		Model:      firstNonEmpty(os.Getenv(modelEnv), file.Model),
		BaseURL:    firstNonEmpty(os.Getenv(baseURLEnv), file.BaseURL),
		SQLitePath: firstNonEmpty(os.Getenv(sqliteEnv), file.SQLitePath),
		MCPServers: servers,
	}, nil
}

// ConfigPath returns the location of the local configuration file. Set
// GOLEM_CONFIG_FILE to make it deterministic in tests or to integrate with a
// dotfiles manager.
func ConfigPath() string {
	if path := strings.TrimSpace(os.Getenv(configFileEnv)); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return filepath.Join("data", "config.json")
	}
	return filepath.Join(home, ".config", "golem", "config.json")
}

// LoadSettingsValues reads the values shown by `golem config show`.
func LoadSettingsValues() (SettingsValues, error) {
	file, err := readFileConfig()
	if err != nil {
		return SettingsValues{}, err
	}
	return SettingsValues{
		APIKey:          file.APIKey,
		Model:           file.Model,
		BaseURL:         file.BaseURL,
		SQLitePath:      file.SQLitePath,
		StorageLocation: file.StorageLocation,
	}, nil
}

// SaveSettingsValues updates the settings file while preserving MCP entries.
// Credentials are written to a mode-0600 file and never included in logs.
func SaveSettingsValues(values SettingsValues) error {
	file, err := readFileConfig()
	if err != nil {
		return err
	}
	file.APIKey = strings.TrimSpace(values.APIKey)
	file.Model = strings.TrimSpace(values.Model)
	file.BaseURL = strings.TrimSpace(values.BaseURL)
	file.SQLitePath = strings.TrimSpace(values.SQLitePath)
	file.StorageLocation = strings.TrimSpace(values.StorageLocation)
	return writeFileConfig(file)
}

// ListMCPServers returns the locally configured MCP servers. The local CLI
// uses one owner, `local`; the runtime copies these records into golem's SQLX
// store at startup so the core access rules remain authoritative.
func ListMCPServers() ([]store.MCPServerConfig, error) {
	file, err := readFileConfig()
	if err != nil {
		return nil, err
	}
	servers := make([]store.MCPServerConfig, 0, len(file.MCPServers))
	for _, server := range file.MCPServers {
		servers = append(servers, normalizeMCPServer(server))
	}
	return servers, nil
}

// SaveMCPServer creates or replaces one local MCP server by name.
func SaveMCPServer(server store.MCPServerConfig) error {
	server.Name = strings.TrimSpace(server.Name)
	server.URL = strings.TrimSpace(server.URL)
	if server.Name == "" || server.URL == "" {
		return fmt.Errorf("MCP server name and URL are required")
	}
	endpoint, err := url.Parse(server.URL)
	if err != nil || endpoint.Host == "" || endpoint.Hostname() == "" {
		return fmt.Errorf("MCP URL must be an absolute URL with a host")
	}
	if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
		return fmt.Errorf("MCP URL scheme must be http or https")
	}
	if endpoint.User != nil || endpoint.Fragment != "" {
		return fmt.Errorf("MCP URL must not contain userinfo or a fragment")
	}
	if server.OwnerID == "" {
		server.OwnerID = "local"
	}
	if server.ID == "" {
		server.ID = mcpServerID(server.OwnerID, server.Name)
	}
	if server.Transport == "" {
		server.Transport = store.MCPTransportStreamableHTTP
	}
	if server.Version == "" {
		server.Version = store.MCPServerConfigDefaultVersion
	}
	file, err := readFileConfig()
	if err != nil {
		return err
	}
	next := make([]fileMCPServer, 0, len(file.MCPServers)+1)
	replaced := false
	for _, existing := range file.MCPServers {
		current := normalizeMCPServer(existing)
		if current.OwnerID == server.OwnerID && current.Name == server.Name {
			next = append(next, mcpServerFile(server))
			replaced = true
			continue
		}
		next = append(next, existing)
	}
	if !replaced {
		next = append(next, mcpServerFile(server))
	}
	file.MCPServers = next
	return writeFileConfig(file)
}

// DeleteMCPServer removes a local MCP server by name.
func DeleteMCPServer(name string) error {
	name = strings.TrimSpace(name)
	file, err := readFileConfig()
	if err != nil {
		return err
	}
	next := file.MCPServers[:0]
	found := false
	for _, existing := range file.MCPServers {
		if normalizeMCPServer(existing).OwnerID == "local" && normalizeMCPServer(existing).Name == name {
			found = true
			continue
		}
		next = append(next, existing)
	}
	if !found {
		return fmt.Errorf("MCP server %q is not configured", name)
	}
	file.MCPServers = next
	return writeFileConfig(file)
}

func readFileConfig() (fileConfig, error) {
	data, err := os.ReadFile(ConfigPath())
	if os.IsNotExist(err) {
		return fileConfig{}, nil
	}
	if err != nil {
		return fileConfig{}, fmt.Errorf("read config %s: %w", ConfigPath(), err)
	}
	var file fileConfig
	if err := json.Unmarshal(data, &file); err != nil {
		return fileConfig{}, fmt.Errorf("parse config %s: %w", ConfigPath(), err)
	}
	return file, nil
}

func writeFileConfig(file fileConfig) error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return fmt.Errorf("create config temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("protect config temporary file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close config temporary file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

func mcpServerFile(server store.MCPServerConfig) fileMCPServer {
	enabled := server.Enabled
	return fileMCPServer{
		ID:          server.ID,
		OwnerID:     server.OwnerID,
		Name:        server.Name,
		Transport:   string(server.Transport),
		URL:         server.URL,
		Headers:     server.Headers,
		Title:       server.Title,
		Version:     server.Version,
		Description: server.Description,
		WebsiteURL:  server.WebsiteURL,
		Enabled:     &enabled,
		SharedWith:  server.SharedWith,
	}
}

func normalizeMCPServer(server fileMCPServer) store.MCPServerConfig {
	ownerID := strings.TrimSpace(server.OwnerID)
	if ownerID == "" {
		ownerID = "local"
	}
	name := strings.TrimSpace(server.Name)
	id := strings.TrimSpace(server.ID)
	if id == "" {
		id = mcpServerID(ownerID, name)
	}
	transport := store.MCPTransport(strings.TrimSpace(server.Transport))
	if transport == "" {
		transport = store.MCPTransportStreamableHTTP
	}
	enabled := true
	if server.Enabled != nil {
		enabled = *server.Enabled
	}
	version := strings.TrimSpace(server.Version)
	if version == "" {
		version = store.MCPServerConfigDefaultVersion
	}
	return store.MCPServerConfig{
		ID:          id,
		OwnerID:     ownerID,
		Name:        name,
		Transport:   transport,
		URL:         strings.TrimSpace(server.URL),
		Headers:     server.Headers,
		Title:       server.Title,
		Version:     version,
		Description: server.Description,
		WebsiteURL:  server.WebsiteURL,
		Enabled:     enabled,
		SharedWith:  server.SharedWith,
	}
}

func mcpServerID(ownerID, name string) string {
	// A deterministic id makes syncing the file into SQLX idempotent.
	digest := sha256.Sum256([]byte(ownerID + "\x00" + name))
	return "mcp-" + hex.EncodeToString(digest[:16])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func splitList(v string) []string {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envBool(name string, def bool) bool {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(name string) int {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0
	}
	return n
}

func envIntOr(name string, fallback int) int {
	if strings.TrimSpace(os.Getenv(name)) == "" {
		return fallback
	}
	return envInt(name)
}

func envDuration(name string) time.Duration {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0
	}
	return d
}
