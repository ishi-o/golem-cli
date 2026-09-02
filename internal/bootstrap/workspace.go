package bootstrap

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	coreconfig "github.com/ishi-o/golem/core/config"
)

// PrepareWorkspace resolves the runtime workspace and asks for permission the
// first time it is used. The returned config uses an absolute path so the
// process cannot change its workspace merely by changing its working
// directory after the trust decision.
func PrepareWorkspace(cfg coreconfig.Config, input io.Reader, output io.Writer) (coreconfig.Config, error) {
	return PrepareWorkspaceWithPrompt(cfg, func(workspace string) (bool, error) {
		return promptWorkspaceTrust(input, output, workspace)
	})
}

// PrepareWorkspaceWithPrompt is the same preparation flow with a caller-owned
// trust UI. The CLI uses this to present approval in its Bubble Tea screen;
// embedders can provide another confirmation surface.
func PrepareWorkspaceWithPrompt(cfg coreconfig.Config, prompt func(string) (bool, error)) (coreconfig.Config, error) {
	cfg, workspace, err := resolveWorkspace(cfg)
	if err != nil {
		return cfg, err
	}
	if err := ensureWorkspaceTrusted(workspace, prompt); err != nil {
		return cfg, err
	}
	cfg.Storage.Location = workspace
	cfg.AI.SystemPrompt = workspaceSystemPrompt(cfg.AI.SystemPrompt, workspace)
	return cfg, nil
}

func resolveWorkspace(cfg coreconfig.Config) (coreconfig.Config, string, error) {
	if strings.TrimSpace(cfg.Storage.Location) == "" {
		current, err := os.Getwd()
		if err != nil {
			return cfg, "", fmt.Errorf("bootstrap: get current directory: %w", err)
		}
		cfg.Storage.Location = current
	}
	if err := cfg.Normalize(); err != nil {
		return cfg, "", fmt.Errorf("bootstrap: normalize workspace config: %w", err)
	}

	workspace, err := normalizeDirectory(cfg.Storage.Location)
	if err != nil {
		return cfg, "", fmt.Errorf("bootstrap: resolve workspace: %w", err)
	}
	return cfg, workspace, nil
}

// EnsureWorkspaceTrusted records an explicit approval for one directory in
// the local config file. Trust is exact-directory and symlink-aware; a parent
// directory does not implicitly trust a new child.
func EnsureWorkspaceTrusted(workspace string, input io.Reader, output io.Writer) error {
	workspace, err := normalizeDirectory(workspace)
	if err != nil {
		return fmt.Errorf("bootstrap: resolve workspace: %w", err)
	}
	return ensureWorkspaceTrusted(workspace, func(workspace string) (bool, error) {
		return promptWorkspaceTrust(input, output, workspace)
	})
}

func ensureWorkspaceTrusted(workspace string, prompt func(string) (bool, error)) error {
	file, err := readFileConfig()
	if err != nil {
		return err
	}
	if trustedDirectory(file.TrustedDirectories, workspace) {
		return nil
	}

	if prompt == nil {
		return fmt.Errorf("bootstrap: workspace %q is not trusted; run golem once from an interactive terminal to approve it", workspace)
	}
	approved, err := prompt(workspace)
	if err != nil {
		return err
	}
	if !approved {
		return fmt.Errorf("bootstrap: workspace %q was not trusted", workspace)
	}

	file.TrustedDirectories = appendTrustedDirectory(file.TrustedDirectories, workspace)
	if err := writeFileConfig(file); err != nil {
		return fmt.Errorf("bootstrap: save workspace trust: %w", err)
	}
	return nil
}

func promptWorkspaceTrust(input io.Reader, output io.Writer, workspace string) (bool, error) {
	if !canPrompt(input, output) {
		return false, fmt.Errorf("bootstrap: workspace %q is not trusted; run golem once from an interactive terminal to approve it", workspace)
	}
	if _, err := fmt.Fprintf(output, "Allow golem to read and modify files in\n  %s\nTrust this workspace? [y/N] ", workspace); err != nil {
		return false, fmt.Errorf("bootstrap: prompt for workspace trust: %w", err)
	}
	answer, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && err != io.EOF {
		return false, fmt.Errorf("bootstrap: read workspace trust: %w", err)
	}
	if !isAffirmative(answer) {
		return false, nil
	}
	_, _ = fmt.Fprintln(output, "Workspace trusted.")
	return true, nil
}

func workspaceSystemPrompt(systemPrompt, workspace string) string {
	const marker = "# CLI workspace"
	base := strings.TrimSpace(systemPrompt)
	if index := strings.Index(base, marker); index >= 0 {
		base = strings.TrimSpace(base[:index])
	}
	// Golem's prompt renderer treats braces as placeholders. Displaying a
	// directory with literal braces must not accidentally create one.
	displayPath := strings.NewReplacer("{", "[", "}", "]").Replace(filepath.ToSlash(workspace))
	instructions := fmt.Sprintf(`%s
- The current CLI workspace is %q.
- Treat it as the project root. For ListFiles, ReadFile, WriteFile, and GrepFiles, pass paths relative to this directory; use . for its root.
- Keep file, skill, and sandbox operations inside this workspace unless the user explicitly asks otherwise.`, marker, displayPath)
	if base == "" {
		return instructions
	}
	return base + "\n\n" + instructions
}

func trustedDirectory(entries []string, workspace string) bool {
	for _, entry := range entries {
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		candidate, err := normalizeDirectory(entry)
		if err == nil && candidate == workspace {
			return true
		}
		// Keep trust usable for a directory that was configured before it
		// existed; normalizeDirectory cannot resolve a deleted path.
		if abs, err := filepath.Abs(entry); err == nil && filepath.Clean(abs) == workspace {
			return true
		}
	}
	return false
}

func appendTrustedDirectory(entries []string, workspace string) []string {
	result := make([]string, 0, len(entries)+1)
	seen := make(map[string]struct{}, len(entries)+1)
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		candidate, err := normalizeDirectory(entry)
		if err != nil {
			if abs, absErr := filepath.Abs(entry); absErr == nil {
				candidate = filepath.Clean(abs)
			} else {
				continue
			}
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		result = append(result, candidate)
	}
	if _, ok := seen[workspace]; !ok {
		result = append(result, workspace)
	}
	return result
}

func normalizeDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("workspace directory is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)

	if info, err := os.Stat(abs); err == nil {
		if !info.IsDir() {
			return "", fmt.Errorf("%q is not a directory", path)
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", err
		}
		return filepath.Clean(resolved), nil
	}

	// The runtime can create a configured storage directory later. Resolve
	// the existing parent so a symlink in the path cannot silently change the
	// trusted location.
	missing := []string{}
	current := abs
	for {
		info, statErr := os.Stat(current)
		if statErr == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("%q is not a directory", current)
			}
			resolved, evalErr := filepath.EvalSymlinks(current)
			if evalErr != nil {
				return "", evalErr
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", statErr
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

func canPrompt(input io.Reader, output io.Writer) bool {
	if inputFile, ok := input.(*os.File); ok {
		info, err := inputFile.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	if outputFile, ok := output.(*os.File); ok {
		info, err := outputFile.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
	}
	return input != nil && output != nil
}

func isAffirmative(answer string) bool {
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true
	default:
		return false
	}
}
