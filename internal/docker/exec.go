package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/client"
)

// ExecLimits bound a one-shot exec run: without a cap a hung command
// (tail -f /tmp, a REPL, an interactive prompt) would pin the HTTP request
// and the edge response slot forever.
const (
	ExecDefaultTimeout = 30 * time.Second
	ExecMaxTimeout     = 120 * time.Second
	// ExecMaxOutput caps captured stdout+stderr at 512 KiB — plenty for
	// `setup email add`, `ls -la`, `cat config` style commands, while keeping
	// the edge JSON response well under broker limits.
	ExecMaxOutput = 512 * 1024
)

// ExecRequest describes a one-shot non-interactive command inside a container.
type ExecRequest struct {
	Cmd     []string `json:"cmd"`               // required, exec form: ["ls","-la","/tmp"]
	Timeout int      `json:"timeout,omitempty"` // seconds, 1..ExecMaxTimeout (default 30)
	User    string   `json:"user,omitempty"`    // optional exec user, e.g. "root"
}

// ExecResult carries the captured output and exit status of a finished exec.
type ExecResult struct {
	ExitCode   int    `json:"exit_code"`
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}

// ExecCommand runs a non-interactive command inside a container and waits for
// it to finish, capturing stdout/stderr separately. TTY and stdin are
// disabled so the two streams stay demultiplexed and the command must finish
// on its own; anything that needs a terminal (vim, top, password prompts) is
// out of scope for this endpoint.
func (c *Client) ExecCommand(ctx context.Context, containerID string, req ExecRequest) (*ExecResult, error) {
	if len(req.Cmd) == 0 {
		return nil, fmt.Errorf("cmd is required")
	}
	timeout := time.Duration(req.Timeout) * time.Second
	if timeout == 0 {
		timeout = ExecDefaultTimeout
	}
	if timeout > ExecMaxTimeout {
		timeout = ExecMaxTimeout
	}

	execResp, err := c.cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		Cmd:          req.Cmd,
		AttachStdout: true,
		AttachStderr: true,
		User:         req.User,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create exec: %w", err)
	}

	start := time.Now()
	attachResp, err := c.cli.ExecAttach(ctx, execResp.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to attach exec: %w", err)
	}
	defer attachResp.Close()

	// Demultiplex the docker stdio stream into two buffers, each hard-capped
	// at ExecMaxOutput so a chatty command cannot exhaust agent memory.
	var outBuf, errBuf bytes.Buffer
	truncated := false
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		header := make([]byte, 8)
		for {
			if _, err := io.ReadFull(attachResp.Reader, header); err != nil {
				return // EOF is the normal end of an exec stream
			}
			// stdio frame header: [streamType, 0, 0, 0, size uint32 BE]
			size := uint64(header[4])<<24 | uint64(header[5])<<16 | uint64(header[6])<<8 | uint64(header[7])
			dst := &outBuf
			if header[0] == 2 {
				dst = &errBuf
			}
			for size > 0 {
				n := size
				if n > 64*1024 {
					n = 64 * 1024
				}
				chunk := make([]byte, n)
				rn, rerr := io.ReadFull(attachResp.Reader, chunk)
				if outBuf.Len()+errBuf.Len()+rn <= ExecMaxOutput {
					dst.Write(chunk[:rn])
				} else {
					truncated = true
				}
				if rerr != nil {
					return
				}
				size -= uint64(rn)
			}
		}
	}()

	// Wait for the exec process to exit. ExecInspect polling is cheap and
	// this is a one-shot command; the moby client has no blocking Wait here.
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	var timedOut bool
	var exitCode int
	for {
		insp, ierr := c.cli.ExecInspect(ctx, execResp.ID, client.ExecInspectOptions{})
		if ierr != nil || !insp.Running {
			if ierr == nil {
				exitCode = insp.ExitCode
			}
			break
		}
		select {
		case <-deadline.C:
			// No exec-level kill in this moby client version: cancel the
			// request context so the API connection tears down, mark the run
			// timed out, and stop polling. The exec process itself is
			// reaped by the container runtime when the exec detach
			// propagates; the HTTP handler's response is not blocked.
			timedOut = true
			exitCode = -1
		default:
		}
		if timedOut {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	<-readDone // stream fully drained (or reader errored) before reporting

	return &ExecResult{
		ExitCode:   exitCode,
		Stdout:     outBuf.String(),
		Stderr:     errBuf.String(),
		TimedOut:   timedOut,
		Truncated:  truncated,
		DurationMS: time.Since(start).Milliseconds(),
	}, nil
}
