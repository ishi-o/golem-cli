package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudwego/eino/compose"
	coretools "github.com/ishi-o/golem/core/tools"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceFactoryMountAliasResolvesToTrustedWorkspace(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("project"), 0o644))

	factory, err := workspaceFactory(workspace)
	require.NoError(t, err)
	home := factory.ForOwner(localOwnerID)

	resolved, err := filepath.EvalSymlinks(home.Root())
	require.NoError(t, err)
	canonicalWorkspace, err := normalizeDirectory(workspace)
	require.NoError(t, err)
	require.Equal(t, canonicalWorkspace, resolved)
	_, err = os.Stat(filepath.Join(home.Root(), "README.md"))
	require.NoError(t, err)
}

func TestWorkspaceToolsMiddlewareUsesTrustedWorkspaceRoot(t *testing.T) {
	workspace := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "README.md"), []byte("project marker"), 0o644))

	middleware, err := workspaceToolsMiddleware(workspace, nil, "")
	require.NoError(t, err)
	nextCalled := false
	endpoint := middleware.Invokable(func(context.Context, *compose.ToolInput) (*compose.ToolOutput, error) {
		nextCalled = true
		return nil, nil
	})

	list, err := endpoint(context.Background(), &compose.ToolInput{
		Name:      coretools.ToolNameListFiles,
		Arguments: `{"path":"."}`,
	})
	require.NoError(t, err)
	require.Contains(t, list.Result, "README.md")
	require.False(t, nextCalled)

	read, err := endpoint(context.Background(), &compose.ToolInput{
		Name:      coretools.ToolNameReadFile,
		Arguments: `{"path":"README.md"}`,
	})
	require.NoError(t, err)
	require.Contains(t, read.Result, "project marker")

	grep, err := endpoint(context.Background(), &compose.ToolInput{
		Name:      coretools.ToolNameGrepFiles,
		Arguments: `{"pattern":"project marker","path":"."}`,
	})
	require.NoError(t, err)
	require.True(t, strings.Contains(grep.Result, "README.md"), grep.Result)
}
