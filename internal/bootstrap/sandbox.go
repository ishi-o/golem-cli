package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/store"
	"github.com/ishi-o/golem/core/tools"
	"github.com/ishi-o/golem/sandbox/docker"
)

const (
	sandboxEnv        = "GOLEM_SANDBOX" // "docker" or unset (off)
	sandboxImageEnv   = "GOLEM_SANDBOX_IMAGE"
	sandboxNetworkEnv = "GOLEM_SANDBOX_NETWORK" // docker only
)

// newSandbox builds the Docker sandbox selected by GOLEM_SANDBOX, wired to
// the user workspaces and the credential store. A backend that is off (unset)
// returns nils. The local CLI keeps the shell backend disposable and local.
func newSandbox(backend store.Backend, workspaces *storage.WorkspaceFactory, logger *slog.Logger) (tools.Sandbox, tools.SandboxToolsConfig, error) {
	kind := strings.TrimSpace(os.Getenv(sandboxEnv))
	image := strings.TrimSpace(os.Getenv(sandboxImageEnv))
	credentials := tools.CredentialsFromRepository(backend.ShellCredentials())
	switch kind {
	case "":
		return nil, tools.SandboxToolsConfig{}, nil
	case "docker":
		if image == "" {
			return nil, tools.SandboxToolsConfig{}, fmt.Errorf("bootstrap: %s is required when GOLEM_SANDBOX=docker", sandboxImageEnv)
		}
		sandbox, err := docker.New(docker.Config{
			Image:       image,
			Network:     strings.TrimSpace(os.Getenv(sandboxNetworkEnv)),
			Workspaces:  workspaces,
			Credentials: credentials,
			Logger:      logger,
		})
		if err != nil {
			return nil, tools.SandboxToolsConfig{}, fmt.Errorf("bootstrap: docker sandbox: %w", err)
		}
		return sandbox, docker.DefaultToolsConfig(), nil
	default:
		return nil, tools.SandboxToolsConfig{}, fmt.Errorf("bootstrap: %s=%q is not docker", sandboxEnv, kind)
	}
}
