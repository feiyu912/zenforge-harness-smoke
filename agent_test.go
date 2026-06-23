package main

import (
	"context"
	"testing"
	"time"

	"github.com/feiyu912/zenforge"
)

// TestStreamEventSequence runs the scripted model end-to-end and asserts the
// shape of the event stream: run start, a tool.call for local_skill, a
// tool.result, a tool.call for shell, an approval.requested / approval.resolved
// pair, a terminal tool result/error, and run.done. This exercises Config,
// the ModeReAct loop, the tool registry, eventlogmemory, and AlwaysAllow.
func TestStreamEventSequence(t *testing.T) {
	agent, err := buildAgent(appOptions{Model: "scripted", Approve: "auto", Docker: false, Workspace: "."})
	if err != nil {
		t.Fatalf("buildAgent returned error: %v", err)
	}

	events, err := agent.Stream(context.Background(), zenforgeTask("walk the loop"))
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}

	var (
		sawRunStarted   bool
		sawRunDone      bool
		sawLocalSkill   bool
		localSkillOut   string
		sawShellCall    bool
		sawApprovalReq  bool
		sawApprovalRes  bool
		approvalAction  string
		terminalToolOut string
		terminalToolErr string
	)

	for ev := range events {
		switch ev.Type {
		case zenforge.EventRunStarted:
			sawRunStarted = true
		case zenforge.EventRunDone:
			sawRunDone = true
		case zenforge.EventToolCall:
			name, _ := ev.Payload["toolName"].(string)
			switch name {
			case "local_skill":
				sawLocalSkill = true
			case "shell":
				sawShellCall = true
			}
		case zenforge.EventToolResult:
			name, _ := ev.Payload["toolName"].(string)
			if name == "local_skill" {
				localSkillOut, _ = ev.Payload["output"].(string)
			}
			if name == "shell" {
				terminalToolOut, _ = ev.Payload["output"].(string)
				terminalToolErr, _ = ev.Payload["error"].(string)
			}
		case zenforge.EventToolError:
			name, _ := ev.Payload["toolName"].(string)
			if name == "shell" {
				terminalToolOut, _ = ev.Payload["output"].(string)
				terminalToolErr, _ = ev.Payload["error"].(string)
			}
		case zenforge.EventApprovalRequested:
			name, _ := ev.Payload["toolName"].(string)
			if name == "shell" {
				sawApprovalReq = true
			}
		case zenforge.EventApprovalResolved:
			name, _ := ev.Payload["toolName"].(string)
			if name == "shell" {
				sawApprovalRes = true
				approvalAction, _ = ev.Payload["action"].(string)
			}
		}
	}

	if !sawRunStarted {
		t.Fatal("expected EventRunStarted in stream")
	}
	if !sawRunDone {
		t.Fatal("expected EventRunDone in stream")
	}
	if !sawLocalSkill {
		t.Fatal("expected a tool.call for local_skill")
	}
	if localSkillOut == "" {
		t.Fatal("local_skill tool.result had empty output")
	}
	if !sawShellCall {
		t.Fatal("expected a tool.call for shell")
	}
	if !sawApprovalReq {
		t.Fatal("expected an approval.requested for shell")
	}
	if !sawApprovalRes {
		t.Fatal("expected an approval.resolved for shell")
	}
	if approvalAction != "approve" {
		t.Fatalf("expected approval action %q, got %q", "approve", approvalAction)
	}
	if terminalToolOut == "" && terminalToolErr == "" {
		t.Fatal("expected a non-empty tool result or error from the shell tool")
	}
}

// TestStreamTerminatesWithinTimeout is a guardrail that Stream always
// closes the channel within a reasonable window, even if the model never
// produces a final answer.
func TestStreamTerminatesWithinTimeout(t *testing.T) {
	agent, err := buildAgent(appOptions{Model: "scripted", Approve: "auto", Docker: false, Workspace: "."})
	if err != nil {
		t.Fatalf("buildAgent returned error: %v", err)
	}
	events, err := agent.Stream(context.Background(), zenforgeTask("terminate"))
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	done := make(chan struct{})
	go func() {
		for range events {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stream did not terminate within 10s")
	}
}
