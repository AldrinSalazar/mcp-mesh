package lab

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/AldrinSalazar/mcp-mesh/e2e/hyperv"
)

// dial opens an SSH connection to the machine. Lab VMs get new host keys
// whenever they are recreated, and nothing but this host can reach them, so
// host keys aren't checked.
func (m *Machine) dial(ctx context.Context) (*ssh.Client, error) {
	signer, err := m.lab.signer()
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User:            User,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := m.lab.Dial(dctx, "tcp", net.JoinHostPort(m.IP, "22"))
	if err != nil {
		return nil, fmt.Errorf("%s: dial ssh: %w", m.Name, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, m.IP, cfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%s: ssh handshake: %w", m.Name, err)
	}
	return ssh.NewClient(c, chans, reqs), nil
}

// Run runs a shell command as the lab user and returns its combined output.
// The command ends when ctx does (the connection is closed).
func (m *Machine) Run(ctx context.Context, cmd string) (string, error) {
	return m.RunInput(ctx, cmd, nil)
}

// RunInput is Run with stdin.
func (m *Machine) RunInput(ctx context.Context, cmd string, stdin io.Reader) (string, error) {
	c, err := m.dial(ctx)
	if err != nil {
		return "", err
	}
	defer c.Close()
	s, err := c.NewSession()
	if err != nil {
		return "", fmt.Errorf("%s: ssh session: %w", m.Name, err)
	}
	defer s.Close()
	// The session copies stdout and stderr in separate goroutines.
	var out lockedBuffer
	s.Stdout, s.Stderr, s.Stdin = &out, &out, stdin
	done := make(chan error, 1)
	go func() { done <- s.Run(cmd) }()
	select {
	case err = <-done:
	case <-ctx.Done():
		c.Close()
		<-done
		err = ctx.Err()
	}
	if err != nil {
		return out.String(), fmt.Errorf("%s: %s: %w", m.Name, cmd, err)
	}
	return out.String(), nil
}

// Upload copies a local file to dst on the machine with the given mode.
func (m *Machine) Upload(ctx context.Context, src, dst string, mode os.FileMode) error {
	f, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer f.Close()
	return m.Write(ctx, f, dst, mode)
}

// Write stores r's content at dst on the machine with the given mode.
func (m *Machine) Write(ctx context.Context, r io.Reader, dst string, mode os.FileMode) error {
	tmp := dst + ".part"
	cmd := fmt.Sprintf("mkdir -p %s && cat > %s && chmod %o %s && mv -f %s %s",
		shq(path.Dir(dst)), shq(tmp), mode.Perm(), shq(tmp), shq(tmp), shq(dst))
	if out, err := m.RunInput(ctx, cmd, r); err != nil {
		return fmt.Errorf("upload to %s: %w\n%s", dst, err, out)
	}
	return nil
}

// Read returns the content of a file on the machine.
func (m *Machine) Read(ctx context.Context, p string) (string, error) {
	return m.Run(ctx, "cat "+shq(p))
}

// PowerOff cuts the machine's power, like a crash or a pulled plug.
func (m *Machine) PowerOff(ctx context.Context) error {
	return m.lab.cutPower(ctx, m.VM)
}

// Shutdown powers the machine off cleanly.
func (m *Machine) Shutdown(ctx context.Context) error {
	return m.lab.powerOff(ctx, m)
}

// Boot starts a stopped machine again and waits for SSH (its IP may change).
func (m *Machine) Boot(ctx context.Context) error {
	n, err := m.lab.boot(ctx, m.Name)
	if err != nil {
		return err
	}
	m.IP = n.IP
	return nil
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// BootWithoutLogin starts a stopped machine and waits only until Hyper-V
// reports its address, without an SSH login. Logging in starts the user's
// systemd instance, which would hide whether a user service starts at boot
// on its own.
func (m *Machine) BootWithoutLogin(ctx context.Context, timeout time.Duration) error {
	if err := hyperv.Start(ctx, m.VM); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		info, _, err := hyperv.Get(ctx, m.VM)
		if err != nil {
			return err
		}
		for _, ip := range info.IPs {
			if p := net.ParseIP(ip); p != nil && p.To4() != nil {
				m.IP = ip
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("%s: no address after %s", m.Name, timeout)
}

// shq quotes s for a POSIX shell.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// Shq quotes s for a POSIX shell, for scenario commands.
func Shq(s string) string { return shq(s) }
