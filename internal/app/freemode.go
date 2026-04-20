package app

// freeModeEntry maps a model to its required agentId for codebuff free mode.
type freeModeEntry struct {
	AgentID string
	Model   string
}

// freeModeModels is the allowlist of (agentId, model) pairs that codebuff
// permits in cost_mode=free. Derived from codebuff source:
// common/src/constants/free-agents.ts → FREE_MODE_AGENT_MODELS
//
// Last synced: 2026-04-19 (codebuff v1.0.643)
var freeModeModels = map[string]freeModeEntry{
	// Root orchestrator — base2-free
	"minimax/minimax-m2.7": {AgentID: "base2-free", Model: "minimax/minimax-m2.7"},
	"z-ai/glm-5.1":        {AgentID: "base2-free", Model: "z-ai/glm-5.1"},

	// Editor lite
	"editor-lite/minimax-m2.7": {AgentID: "editor-lite", Model: "minimax/minimax-m2.7"},
	"editor-lite/glm-5.1":     {AgentID: "editor-lite", Model: "z-ai/glm-5.1"},

	// Code reviewer lite
	"code-reviewer-lite/minimax-m2.7": {AgentID: "code-reviewer-lite", Model: "minimax/minimax-m2.7"},
	"code-reviewer-lite/glm-5.1":     {AgentID: "code-reviewer-lite", Model: "z-ai/glm-5.1"},

	// File exploration agents
	"google/gemini-2.5-flash-lite":         {AgentID: "file-picker", Model: "google/gemini-2.5-flash-lite"},
	"google/gemini-3.1-flash-lite-preview": {AgentID: "file-picker-max", Model: "google/gemini-3.1-flash-lite-preview"},
}

// resolveFreeModeAgent looks up the model in the free-mode allowlist.
// Returns (agentId, canonicalModel, true) if allowed, or ("", "", false)
// if the model is not available in free mode.
func resolveFreeModeAgent(model string) (agentID, canonicalModel string, ok bool) {
	if entry, found := freeModeModels[model]; found {
		return entry.AgentID, entry.Model, true
	}
	return "", "", false
}
