package bootstrap

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/ishi-o/golem/core/storage"
	corestore "github.com/ishi-o/golem/core/store"
	coretools "github.com/ishi-o/golem/core/tools"
)

// cliWorkspaceHome adapts the trusted directory to golem's Home interface.
// Golem's default UserHome puts the file tools below a fixed "workspace"
// folder; a local CLI needs the trusted directory itself to be that folder.
// The tool implementations remain golem's own FileSystemTools and
// PublishFileTools — this type only supplies their root.
type cliWorkspaceHome struct {
	root string
}

func (h cliWorkspaceHome) Root() string { return h.root }

func (h cliWorkspaceHome) Folder(folder storage.Folder) (string, error) {
	if strings.TrimSpace(string(folder)) == "" || strings.ContainsAny(string(folder), `/\\`) {
		return "", fmt.Errorf("invalid workspace folder %q", folder)
	}
	directory := filepath.Join(h.root, string(folder))
	if folder == storage.FolderWorkspace {
		return h.root, nil
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", err
	}
	return directory, nil
}

func (h cliWorkspaceHome) Roots() []string { return []string{h.root} }

func (h cliWorkspaceHome) Dirs(folder storage.Folder) ([]string, error) {
	directory, err := h.Folder(folder)
	if err != nil {
		return nil, err
	}
	return []string{directory}, nil
}

func (h cliWorkspaceHome) Contains(candidate string) bool {
	abs, err := filepath.Abs(candidate)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	return abs == h.root || strings.HasPrefix(abs, h.root+string(filepath.Separator))
}

// workspaceToolsMiddleware routes workspace-sensitive built-ins through
// Golem tool implementations backed by the actual trusted directory. The
// normal provider still registers every built-in tool; this is only a root
// adapter for the four file tools and publish tools whose implementations
// otherwise assume UserHome/<workspace>.
func workspaceToolsMiddleware(workspace string, repos corestore.Backend, publishBaseURL string) (compose.ToolMiddleware, error) {
	home := cliWorkspaceHome{root: workspace}
	filesystem, err := coretools.NewFileSystemTools(home)
	if err != nil {
		return compose.ToolMiddleware{}, fmt.Errorf("bootstrap: create workspace file tools: %w", err)
	}

	toolsByName, err := indexWorkspaceTools(filesystem.List())
	if err != nil {
		return compose.ToolMiddleware{}, err
	}
	if repos != nil {
		publish := coretools.NewPublishFileTools(
			repos.PublishedResources(),
			storage.NewFileSystem(workspace),
			home,
			publishBaseURL,
		)
		publishTools, err := indexWorkspaceTools(publish.List())
		if err != nil {
			return compose.ToolMiddleware{}, err
		}
		for name, workspaceTool := range publishTools {
			toolsByName[name] = workspaceTool
		}
	}

	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				if input != nil {
					if workspaceTool, ok := toolsByName[input.Name]; ok {
						result, err := workspaceTool.InvokableRun(ctx, input.Arguments, input.CallOptions...)
						if err != nil {
							return nil, err
						}
						return &compose.ToolOutput{Result: result}, nil
					}
				}
				return next(ctx, input)
			}
		},
	}, nil
}

func indexWorkspaceTools(candidates []tool.InvokableTool) (map[string]tool.InvokableTool, error) {
	indexed := make(map[string]tool.InvokableTool, len(candidates))
	for _, candidate := range candidates {
		if candidate == nil {
			continue
		}
		info, err := candidate.Info(context.Background())
		if err != nil {
			return nil, fmt.Errorf("bootstrap: inspect workspace tool: %w", err)
		}
		if info == nil || strings.TrimSpace(info.Name) == "" {
			return nil, fmt.Errorf("bootstrap: workspace tool has no name")
		}
		indexed[info.Name] = candidate
	}
	return indexed, nil
}
