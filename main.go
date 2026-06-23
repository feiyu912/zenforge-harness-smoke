package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	approvalcli "github.com/feiyu912/zenforge/approval/cli"
	checkpointmemory "github.com/feiyu912/zenforge/checkpoint/memory"
	eventlogmemory "github.com/feiyu912/zenforge/eventlog/memory"
	"github.com/feiyu912/zenforge/model"
	"github.com/feiyu912/zenforge/model/anthropic"
	"github.com/feiyu912/zenforge/model/openai"
	"github.com/feiyu912/zenforge/policy"
	"github.com/feiyu912/zenforge/sandbox"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/tools"
	shelltool "github.com/feiyu912/zenforge/tools/shell"
	"github.com/feiyu912/zenforge/trace"
)

type skillInput struct {
	Topic string `json:"topic" jsonschema:"required,description=Topic to answer from the local skill"`
}

type appOptions struct {
	Model      string
	Approve    string
	Docker     bool
	Image      string
	Question   string
	Workspace  string
	MiniMaxKey string
	BaseURL    string
	Verbose    bool
	HTTP       bool
	Addr       string
}

func main() {
	_ = loadDotEnv(".env")
	opts := parseFlags()
	ctx := context.Background()
	// `go run .` starts the test server by default — it's the surface the
	// minimal UI in ../zenforge-testui talks to. Pass -q "..." to run a
	// single CLI query instead.
	if !opts.HTTP && opts.Question == "" && len(flag.Args()) == 0 {
		opts.HTTP = true
	}
	if opts.HTTP {
		// Default the server to the offline scripted model unless the user
		// passed --model explicitly. This keeps `go run .` self-contained.
		if !modelFlagSet {
			opts.Model = "scripted"
		}
		if err := runHTTPServer(ctx, opts); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(ctx, opts); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

var modelFlagSet bool

func parseFlags() appOptions {
	opts := optionsFromEnv()
	flag.StringVar(&opts.Model, "model", opts.Model, "model: auto|scripted|minimax|minimax-openai")
	flag.StringVar(&opts.Approve, "approve", opts.Approve, "approval: auto|prompt")
	flag.BoolVar(&opts.Docker, "docker", opts.Docker, "run shell tool in Docker sandbox")
	flag.StringVar(&opts.Image, "image", opts.Image, "Docker image for sandbox shell")
	flag.StringVar(&opts.Question, "q", opts.Question, "question to ask the agent")
	flag.StringVar(&opts.Workspace, "workspace", opts.Workspace, "host workspace to mount into Docker")
	flag.StringVar(&opts.MiniMaxKey, "minimax-key-env", opts.MiniMaxKey, "environment variable containing MiniMax API key")
	flag.StringVar(&opts.BaseURL, "base-url", opts.BaseURL, "MiniMax-compatible base URL")
	flag.BoolVar(&opts.Verbose, "verbose", opts.Verbose, "print detailed harness events and tool results")
	flag.BoolVar(&opts.HTTP, "http", opts.HTTP, "start a local HTTP server instead of running a single query")
	flag.StringVar(&opts.Addr, "addr", opts.Addr, "HTTP listen address (only with --http)")
	flag.Parse()
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "model" {
			modelFlagSet = true
		}
	})
	if opts.Question == "" {
		opts.Question = strings.TrimSpace(strings.Join(flag.Args(), " "))
	}
	return opts
}

func optionsFromEnv() appOptions {
	modelName := envString("ZENFORGE_MODEL", "auto")
	if modelName == "auto" && usableAPIKey(os.Getenv("ANTHROPIC_API_KEY")) {
		modelName = "minimax"
	}
	if modelName == "auto" {
		modelName = "scripted"
	}
	keyEnv := envString("ZENFORGE_MINIMAX_KEY_ENV", "")
	if keyEnv == "" {
		keyEnv = "ANTHROPIC_API_KEY"
		if modelName == "minimax-openai" {
			keyEnv = "MINIMAX_API_KEY"
		}
	}
	baseURL := envString("ZENFORGE_BASE_URL", "")
	if baseURL == "" {
		baseURL = os.Getenv("ANTHROPIC_BASE_URL")
	}
	return appOptions{
		Model:      modelName,
		Approve:    envString("ZENFORGE_APPROVE", "auto"),
		Docker:     envBool("ZENFORGE_DOCKER", true),
		Image:      envString("ZENFORGE_DOCKER_IMAGE", "alpine:3.20"),
		Question:   strings.TrimSpace(os.Getenv("ZENFORGE_QUESTION")),
		Workspace:  envString("ZENFORGE_WORKSPACE", "."),
		MiniMaxKey: keyEnv,
		BaseURL:    baseURL,
		Verbose:    envBool("ZENFORGE_VERBOSE", false),
		HTTP:       envBool("ZENFORGE_HTTP", false),
		Addr:       envString("ZENFORGE_ADDR", ""),
	}
}

func usableAPIKey(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	return lower != "..." &&
		lower != "your-key" &&
		lower != "your_api_key" &&
		lower != "your-minimax-api-key" &&
		!strings.Contains(lower, "填") &&
		!strings.Contains(lower, "replace")
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func run(ctx context.Context, opts appOptions) error {
	if opts.Question == "" {
		question, err := promptQuestion()
		if err != nil {
			return err
		}
		opts.Question = question
	}
	if strings.ToLower(strings.TrimSpace(opts.Model)) == "scripted" {
		fmt.Fprintln(os.Stderr, "Using offline scripted model. Set ANTHROPIC_API_KEY to use MiniMax.")
	}
	agent, err := buildAgent(opts)
	if err != nil {
		return err
	}
	events, err := agent.Stream(ctx, zenforge.Task{
		RunID: "run_harness_smoke",
		Input: opts.Question,
	})
	if err != nil {
		return err
	}
	var final string
	for event := range events {
		printEvent(event, opts.Verbose)
		if event.Type == zenforge.EventRunDone {
			final = fmt.Sprint(event.Payload["output"])
		}
		if event.Type == zenforge.EventRunError {
			if authHint := providerAuthHint(fmt.Sprint(event.Payload["error"])); authHint != "" {
				return errors.New(authHint)
			}
		}
	}
	if final != "" && !opts.Verbose {
		fmt.Println("\nAssistant:", final)
	}
	return nil
}

func promptQuestion() (string, error) {
	fmt.Print("You: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", err
		}
		return "", fmt.Errorf("question is required")
	}
	question := strings.TrimSpace(scanner.Text())
	if question == "" {
		return "", fmt.Errorf("question is required")
	}
	return question, nil
}

func newSkillTool() tool.Tool {
	return tools.Must("local_skill", "Answer a local skill question without using the model provider.", func(ctx context.Context, in skillInput) (string, error) {
		return "local skill says: ZenForge is an application-owned Go harness; the app supplies model, tools, approval, and sandbox.", nil
	})
}

func buildAgent(opts appOptions) (*zenforge.Agent, error) {
	skill := newSkillTool()

	shell, err := shellTool(opts)
	if err != nil {
		return nil, err
	}
	modelAdapter, err := modelFor(opts)
	if err != nil {
		return nil, err
	}
	broker, err := approvalBroker(opts)
	if err != nil {
		return nil, err
	}

	return zenforge.New(zenforge.Config{
		Model:        modelAdapter,
		Instructions: "Answer briefly. Use local_skill for harness facts. Use shell to prove the sandbox when useful.",
		Tools:        []tool.Tool{skill, shell},
		Approval:     broker,
		Events:       eventlogmemory.New(),
		Checkpoints:  checkpointmemory.New(),
		Trace:        trace.Redact(trace.NewMemorySink()),
		MaxSteps:     6,
		Mode:         zenforge.ModeReact,
	}), nil
}

func modelFor(opts appOptions) (model.Model, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Model)) {
	case "", "scripted":
		return &scriptedModel{}, nil
	case "minimax":
		apiKey := os.Getenv(opts.MiniMaxKey)
		if !usableAPIKey(apiKey) {
			fmt.Fprintf(os.Stderr, "%s is not set to a real MiniMax key; falling back to offline scripted model.\n", opts.MiniMaxKey)
			return &scriptedModel{}, nil
		}
		baseURL := opts.BaseURL
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://api.minimaxi.com/anthropic/v1"
		}
		return anthropic.New(anthropic.Config{
			APIKey:  apiKey,
			Model:   "MiniMax-M3",
			BaseURL: baseURL,
		}), nil
	case "minimax-openai":
		apiKey := os.Getenv(opts.MiniMaxKey)
		if !usableAPIKey(apiKey) {
			return nil, fmt.Errorf("%s is not set to a real MiniMax OpenAI-compatible API key", opts.MiniMaxKey)
		}
		baseURL := opts.BaseURL
		if strings.TrimSpace(baseURL) == "" {
			baseURL = "https://api.minimax.io/v1"
		}
		return openai.New(openai.Config{
			APIKey:  apiKey,
			Model:   "MiniMax-M3",
			BaseURL: baseURL,
		}), nil
	default:
		return nil, fmt.Errorf("unknown model %q", opts.Model)
	}
}

func providerAuthHint(message string) string {
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "401") && !strings.Contains(lower, "unauthorized") && !strings.Contains(lower, "invalid api key") {
		return ""
	}
	return "MiniMax rejected the API key. Check ANTHROPIC_API_KEY in .env or your shell environment. The DeepAgents-style MiniMax path uses ANTHROPIC_BASE_URL=https://api.minimaxi.com/anthropic/v1."
}

func approvalBroker(opts appOptions) (approval.Broker, error) {
	switch strings.ToLower(strings.TrimSpace(opts.Approve)) {
	case "", "auto":
		return approval.AlwaysAllow(), nil
	case "prompt":
		if !stdinInteractive() {
			fmt.Fprintln(os.Stderr, "ZENFORGE_APPROVE=prompt needs an interactive terminal; using auto approval for this non-interactive run.")
			return approval.AlwaysAllow(), nil
		}
		return approvalcli.New(os.Stdin, os.Stderr), nil
	default:
		return nil, fmt.Errorf("unknown approval mode %q", opts.Approve)
	}
}

func stdinInteractive() bool {
	info, err := os.Stdin.Stat()
	return err == nil && (info.Mode()&os.ModeCharDevice) != 0
}

func shellTool(opts appOptions) (tool.Tool, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	p := policy.ShellPolicy{
		WorkingDir:      workingDir,
		AllowCommands:   []string{"go version"},
		RequireApproval: true,
		MaxTimeout:      20 * time.Second,
		MaxOutputBytes:  32_000,
	}
	if !opts.Docker {
		return shelltool.New(shelltool.Config{Policy: p})
	}
	return shelltool.New(shelltool.Config{
		Policy:        p,
		Backend:       shelltool.ShellBackendSandbox,
		Sandbox:       DockerSandbox{Image: opts.Image},
		EnvironmentID: opts.Image,
		Mounts: []sandbox.Mount{
			{Source: mustAbs(opts.Workspace), Destination: "/workspace", Mode: "rw"},
		},
	})
}

func printEvent(event zenforge.Event, verbose bool) {
	switch event.Type {
	case zenforge.EventModelDelta:
		if verbose {
			fmt.Print(event.Payload["textDelta"])
		}
	case zenforge.EventToolCall:
		if verbose {
			fmt.Printf("\n[tool] %s %s\n", event.Payload["toolName"], event.Payload["arguments"])
			return
		}
		fmt.Fprintf(os.Stderr, "\n→ tool: %s\n", event.Payload["toolName"])
	case zenforge.EventApprovalRequested:
		fmt.Fprintf(os.Stderr, "→ approval requested: %s\n", event.Payload["operation"])
	case zenforge.EventApprovalResolved:
		fmt.Fprintf(os.Stderr, "→ approval: %s\n", event.Payload["action"])
	case zenforge.EventToolResult:
		if verbose {
			fmt.Printf("[tool-result] %s\n", compact(event.Payload["output"]))
		}
	case zenforge.EventRunDone:
		if verbose {
			fmt.Printf("\n[done] %s\n", event.Payload["output"])
		}
	case zenforge.EventRunError:
		fmt.Fprintf(os.Stderr, "\nerror: %s\n", event.Payload["error"])
	}
}

func compact(value any) string {
	text := fmt.Sprint(value)
	text = strings.ReplaceAll(text, "\n", `\n`)
	if len(text) > 320 {
		return text[:320] + "..."
	}
	return text
}

type scriptedModel struct {
	turn int
}

func (m *scriptedModel) Generate(ctx context.Context, req model.Request) (*model.Response, error) {
	stream, err := m.Stream(ctx, req)
	if err != nil {
		return nil, err
	}
	response := model.Response{}
	for event := range stream {
		if event.Error != nil {
			return nil, event.Error
		}
		if event.Message != nil {
			response.Message = *event.Message
		}
		if event.Delta != "" {
			response.Message.Role = "assistant"
			response.Message.Content += event.Delta
		}
	}
	return &response, nil
}

func (m *scriptedModel) Stream(ctx context.Context, req model.Request) (<-chan model.Event, error) {
	m.turn++
	out := make(chan model.Event, 1)
	go func() {
		defer close(out)
		switch m.turn {
		case 1:
			out <- model.Event{Message: &model.Message{Role: "assistant", ToolCalls: []model.ToolCallSpec{{
				ID:        "call_skill",
				Name:      "local_skill",
				Arguments: json.RawMessage(`{"topic":"zenforge harness"}`),
			}}}}
		case 2:
			out <- model.Event{Message: &model.Message{Role: "assistant", ToolCalls: []model.ToolCallSpec{{
				ID:        "call_shell",
				Name:      "shell",
				Arguments: json.RawMessage(`{"command":"uname -a","description":"prove the shell tool runs in the configured Docker sandbox"}`),
			}}}}
		default:
			out <- model.Event{Delta: "Yes. The app supplied the model, a local skill tool, HITL approval, and a Docker-backed shell sandbox through ZenForge's harness API."}
		}
	}()
	return out, nil
}
