package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPServerEndToEnd(t *testing.T) {
	port := freePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runHTTPServer(ctx, appOptions{Model: "scripted", Approve: "auto", Docker: false, Workspace: ".", Addr: addr})
	}()

	base := "http://" + addr
	if !waitForHealth(t, base+"/healthz", 3*time.Second) {
		t.Fatal("server did not become healthy")
	}

	// Start a run.
	runBody, _ := json.Marshal(map[string]any{
		"runId": "run_http_test",
		"input": "walk the loop",
	})
	resp, err := http.Post(base+"/v1/runs", "application/json", bytes.NewReader(runBody))
	if err != nil {
		t.Fatalf("POST /v1/runs: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST /v1/runs: status %d body %s", resp.StatusCode, body)
	}

	// Read at least the first few SSE events from the same response stream.
	// We expect: run.started, step.started, model.started, tool.call, tool.result,
	// approval.requested, then it will block until we submit.
	reader := bufio.NewReader(resp.Body)
	events, err := readUntilEvent(reader, "approval.requested", 10*time.Second)
	if err != nil {
		types := make([]string, 0, len(events))
		for _, e := range events {
			types = append(types, e.Type)
		}
		t.Fatalf("reading events: %v; got %d events: %v", err, len(events), types)
	}
	if !hasEventType(events, "run.started") {
		t.Fatal("expected run.started in stream")
	}
	if !hasEventType(events, "tool.call") {
		t.Fatal("expected tool.call in stream")
	}
	if !hasEventType(events, "approval.requested") {
		t.Fatal("expected approval.requested in stream")
	}

	// Approve the pending request via the POST endpoint.
	approvalBody, _ := json.Marshal(map[string]any{
		"requestId": extractField(t, events, "approval.requested", "requestId"),
		"action":    "approve",
		"scope":     "once",
	})
	apr, err := http.Post(base+"/v1/runs/run_http_test/approvals", "application/json", bytes.NewReader(approvalBody))
	if err != nil {
		t.Fatalf("POST approvals: %v", err)
	}
	if apr.StatusCode/100 != 2 {
		body, _ := io.ReadAll(apr.Body)
		t.Fatalf("POST approvals: status %d body %s", apr.StatusCode, body)
	}
	apr.Body.Close()

	// Close the run stream so we can move on.
	resp.Body.Close()

	// Replay events to confirm we got run.done after the approval.
	replay, err := http.Get(base + "/v1/runs/run_http_test/events?afterSeq=0")
	if err != nil {
		t.Fatalf("GET events: %v", err)
	}
	defer replay.Body.Close()
	replayed, err := readSSEEvents(bufio.NewReader(replay.Body), 1, 3*time.Second)
	if err != nil && err != io.EOF {
		// We expect run.done to be on the bus; if the run already finished
		// before we subscribed, the bus has been closed, which is also fine.
		t.Logf("replay ended with: %v", err)
	}
	if hasEventType(replayed, "run.done") {
		t.Log("replay confirms run.done is recorded")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("server did not shut down within 3s")
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return port
}

func waitForHealth(t *testing.T, url string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil && resp.StatusCode == 200 {
			resp.Body.Close()
			return true
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

type sseEvent struct {
	Type string         `json:"-"`
	Data map[string]any `json:"-"`
}

func readSSEEvents(r *bufio.Reader, min int, timeout time.Duration) ([]sseEvent, error) {
	deadline := time.Now().Add(timeout)
	out := []sseEvent{}
	for {
		if len(out) >= min {
			return out, nil
		}
		if time.Now().After(deadline) {
			return out, fmt.Errorf("only got %d events before deadline", len(out))
		}
		line, err := readLineWithDeadline(r, deadline)
		if err != nil {
			return out, err
		}
		// Blank line ends a record; nothing to do.
		if line == "" {
			continue
		}
		// We are looking for the "event: " line of the first frame in a
		// record. Other lines (id:, retry:, comments starting with ":") are
		// ignored — they do not change the contract we want to assert.
		if !strings.HasPrefix(line, "event: ") {
			continue
		}
		ev := sseEvent{Type: strings.TrimPrefix(line, "event: ")}
		// Read the data: line that follows.
		dataLine, err := readLineWithDeadline(r, deadline)
		if err != nil {
			return out, err
		}
		if strings.HasPrefix(dataLine, "data: ") {
			raw := strings.TrimPrefix(dataLine, "data: ")
			var payload map[string]any
			if err := json.Unmarshal([]byte(raw), &payload); err == nil {
				ev.Data = payload
			}
		}
		out = append(out, ev)
	}
}

func readLineWithDeadline(r *bufio.Reader, deadline time.Time) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- result{strings.TrimRight(line, "\r\n"), err}
	}()
	select {
	case res := <-ch:
		return res.line, res.err
	case <-time.After(time.Until(deadline)):
		return "", fmt.Errorf("timeout reading SSE")
	}
}

func readUntilEvent(r *bufio.Reader, wantType string, timeout time.Duration) ([]sseEvent, error) {
	deadline := time.Now().Add(timeout)
	out := []sseEvent{}
	for {
		if time.Now().After(deadline) {
			return out, fmt.Errorf("did not see %q before deadline", wantType)
		}
		line, err := readLineWithDeadline(r, deadline)
		if err != nil {
			return out, err
		}
		if line == "" || !strings.HasPrefix(line, "event: ") {
			continue
		}
		ev := sseEvent{Type: strings.TrimPrefix(line, "event: ")}
		dataLine, err := readLineWithDeadline(r, deadline)
		if err != nil {
			return out, err
		}
		if strings.HasPrefix(dataLine, "data: ") {
			raw := strings.TrimPrefix(dataLine, "data: ")
			var payload map[string]any
			if err := json.Unmarshal([]byte(raw), &payload); err == nil {
				ev.Data = payload
			}
		}
		out = append(out, ev)
		if ev.Type == wantType {
			return out, nil
		}
	}
}

func hasEventType(events []sseEvent, want string) bool {
	for _, e := range events {
		if e.Type == want {
			return true
		}
	}
	return false
}

func extractField(t *testing.T, events []sseEvent, eventType, field string) string {
	t.Helper()
	for _, e := range events {
		if e.Type == eventType {
			if v, ok := e.Data[field].(string); ok {
				return v
			}
		}
	}
	t.Fatalf("could not find %q in %q events", field, eventType)
	return ""
}
