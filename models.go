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

var supportedModels = []Model{
	{ID: "anthropic/claude-sonnet-4", Object: "model", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-4-sonnet-20250522", Object: "model", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-sonnet-4.5", Object: "model", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-opus-4.1", Object: "model", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-3.5-haiku-20241022", Object: "model", OwnedBy: "anthropic"},
	{ID: "anthropic/claude-3.5-sonnet-20240620", Object: "model", OwnedBy: "anthropic"},
	{ID: "google/gemini-2.5-flash", Object: "model", OwnedBy: "google"},
	{ID: "google/gemini-2.5-pro", Object: "model", OwnedBy: "google"},
	{ID: "google/gemini-2.5-flash-lite", Object: "model", OwnedBy: "google"},
	{ID: "google/gemini-3.1-flash-lite-preview", Object: "model", OwnedBy: "google"},
	{ID: "google/gemini-3.1-pro-preview", Object: "model", OwnedBy: "google"},
	{ID: "openai/gpt-5.1", Object: "model", OwnedBy: "openai"},
	{ID: "openai/gpt-5.1-chat", Object: "model", OwnedBy: "openai"},
	{ID: "openai/gpt-4o-2024-11-20", Object: "model", OwnedBy: "openai"},
	{ID: "openai/gpt-4o-mini-2024-07-18", Object: "model", OwnedBy: "openai"},
	{ID: "openai/gpt-4.1-nano", Object: "model", OwnedBy: "openai"},
	{ID: "openai/o3-mini-2025-01-31", Object: "model", OwnedBy: "openai"},
	{ID: "minimax/minimax-m2.7", Object: "model", OwnedBy: "minimax"},
	{ID: "z-ai/glm-5.1", Object: "model", OwnedBy: "zhipu"},
	{ID: "x-ai/grok-4-07-09", Object: "model", OwnedBy: "xai"},
}

func init() {
	now := time.Now().Unix()
	for i := range supportedModels {
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
