package main

import (
	"encoding/json"
	"net/http"
	"time"
)

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// 来源: codebuff 上游 common/src/templates/initial-agents-dir/types/agent-definition.ts
// 及 common/src/constants/claude-oauth.ts 的 OAuth 映射表。
// codebuff backend 对 model 字段透传 OpenRouter，不做白名单校验，
// 因此这里覆盖所有 recommended models + OAuth 映射中的 Anthropic 模型。
var supportedModels = []Model{
	// Anthropic
	{ID: "anthropic/claude-opus-4.6", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-opus-4.5", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-opus-4.1", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-opus-4", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-sonnet-4.6", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-sonnet-4.5", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-sonnet-4", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-4-sonnet-20250522", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-haiku-4.5", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-haiku-4", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-3.5-haiku-20241022", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-3.5-sonnet-20240620", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-3-opus-20240229", OwnedBy: "anthropic"},

	// OpenAI
	{ID: "openai/gpt-5.4", OwnedBy: "openai"},
	{ID: "openai/gpt-5.4-codex", OwnedBy: "openai"},
	{ID: "openai/gpt-5.3", OwnedBy: "openai"},
	{ID: "openai/gpt-5.3-codex", OwnedBy: "openai"},
	{ID: "openai/gpt-5.2", OwnedBy: "openai"},
	{ID: "openai/gpt-5.2-codex", OwnedBy: "openai"},
	{ID: "openai/gpt-5.1", OwnedBy: "openai"},
	{ID: "openai/gpt-5.1-chat", OwnedBy: "openai"},
	{ID: "openai/gpt-5-mini", OwnedBy: "openai"},
	{ID: "openai/gpt-5-nano", OwnedBy: "openai"},
	{ID: "openai/gpt-4o-2024-11-20", OwnedBy: "openai"},
	{ID: "openai/gpt-4o-mini-2024-07-18", OwnedBy: "openai"},
	{ID: "openai/gpt-4.1-nano", OwnedBy: "openai"},
	{ID: "openai/o3-mini-2025-01-31", OwnedBy: "openai"},

	// Google Gemini
	{ID: "google/gemini-3.1-pro-preview", OwnedBy: "google"},
	{ID: "google/gemini-3-pro-preview", OwnedBy: "google"},
	{ID: "google/gemini-3-flash-preview", OwnedBy: "google"},
	{ID: "google/gemini-3.1-flash-lite-preview", OwnedBy: "google"},
	{ID: "google/gemini-2.5-pro", OwnedBy: "google"},
	{ID: "google/gemini-2.5-flash", OwnedBy: "google"},
	{ID: "google/gemini-2.5-flash-lite", OwnedBy: "google"},

	// X-AI
	{ID: "x-ai/grok-4-fast", OwnedBy: "xai"},
	{ID: "x-ai/grok-4.1-fast", OwnedBy: "xai"},
	{ID: "x-ai/grok-code-fast-1", OwnedBy: "xai"},
	{ID: "x-ai/grok-4-07-09", OwnedBy: "xai"},

	// Qwen
	{ID: "qwen/qwen3-max", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-coder-plus", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-coder", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-coder:nitro", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-coder-flash", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-235b-a22b-2507", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-235b-a22b-2507:nitro", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-235b-a22b-thinking-2507", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-235b-a22b-thinking-2507:nitro", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-30b-a3b", OwnedBy: "qwen"},
	{ID: "qwen/qwen3-30b-a3b:nitro", OwnedBy: "qwen"},

	// DeepSeek
	{ID: "deepseek/deepseek-chat-v3-0324", OwnedBy: "deepseek"},
	{ID: "deepseek/deepseek-chat-v3-0324:nitro", OwnedBy: "deepseek"},
	{ID: "deepseek/deepseek-r1-0528", OwnedBy: "deepseek"},
	{ID: "deepseek/deepseek-r1-0528:nitro", OwnedBy: "deepseek"},

	// Moonshot
	{ID: "moonshotai/kimi-k2", OwnedBy: "moonshot"},
	{ID: "moonshotai/kimi-k2:nitro", OwnedBy: "moonshot"},
	{ID: "moonshotai/kimi-k2.5", OwnedBy: "moonshot"},
	{ID: "moonshotai/kimi-k2.5:nitro", OwnedBy: "moonshot"},

	// Z-AI (GLM)
	{ID: "z-ai/glm-5", OwnedBy: "zhipu"},
	{ID: "z-ai/glm-5.1", OwnedBy: "zhipu"},
	{ID: "z-ai/glm-4.7", OwnedBy: "zhipu"},
	{ID: "z-ai/glm-4.7:nitro", OwnedBy: "zhipu"},
	{ID: "z-ai/glm-4.7-flash", OwnedBy: "zhipu"},
	{ID: "z-ai/glm-4.7-flash:nitro", OwnedBy: "zhipu"},
	{ID: "z-ai/glm-4.6", OwnedBy: "zhipu"},
	{ID: "z-ai/glm-4.6:nitro", OwnedBy: "zhipu"},

	// MiniMax
	{ID: "minimax/minimax-m2.5", OwnedBy: "minimax"},
	{ID: "minimax/minimax-m2.7", OwnedBy: "minimax"},
}

func init() {
	now := time.Now().Unix()
	for i := range supportedModels {
		supportedModels[i].Object = "model"
		supportedModels[i].Created = now
	}
}

func handleModels(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ModelList{
		Object: "list",
		Data:   supportedModels,
	})
}
