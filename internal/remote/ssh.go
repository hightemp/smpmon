package remote

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// DefaultRemoteCommand starts the agent on the server. ~/.local/bin is
// added to PATH because that's where `connect --install` puts smpmon and it
// often isn't on PATH for non-interactive sessions. `env` works in any
// login shell, including fish.
const DefaultRemoteCommand = `env PATH="$HOME/.local/bin:$PATH" smpmon agent --stdio`

// SSHOptions configures an SSH connection.
type SSHOptions struct {
	Target  string   // user@host or an alias from ~/.ssh/config
	Command string   // remote command; DefaultRemoteCommand if empty
	Binary  string   // ssh executable; "ssh" if empty
	Args    []string // extra ssh arguments
}

// sshArgs returns the ssh command line. BatchMode makes ssh fail instead of
// prompting, which would corrupt the TUI: keys or an ssh-agent are needed.
func (o SSHOptions) sshArgs(command string) []string {
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}
	args = append(args, o.Args...)
	return append(args, o.Target, command)
}

func (o SSHOptions) binary() string {
	if o.Binary == "" {
		return "ssh"
	}
	return o.Binary
}

// SSHDialer connects by running the agent over ssh.
func SSHDialer(o SSHOptions) Dialer {
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		cmd := o.Command
		if cmd == "" {
			cmd = DefaultRemoteCommand
		}
		return startSSH(ctx, o, cmd)
	}
}

// sshStream is the stdio of an ssh process.
type sshStream struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *tailBuffer
	target string

	once    sync.Once
	waitErr error
	done    chan struct{}
}

func startSSH(ctx context.Context, o SSHOptions, command string) (*sshStream, error) {
	cmd := exec.CommandContext(ctx, o.binary(), o.sshArgs(command)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	s := &sshStream{cmd: cmd, stdin: stdin, stdout: stdout, stderr: &tailBuffer{max: 4096}, target: o.Target, done: make(chan struct{})}
	cmd.Stderr = s.stderr
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", o.binary(), err)
	}
	go func() {
		s.waitErr = cmd.Wait()
		close(s.done)
	}()
	return s, nil
}

func (s *sshStream) Write(p []byte) (int, error) { return s.stdin.Write(p) }

// Read returns ssh's own error message when the process ends, e.g.
// "Permission denied (publickey)".
func (s *sshStream) Read(p []byte) (int, error) {
	n, err := s.stdout.Read(p)
	if errors.Is(err, io.EOF) {
		select {
		case <-s.done:
		case <-time.After(2 * time.Second):
		}
		if e := s.exitError(); e != nil {
			return n, e
		}
	}
	return n, err
}

func (s *sshStream) exitError() error {
	select {
	case <-s.done:
	default:
		return nil
	}
	var ee *exec.ExitError
	if !errors.As(s.waitErr, &ee) {
		return nil
	}
	msg := s.stderr.lastLine()
	switch ee.ExitCode() {
	case 127:
		return fmt.Errorf("smpmon is not installed on %s (run `smpmon connect --install %s`): %s", s.target, s.target, msg)
	case 255:
		if msg == "" {
			msg = "connection failed"
		}
		if strings.Contains(msg, "Host key verification failed") {
			msg += " (connect once with plain ssh to accept the host key)"
		}
		return fmt.Errorf("ssh %s: %s", s.target, msg)
	}
	if msg == "" {
		msg = ee.Error()
	}
	return fmt.Errorf("%s: %s", s.target, msg)
}

func (s *sshStream) Close() error {
	s.once.Do(func() {
		_ = s.stdin.Close()
		select {
		case <-s.done:
		case <-time.After(time.Second):
			if s.cmd.Process != nil {
				_ = s.cmd.Process.Kill()
			}
		}
	})
	return nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.max {
		t.buf = t.buf[len(t.buf)-t.max:]
	}
	return len(p), nil
}

func (t *tailBuffer) lastLine() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	lines := bytes.Split(bytes.TrimSpace(t.buf), []byte("\n"))
	return strings.TrimSpace(string(lines[len(lines)-1]))
}

// RunSSH runs a command over ssh and returns its stdout. stdin, if not nil,
// is fed to the command.
func RunSSH(ctx context.Context, o SSHOptions, command string, stdin io.Reader) ([]byte, error) {
	cmd := exec.CommandContext(ctx, o.binary(), o.sshArgs(command)...)
	cmd.Stdin = stdin
	var out bytes.Buffer
	errBuf := &tailBuffer{max: 4096}
	cmd.Stdout, cmd.Stderr = &out, errBuf
	if err := cmd.Run(); err != nil {
		if msg := errBuf.lastLine(); msg != "" {
			return out.Bytes(), fmt.Errorf("%s: %s", o.Target, msg)
		}
		return out.Bytes(), fmt.Errorf("%s: %w", o.Target, err)
	}
	return out.Bytes(), nil
}
