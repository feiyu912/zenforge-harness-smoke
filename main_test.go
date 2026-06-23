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
