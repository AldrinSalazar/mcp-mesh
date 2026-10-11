package upstream

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/internal/proto"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type stdioUpstream struct{ cfg StdioConfig }

// NewStdio creates an upstream that starts a fresh process for every Open.
func NewStdio(cfg StdioConfig) Upstream {
	cfg.Args = append([]string(nil), cfg.Args...)
	cfg.Env = append([]string(nil), cfg.Env...)
	return &stdioUpstream{cfg: cfg}
}
func (*stdioUpstream) Transport() proto.Transport { return proto.TransportStdio }

func (u *stdioUpstream) Open(ctx context.Context) (mcp.Connection, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limit := u.cfg.MaxMessageSize
	if limit == 0 {
		limit = proto.DefaultMaxMessageSize
	}
	if limit < 0 {
		return nil, fmt.Errorf("open stdio: invalid message size %d", limit)
	}
	// CommandContext kills only the leader, and bypasses the stdio shutdown sequence.
	cmd := exec.Command(u.cfg.Command, u.cfg.Args...)
	cmd.Env = append(os.Environ(), u.cfg.Env...)
	cmd.Dir = u.cfg.Dir
	cmd.Stderr = u.cfg.Stderr
	configureProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open %s stdin: %w", u.cfg.Command, err)
	}
	// Own the pipes rather than using StdoutPipe: Wait must not close stdout
	// before the reader has consumed the child's final messages.
	stdout, out, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("open %s stdout: %w", u.cfg.Command, err)
	}
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		out.Close()
		return nil, fmt.Errorf("start %s: %w", u.cfg.Command, err)
	}
	out.Close()
	c := &stdioConn{cmd: cmd, stdin: stdin, stdout: stdout, limit: limit,
		incoming: make(chan readResult), closed: make(chan struct{}), readDone: make(chan struct{}), waitDone: make(chan struct{}), ownerDone: make(chan struct{}),
		exitWait: 5 * time.Second, termWait: 5 * time.Second, stderr: u.cfg.Stderr}
	go func() { defer close(c.waitDone); _ = cmd.Wait() }()
	go c.readLoop()
	go func() {
		defer close(c.ownerDone)
		select {
		case <-ctx.Done():
			_ = c.shutdown()
		case <-c.closed:
		}
	}()
	return c, nil
}

type readResult struct {
	msg jsonrpc.Message
	err error
}
type stdioConn struct {
	cmd                                   *exec.Cmd
	stdin                                 io.WriteCloser
	stdout                                *os.File
	limit                                 int
	incoming                              chan readResult
	closed, readDone, waitDone, ownerDone chan struct{}
	writeMu                               sync.Mutex
	closeOnce                             sync.Once
	closeErr                              error
	exitWait, termWait                    time.Duration
	stderr                                io.Writer
}

func (c *stdioConn) readLoop() {
	defer close(c.readDone)
	defer close(c.incoming)
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, min(4096, c.limit+2)), c.limit+2)
	for scanner.Scan() {
		// Drain output during graceful shutdown so a child finishing its last
		// response cannot block on a full stdout pipe after stdin reaches EOF.
		select {
		case <-c.closed:
			continue
		default:
		}
		raw := scanner.Bytes()
		var result readResult
		if len(raw) > c.limit {
			result.err = fmt.Errorf("stdio message exceeds %d bytes", c.limit)
		} else {
			var err error
			result.msg, err = jsonrpc.DecodeMessage(raw)
			if err != nil {
				// Some servers print banners or logs to stdout. Dropping the line
				// keeps the session alive; failing would restart it in a loop.
				if c.stderr != nil {
					fmt.Fprintf(c.stderr, "mcp-mesh: ignoring non-JSON-RPC stdout line (%v): %.200s\n", err, raw)
				}
				continue
			}
		}
		select {
		case c.incoming <- result:
		case <-c.closed:
			continue
		}
		if result.err != nil {
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case c.incoming <- readResult{err: fmt.Errorf("read stdio: %w", err)}:
		case <-c.closed:
		}
	}
}
func (c *stdioConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, io.EOF
	case r, ok := <-c.incoming:
		if !ok {
			return nil, io.EOF
		}
		return r.msg, r.err
	}
}
func (c *stdioConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return fmt.Errorf("encode stdio message: %w", err)
	}
	if len(raw) > c.limit {
		return fmt.Errorf("stdio message exceeds %d bytes", c.limit)
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	select {
	case <-c.closed:
		return mcp.ErrConnectionClosed
	default:
	}
	_, err = c.stdin.Write(append(raw, '\n'))
	if err != nil {
		return fmt.Errorf("write stdio: %w", err)
	}
	return nil
}
func (*stdioConn) SessionID() string { return "" }

// PID returns the child's process ID, including after the process has exited.
func (c *stdioConn) PID() int { return c.cmd.Process.Pid }

func (c *stdioConn) waitExit(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.waitDone:
		return true
	case <-timer.C:
		return false
	}
}
func (c *stdioConn) Close() error {
	err := c.shutdown()
	<-c.ownerDone
	return err
}
func (c *stdioConn) shutdown() error {
	c.closeOnce.Do(func() {
		close(c.closed)
		_ = c.stdin.Close()
		exited := c.waitExit(c.exitWait)
		if !exited || processGroupAlive(c.cmd) {
			c.closeErr = signalProcess(c.cmd, false)
			// Even if the leader exited, descendants still need time to handle TERM.
			deadline := time.NewTimer(c.termWait)
			ticker := time.NewTicker(10 * time.Millisecond)
			defer deadline.Stop()
			defer ticker.Stop()
		waitGroup:
			for {
				select {
				case <-c.waitDone:
					exited = true
				default:
				}
				if exited && !processGroupAlive(c.cmd) {
					break
				}
				select {
				case <-deadline.C:
					break waitGroup
				case <-ticker.C:
				}
			}
			c.closeErr = errors.Join(c.closeErr, signalProcess(c.cmd, true))
		}
		<-c.waitDone
		// Descendants may have inherited stdout even after the leader exited.
		_ = c.stdout.Close()
		<-c.readDone
	})
	return c.closeErr
}
