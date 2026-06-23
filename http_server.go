package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/feiyu912/zenforge"
	"github.com/feiyu912/zenforge/approval"
	"github.com/feiyu912/zenforge/checkpoint/memory"
	"github.com/feiyu912/zenforge/eventlog"
	eventlogmemory "github.com/feiyu912/zenforge/eventlog/memory"
	harnesshttp "github.com/feiyu912/zenforge/server/harnesshttp"
	"github.com/feiyu912/zenforge/server/sse"
	"github.com/feiyu912/zenforge/tool"
	"github.com/feiyu912/zenforge/trace"
)

// runHTTPServer starts a local HTTP server exposing the zenforge harnesshttp
// handler. It uses the same scripted-model agent as the CLI flow but with a
// PendingBroker so the test UI can submit HITL decisions.
func runHTTPServer(ctx context.Context, opts appOptions) error {
	bus := eventlog.NewBus()
	store := eventlogmemory.New()
	events := eventlog.NewFanoutStore(store, bus)

	broker := approval.NewPendingBroker(32)

	skill := newSkillTool()
	shell, err := shellTool(opts)
	if err != nil {
		return err
	}
	modelAdapter, err := modelFor(opts)
	if err != nil {
		return err
	}
	agent, err := zenforge.New(zenforge.Config{
		Model:        modelAdapter,
		Instructions: "Answer briefly. Use local_skill for harness facts. Use shell to prove the sandbox when useful.",
		Tools:        []tool.Tool{skill, shell},
		Approval:     broker,
		Events:       events,
		Checkpoints:  memory.New(),
		Trace:        trace.Redact(trace.NewMemorySink()),
		MaxSteps:     6,
		Mode:         zenforge.ModeReact,
	}), nil
	if err != nil {
		return err
	}

	handler := &harnesshttp.Handler{
		Agent:      agent,
		Events:     events,
		Bus:        bus,
		Approvals:  broker,
		SSE:        sse.Options{RetryMillis: 1500},
		LiveBuffer: 256,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/runs", handler.ServeRun)
	mux.HandleFunc("POST /v1/runs/{id}/resume", handler.ServeResume)
	mux.HandleFunc("GET /v1/runs/{id}/events", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("live") == "true" {
			handler.ServeLiveEvents(w, r)
			return
		}
		handler.ServeEvents(w, r)
	})
	mux.HandleFunc("POST /v1/runs/{id}/approvals", handler.ServeApproval)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	addr := strings.TrimSpace(opts.Addr)
	if addr == "" {
		addr = "127.0.0.1:8088"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           withCORS(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("zenforge test server listening on http://%s", addr)
	log.Printf("  POST   /v1/runs                       start a run (body: {runId?, input, meta?})")
	log.Printf("  POST   /v1/runs/{id}/resume            resume a run (body: {runId})")
	log.Printf("  GET    /v1/runs/{id}/events?live=true  live SSE for a run")
	log.Printf("  GET    /v1/runs/{id}/events?afterSeq=N replay events from disk")
	log.Printf("  POST   /v1/runs/{id}/approvals         submit HITL decision (body: {requestId, action, scope?, reason?})")

	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}

// withCORS adds permissive CORS headers for the local vite dev server. It is
// only meant for the test-driver; do not use this in a public deployment.
func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Last-Event-ID")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
