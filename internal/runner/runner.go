package runner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"syscall"
)

// Run executes a single external command in the given directory, streaming
// stdout, stderr, and stdin directly. It blocks until the command completes.
func Run(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// RunAll executes a slice of commands sequentially in the given directory.
// Each element of commands is a []string where commands[i][0] is the binary
// name and commands[i][1:] are its arguments. It returns the first error.
func RunAll(ctx context.Context, dir string, commands [][]string) error {
	for _, c := range commands {
		if len(c) == 0 {
			continue
		}
		if err := Run(ctx, dir, c[0], c[1:]...); err != nil {
			return err
		}
	}
	return nil
}

// Proc describes a long-running process for use with RunConcurrent.
type Proc struct {
	Name string
	Dir  string
	Cmd  string
	Args []string
}

// RunConcurrent starts all procs concurrently and waits for all of them to
// finish. It installs a signal handler for SIGINT/SIGTERM so that a single
// interrupt cancels every child process. It returns the first non-nil error
// from any process (or nil if all succeed).
func RunConcurrent(ctx context.Context, procs []Proc) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	var (
		mu      sync.Mutex
		firstErr error
		wg      sync.WaitGroup
	)

	for _, p := range procs {
		p := p
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.CommandContext(ctx, p.Cmd, p.Args...)
			cmd.Dir = p.Dir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Stdin = os.Stdin
			if err := cmd.Start(); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: start: %w", p.Name, err)
				}
				mu.Unlock()
				return
			}
			if err := cmd.Wait(); err != nil {
				// Context cancellation causes an error; treat it as nil so a
				// graceful shutdown does not surface as a failure.
				if ctx.Err() != nil {
					return
				}
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("%s: %w", p.Name, err)
				}
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	return firstErr
}
