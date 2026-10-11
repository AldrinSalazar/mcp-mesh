package lab

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/AldrinSalazar/mcp-mesh/e2e/hyperv"
)

// relay is winrelay.exe running on the Windows host: WSL (mirrored
// networking) can't route to Hyper-V's internal switches, so every
// connection to a VM goes through it. See e2e/winrelay.
type relay struct {
	addr  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

var relayMu sync.Mutex

// Start builds and starts the relay. Call Close when done.
func (l *Lab) Start(ctx context.Context) error {
	relayMu.Lock()
	defer relayMu.Unlock()
	if l.relay != nil {
		return nil
	}
	subnet, err := l.switchSubnet(ctx)
	if err != nil {
		return err
	}
	exe := filepath.Join(l.Dir, "bin", "winrelay.exe")
	build := exec.CommandContext(ctx, goTool(), "build", "-o", exe, "./e2e/winrelay")
	build.Dir = moduleRoot()
	build.Env = append(os.Environ(), "GOOS=windows", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		return fmt.Errorf("build winrelay: %w\n%s", err, out)
	}
	// Not CommandContext: the relay lives as long as the lab, not this call.
	cmd := exec.Command(exe, "-listen", "127.0.0.1:0", "-allow", subnet)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start winrelay: %w", err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return fmt.Errorf("winrelay: no address: %w", err)
	}
	l.relay = &relay{addr: strings.TrimSpace(line), cmd: cmd, stdin: stdin}
	l.logf("relay to %s listening on %s", subnet, l.relay.addr)
	return nil
}

// Close stops the relay: closing its stdin makes it exit.
func (l *Lab) Close() error {
	relayMu.Lock()
	defer relayMu.Unlock()
	if l.relay == nil {
		return nil
	}
	_ = l.relay.stdin.Close()
	done := make(chan error, 1)
	go func() { done <- l.relay.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = l.relay.cmd.Process.Kill()
		<-done
	}
	l.relay = nil
	return nil
}

// switchSubnet is the IPv4 subnet of the host's adapter on the lab switch.
func (l *Lab) switchSubnet(ctx context.Context) (string, error) {
	out, err := hyperv.Run(ctx, fmt.Sprintf(`$a = Get-NetIPAddress -InterfaceAlias %s -AddressFamily IPv4 | Select-Object -First 1
"$($a.IPAddress)/$($a.PrefixLength)"`, hyperv.Quote("vEthernet ("+l.Switch+")")))
	if err != nil {
		return "", fmt.Errorf("lab switch subnet: %w", err)
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(out))
	if err != nil {
		return "", fmt.Errorf("lab switch subnet %q: %w", out, err)
	}
	return p.Masked().String(), nil
}

// Dial connects to addr (an IP:port on the lab switch) through the relay.
func (l *Lab) Dial(ctx context.Context, network, addr string) (net.Conn, error) {
	relayMu.Lock()
	r := l.relay
	relayMu.Unlock()
	if r == nil {
		return nil, errors.New("lab relay not started (Lab.Start)")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		l.hostsMu.Lock()
		mapped, ok := l.hosts[host]
		l.hostsMu.Unlock()
		if !ok {
			return nil, fmt.Errorf("lab dial %s: unknown host (Lab.AddHost)", addr)
		}
		ip = mapped
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	c, err := d.DialContext(ctx, "tcp", r.addr)
	if err != nil {
		return nil, fmt.Errorf("relay: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	req := []byte{5, 1, 0, 5, 1, 0, 1}
	if ip.Is4() {
		b := ip.As4()
		req = append(req, b[:]...)
	} else {
		req[6] = 4
		b := ip.As16()
		req = append(req, b[:]...)
	}
	req = binary.BigEndian.AppendUint16(req, uint16(port))
	if _, err := c.Write(req); err != nil {
		c.Close()
		return nil, fmt.Errorf("relay: %w", err)
	}
	var resp [12]byte // greeting reply (2) + CONNECT reply (10)
	if _, err := io.ReadFull(c, resp[:]); err != nil {
		c.Close()
		return nil, fmt.Errorf("relay: %w", err)
	}
	if resp[3] != 0 {
		c.Close()
		return nil, fmt.Errorf("relay: connect %s refused (code %d)", addr, resp[3])
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

// RelayAddr is the relay's SOCKS5 address on Windows' loopback (shared with
// WSL), for programs that take a proxy: a browser reaches the lab through it.
func (l *Lab) RelayAddr() string {
	relayMu.Lock()
	defer relayMu.Unlock()
	if l.relay == nil {
		return ""
	}
	return l.relay.addr
}

// AddHost makes name resolve to ip for Dial and HTTPClient, like a DNS
// record for a hub that is addressed by name.
func (l *Lab) AddHost(name, ip string) error {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return err
	}
	l.hostsMu.Lock()
	defer l.hostsMu.Unlock()
	if l.hosts == nil {
		l.hosts = map[string]netip.Addr{}
	}
	l.hosts[name] = a
	return nil
}

// HTTPClient returns a client whose connections go through the relay, with
// tlsConfig for HTTPS (nil for the defaults).
func (l *Lab) HTTPClient(tlsConfig *tls.Config) *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			DialContext:     l.Dial,
			TLSClientConfig: tlsConfig,
			MaxIdleConns:    16,
			IdleConnTimeout: 30 * time.Second,
		},
		Timeout: 2 * time.Minute,
	}
}

// goTool is the go command: from PATH, else the toolchain that built this
// binary (labctl run directly, outside go run).
func goTool() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(runtime.GOROOT(), "bin", "go")
}

// moduleRoot finds the repository root (where go.mod is) from the working
// directory, which is a package directory under go test.
func moduleRoot() string {
	dir, _ := os.Getwd()
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "go.mod")); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return dir
		}
	}
}
