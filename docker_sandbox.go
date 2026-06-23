package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/feiyu912/zenforge/sandbox"
)

type DockerSandbox struct {
	Image string
}

func (d DockerSandbox) Open(ctx context.Context, req sandbox.OpenRequest) (*sandbox.Session, error) {
	image := strings.TrimSpace(d.Image)
	if image == "" {
		image = strings.TrimSpace(req.EnvironmentID)
	}
	if image == "" {
		image = "alpine:3.20"
	}
	return &sandbox.Session{
		ID:            sandbox.SessionKey(req.RunID, req.SubtaskID),
		RunID:         req.RunID,
		SubtaskID:     req.SubtaskID,
		EnvironmentID: image,
		WorkingDir:    "/workspace",
		Metadata: map[string]any{
			"mounts": mountsState(req.Mounts),
		},
	}, nil
}

func (d DockerSandbox) Execute(ctx context.Context, session *sandbox.Session, req sandbox.ExecuteRequest) (sandbox.ExecuteResult, error) {
	if session == nil {
		return sandbox.ExecuteResult{ExitCode: 1}, fmt.Errorf("%w: nil session", sandbox.ErrSessionOpenFailed)
	}
	image := session.EnvironmentID
	if image == "" {
		image = d.Image
	}
	if image == "" {
		image = "alpine:3.20"
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	args := []string{"run", "--rm"}
	for _, mount := range mountsFromState(session.Metadata["mounts"]) {
		mode := strings.TrimSpace(mount.Mode)
		if mode == "" {
			mode = "rw"
		}
		args = append(args, "-v", mount.Source+":"+mount.Destination+":"+mode)
	}
	cwd := containerCWD(req.CWD, mountsFromState(session.Metadata["mounts"]))
	args = append(args, "-w", cwd, image, "sh", "-lc", req.Command)

	cmd := exec.CommandContext(runCtx, "docker", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	if runCtx.Err() == context.DeadlineExceeded {
		err = sandbox.ErrTimeout
		exitCode = 124
	}
	return sandbox.ExecuteResult{
		ExitCode:         exitCode,
		Stdout:           stdout.String(),
		Stderr:           stderr.String(),
		WorkingDirectory: cwd,
		Metadata: map[string]any{
			"image": image,
		},
	}, err
}

func (d DockerSandbox) Close(ctx context.Context, session *sandbox.Session) error {
	return nil
}

func mountsState(mounts []sandbox.Mount) []map[string]string {
	out := make([]map[string]string, 0, len(mounts))
	for _, mount := range mounts {
		out = append(out, map[string]string{
			"source":      mount.Source,
			"destination": mount.Destination,
			"mode":        mount.Mode,
		})
	}
	return out
}

func mountsFromState(value any) []sandbox.Mount {
	raw, _ := value.([]map[string]string)
	out := make([]sandbox.Mount, 0, len(raw))
	for _, mount := range raw {
		out = append(out, sandbox.Mount{
			Source:      mount["source"],
			Destination: mount["destination"],
			Mode:        mount["mode"],
		})
	}
	return out
}

func containerCWD(hostCWD string, mounts []sandbox.Mount) string {
	hostCWD = mustAbs(hostCWD)
	for _, mount := range mounts {
		source := mustAbs(mount.Source)
		rel, err := filepath.Rel(source, hostCWD)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			if rel == "." {
				return mount.Destination
			}
			return filepath.ToSlash(filepath.Join(mount.Destination, rel))
		}
	}
	return "/workspace"
}

func mustAbs(path string) string {
	if strings.TrimSpace(path) == "" {
		path = "."
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	real, err := filepath.EvalSymlinks(abs)
	if err == nil {
		return real
	}
	if _, statErr := os.Stat(abs); statErr == nil {
		return abs
	}
	return abs
}
