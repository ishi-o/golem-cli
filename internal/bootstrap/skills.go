package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ishi-o/golem/core/tools"
)

// SkillInfo is the small display shape used by the CLI skills command.
type SkillInfo struct {
	Name        string
	Description string
}

// ListSkills reads the skills visible to one local CLI owner using golem's
// own SkillTools implementation. It deliberately shares the same workspace
// layout as the agent instead of maintaining a second directory scanner.
func ListSkills(ownerID string) ([]SkillInfo, error) {
	settings, err := LoadSettings()
	if err != nil {
		return nil, err
	}
	workspace, err := normalizeDirectory(settings.Config.Storage.Location)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: resolve skills workspace: %w", err)
	}
	// The CLI has one trusted project workspace. ownerID stays in the public
	// helper signature for compatibility with embedders, while the Golem
	// SkillTools itself receives the actual project-root home.
	_ = ownerID
	home := cliWorkspaceHome{root: workspace}
	skillTools, err := tools.NewSkillTools(home)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: create skill tools: %w", err)
	}
	result, err := skillTools.ListSkills().InvokableRun(context.Background(), "{}")
	if err != nil {
		return nil, fmt.Errorf("bootstrap: list skills: %w", err)
	}
	var payload struct {
		Skills []struct {
			Name string `json:"name"`
			Desc string `json:"description"`
		} `json:"skills"`
	}
	if err := json.Unmarshal([]byte(result), &payload); err != nil {
		return nil, fmt.Errorf("bootstrap: decode skills: %w", err)
	}
	items := make([]SkillInfo, 0, len(payload.Skills))
	for _, skill := range payload.Skills {
		items = append(items, SkillInfo{Name: skill.Name, Description: skill.Desc})
	}
	return items, nil
}
