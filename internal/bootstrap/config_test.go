package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ishi-o/golem/core/storage"
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

	home := storage.NewWorkspaceFactory(storageRoot).ForOwner("local")
	skillsDir, err := home.Folder(storage.FolderSkills)
	require.NoError(t, err)
	skillDir := filepath.Join(skillsDir, "release")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("Prepare a release.\n\nDetailed instructions."), 0o644))

	items, err := ListSkills("local")
	require.NoError(t, err)
	require.Equal(t, []SkillInfo{{Name: "release", Description: "Prepare a release."}}, items)
}
