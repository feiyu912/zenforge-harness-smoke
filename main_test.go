package main

import (
	"context"
	"testing"

	"github.com/feiyu912/zenforge"
)

func TestBuildAgentWithScriptedModel(t *testing.T) {
	agent, err := buildAgent(appOptions{Model: "scripted", Approve: "auto", Docker: false, Workspace: "."})
	if err != nil {
		t.Fatalf("buildAgent returned error: %v", err)
	}
	result, err := agent.Run(context.Background(), zenforgeTask("hello"))
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Output == "" {
		t.Fatal("result output is empty")
	}
}

func zenforgeTask(input string) zenforge.Task {
	return zenforge.Task{RunID: "run_test", Input: input}
}

func TestAnthropicBaseURL(t *testing.T) {
	tests := map[string]string{
		"https://api.minimaxi.com/anthropic":      "https://api.minimaxi.com/anthropic/v1",
		"https://api.minimaxi.com/anthropic/":     "https://api.minimaxi.com/anthropic/v1",
		"https://api.minimaxi.com/anthropic/v1":   "https://api.minimaxi.com/anthropic/v1",
		"https://proxy.example.test/anthropic/v1": "https://proxy.example.test/anthropic/v1",
	}
	for input, want := range tests {
		if got := anthropicBaseURL(input); got != want {
			t.Errorf("anthropicBaseURL(%q) = %q, want %q", input, got, want)
		}
	}
}
