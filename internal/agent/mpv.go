package agent

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// mpvProcess runs one mpv instance and tracks whether it is still alive.
type mpvProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
}

func startMpv(path string, args []string, logf func(string, ...any)) (*mpvProcess, error) {
	// Not CommandContext: mpv must outlive the request that started it.
	cmd := exec.Command(path, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mpv: %w", err)
	}
	p := &mpvProcess{cmd: cmd, done: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		if logf != nil {
			logf("mpv (pid %d) exited: %v", cmd.Process.Pid, err)
		}
		close(p.done)
	}()
	return p, nil
}

func (p *mpvProcess) alive() bool {
	if p == nil {
		return false
	}
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// stop asks mpv to quit, then kills it if it doesn't.
func (p *mpvProcess) stop() {
	if !p.alive() {
		return
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		<-p.done
	}
}

// baseMpvArgs are shared by live playback and the slideshow.
func baseMpvArgs() []string {
	return []string{
		"--fullscreen",
		"--no-terminal",
		"--really-quiet",
		"--no-osc",
		"--no-input-default-bindings",
		"--input-vo-keyboard=no",
		"--cursor-autohide=always",
		"--hwdec=auto-safe",
		"--background=color",
		"--background-color=#000000",
	}
}

// waitBriefly gives mpv a moment to fail fast (bad URL, missing codec) so
// Play can report an error instead of claiming success.
func waitBriefly(ctx context.Context, p *mpvProcess, d time.Duration) error {
	select {
	case <-p.done:
		return fmt.Errorf("mpv exited immediately")
	case <-time.After(d):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// guarded serializes access to a running process.
type guarded struct {
	mu   sync.Mutex
	proc *mpvProcess
}
