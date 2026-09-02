package bootstrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	coreconfig "github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/store"
	"github.com/stretchr/testify/require"
)

func TestSettingsAndMCPConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv(configFileEnv, path)
	t.Setenv(apiKeyEnv, "")
	t.Setenv(modelEnv, "")
	t.Setenv(baseURLEnv, "")
	t.Setenv(sqliteEnv, "")
	t.Setenv("GOLEM_STORAGE_LOCATION", "")

	require.NoError(t, SaveSettingsValues(SettingsValues{
		APIKey:          "file-secret",
		Model:           "local-model",
		BaseURL:         "https://model.example/v1",
		SQLitePath:      "data/test.db",
		StorageLocation: "data/workspaces",
	}))
	values, err := LoadSettingsValues()
	require.NoError(t, err)
	require.Equal(t, "file-secret", values.APIKey)
	require.Equal(t, "local-model", values.Model)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	t.Setenv(apiKeyEnv, "environment-secret")
	settings, err := LoadSettings()
	require.NoError(t, err)
	require.Equal(t, "environment-secret", settings.APIKey)
	require.Equal(t, "https://model.example/v1", settings.BaseURL)
	require.Equal(t, "data/workspaces", settings.Config.Storage.Location)

	require.NoError(t, SaveMCPServer(store.MCPServerConfig{
		Name:       "local-server",
		URL:        "http://127.0.0.1:1234/mcp",
		Enabled:    true,
		Headers:    map[string]string{"Authorization": "Bearer test"},
		SharedWith: []string{store.SharedWithAll},
	}))
	servers, err := ListMCPServers()
	require.NoError(t, err)
	require.Len(t, servers, 1)
	require.NotEmpty(t, servers[0].ID)
	require.Equal(t, store.MCPTransportStreamableHTTP, servers[0].Transport)
	require.NoError(t, DeleteMCPServer("local-server"))
	servers, err = ListMCPServers()
	require.NoError(t, err)
	require.Empty(t, servers)
}

func TestSettingsDefaultToCurrentDirectory(t *testing.T) {
	t.Setenv(configFileEnv, filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("GOLEM_STORAGE_LOCATION", "")

	current, err := os.Getwd()
	require.NoError(t, err)
	settings, err := LoadSettings()
	require.NoError(t, err)
	require.Equal(t, current, settings.Config.Storage.Location)
}

func TestWorkspaceTrustIsPersisted(t *testing.T) {
	t.Setenv(configFileEnv, filepath.Join(t.TempDir(), "config.json"))
	workspace := t.TempDir()
	var prompt strings.Builder

	require.NoError(t, EnsureWorkspaceTrusted(workspace, strings.NewReader("yes\n"), &prompt))
	require.Contains(t, prompt.String(), "Trust this workspace?")

	values, err := LoadSettingsValues()
	require.NoError(t, err)
	canonicalWorkspace, err := normalizeDirectory(workspace)
	require.NoError(t, err)
	require.Contains(t, values.TrustedDirectories, canonicalWorkspace)

	prompt.Reset()
	require.NoError(t, EnsureWorkspaceTrusted(workspace, strings.NewReader("no\n"), &prompt))
	require.Empty(t, prompt.String())
}

func TestPrepareWorkspaceAddsCurrentDirectoryToPrompt(t *testing.T) {
	t.Setenv(configFileEnv, filepath.Join(t.TempDir(), "config.json"))
	workspace := t.TempDir()

	cfg, err := PrepareWorkspaceWithPrompt(configForLocation(workspace), func(string) (bool, error) {
		return true, nil
	})
	require.NoError(t, err)
	canonicalWorkspace, err := normalizeDirectory(workspace)
	require.NoError(t, err)
	require.Equal(t, canonicalWorkspace, cfg.Storage.Location)
	require.Contains(t, cfg.AI.SystemPrompt, canonicalWorkspace)
	require.Contains(t, cfg.AI.SystemPrompt, "ListFiles")
	require.Contains(t, cfg.AI.SystemPrompt, "use . for its root")
	require.Contains(t, cfg.AI.SystemPrompt, "shell sandbox starts in this same project root")
	link := filepath.Join(canonicalWorkspace, filepath.FromSlash(workspaceRuntimeDir), localOwnerID)
	linkInfo, err := os.Lstat(link)
	require.NoError(t, err)
	require.True(t, linkInfo.Mode()&os.ModeSymlink != 0)
	resolvedLink, err := filepath.EvalSymlinks(link)
	require.NoError(t, err)
	require.Equal(t, canonicalWorkspace, resolvedLink)
}

func TestWorkspaceTrustRejectsNonInteractiveInput(t *testing.T) {
	t.Setenv(configFileEnv, filepath.Join(t.TempDir(), "config.json"))
	workspace := t.TempDir()
	reader, writer, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	defer reader.Close()

	_, err = PrepareWorkspace(configForLocation(workspace), reader, &strings.Builder{})
	require.ErrorContains(t, err, "not trusted")
}

func configForLocation(location string) coreconfig.Config {
	return coreconfig.Config{Storage: coreconfig.Storage{Location: location}}
}

func TestConfigPathUsesXDGStyleDefault(t *testing.T) {
	t.Setenv(configFileEnv, "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.Equal(t, filepath.Join(home, ".config", "golem", "config.json"), ConfigPath())
}

func TestListSkillsUsesGolemWorkspaceTools(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	storageRoot := t.TempDir()
	t.Setenv(configFileEnv, configPath)
	t.Setenv("GOLEM_STORAGE_LOCATION", storageRoot)

	skillsDir := filepath.Join(storageRoot, "skills")
	err := os.MkdirAll(skillsDir, 0o755)
	require.NoError(t, err)
	skillDir := filepath.Join(skillsDir, "release")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("Prepare a release.\n\nDetailed instructions."), 0o644))

	items, err := ListSkills("local")
	require.NoError(t, err)
	require.Equal(t, []SkillInfo{{Name: "release", Description: "Prepare a release."}}, items)
}
