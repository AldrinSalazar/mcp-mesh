// Package lab manages the VMs the end-to-end tests run on: a base image,
// one VM per machine with a "clean" checkpoint taken after its first boot,
// and SSH access. Each scenario restores the checkpoints it needs, so every
// run starts from the same machines.
package lab

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/AldrinSalazar/mcp-mesh/e2e/hyperv"
)

// CleanCheckpoint is the checkpoint every scenario starts from: the machine
// right after cloud-init finished its first boot, powered off.
const CleanCheckpoint = "clean"

// VMPrefix starts the Hyper-V name of every lab VM.
const VMPrefix = "mm-e2e-"

// User is the account on every lab machine (passwordless sudo, SSH key).
const User = "mesh"

// Lab is a set of machines under one directory on the Windows host.
type Lab struct {
	// Dir holds images, SSH keys and the VMs, as a WSL path to a Windows
	// drive (Hyper-V can't keep disks in the WSL file system).
	Dir    string
	Switch string
	Logger *slog.Logger

	relay   *relay
	hostsMu sync.Mutex
	hosts   map[string]netip.Addr
}

// New returns a lab rooted at dir (for example /mnt/d/mcp-mesh-vms).
func New(dir string, logger *slog.Logger) *Lab {
	if logger == nil {
		logger = slog.Default()
	}
	return &Lab{Dir: dir, Switch: "Default Switch", Logger: logger}
}

func (l *Lab) logf(format string, args ...any) { l.Logger.Info(fmt.Sprintf(format, args...)) }

// MachineSpec sizes one machine.
type MachineSpec struct {
	Name     string // short name, also the hostname: hub, node-a…
	MemoryMB int
	CPUs     int
}

func (s MachineSpec) vmName() string { return VMPrefix + s.Name }

// Machine is a running lab machine.
type Machine struct {
	Name string
	VM   string
	IP   string
	lab  *Lab
}

// Ensure creates the machine if it has no clean checkpoint yet: first boot
// with its seed, wait for cloud-init, power off, checkpoint. It is a no-op
// for a machine that is already prepared.
func (l *Lab) Ensure(ctx context.Context, spec MachineSpec) error {
	vm := spec.vmName()
	info, exists, err := hyperv.Get(ctx, vm)
	if err != nil {
		return err
	}
	if exists {
		if info.Notes != hyperv.LabNote {
			return fmt.Errorf("a VM named %s exists and isn't a lab VM", vm)
		}
		ok, err := hyperv.HasCheckpoint(ctx, vm, CleanCheckpoint)
		if err != nil || ok {
			return err
		}
		l.logf("%s: exists without a clean checkpoint; recreating", spec.Name)
		if err := hyperv.Remove(ctx, vm); err != nil {
			return err
		}
	}
	if err := l.EnsureBase(ctx); err != nil {
		return err
	}
	pub, err := l.publicKey()
	if err != nil {
		return err
	}
	dir := filepath.Join(l.Dir, "vms", spec.Name)
	iso, err := l.writeSeed(ctx, dir, spec.Name, pub)
	if err != nil {
		return err
	}
	win := func(p string) (string, error) { return hyperv.WindowsPath(ctx, p) }
	winDir, err := win(dir)
	if err != nil {
		return err
	}
	winBase, err := win(filepath.Join(l.Dir, "images", baseVHDX))
	if err != nil {
		return err
	}
	winISO, err := win(iso)
	if err != nil {
		return err
	}
	l.logf("%s: creating VM %s", spec.Name, vm)
	if err := hyperv.Create(ctx, hyperv.Spec{
		Name: vm, Dir: winDir, Parent: winBase, SeedISO: winISO,
		MemoryMB: spec.MemoryMB, CPUs: spec.CPUs, Switch: l.Switch,
	}); err != nil {
		return err
	}
	m, err := l.boot(ctx, spec.Name)
	if err != nil {
		return err
	}
	l.logf("%s: waiting for cloud-init (installs packages on first boot)", spec.Name)
	out, err := m.Run(ctx, "cloud-init status --wait --long; true")
	if err != nil {
		return fmt.Errorf("%s: cloud-init: %w\n%s", spec.Name, err, out)
	}
	if !strings.Contains(out, "status: done") {
		return fmt.Errorf("%s: cloud-init did not finish cleanly:\n%s", spec.Name, out)
	}
	if err := l.powerOff(ctx, m); err != nil {
		return err
	}
	l.logf("%s: taking the clean checkpoint", spec.Name)
	return hyperv.Checkpoint(ctx, vm, CleanCheckpoint)
}

// Reset restores the machine's clean checkpoint and boots it.
func (l *Lab) Reset(ctx context.Context, name string) (*Machine, error) {
	if err := hyperv.Restore(ctx, VMPrefix+name, CleanCheckpoint); err != nil {
		return nil, err
	}
	return l.boot(ctx, name)
}

// Attach returns a machine that is already running, without resetting it.
func (l *Lab) Attach(ctx context.Context, name string) (*Machine, error) {
	return l.waitReachable(ctx, name, 30*time.Second)
}

// boot starts the VM and waits until SSH answers.
func (l *Lab) boot(ctx context.Context, name string) (*Machine, error) {
	if err := hyperv.Start(ctx, VMPrefix+name); err != nil {
		return nil, err
	}
	return l.waitReachable(ctx, name, 10*time.Minute)
}

func (l *Lab) waitReachable(ctx context.Context, name string, timeout time.Duration) (*Machine, error) {
	vm := VMPrefix + name
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		ip, err := l.ipOf(ctx, vm)
		if err == nil && ip != "" {
			m := &Machine{Name: name, VM: vm, IP: ip, lab: l}
			if _, err = m.Run(ctx, "true"); err == nil {
				return m, nil
			}
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return nil, fmt.Errorf("%s: not reachable over SSH after %s (last: %v)", name, timeout, last)
}

// ipOf asks Hyper-V for the guest's IPv4 address (reported by the guest's
// KVP daemon). Before that daemon runs, the host's neighbor table still
// knows the address the VM's MAC used for DHCP.
func (l *Lab) ipOf(ctx context.Context, vm string) (string, error) {
	info, ok, err := hyperv.Get(ctx, vm)
	if err != nil || !ok {
		return "", err
	}
	if info.State != "Running" {
		return "", fmt.Errorf("%s is %s", vm, strings.ToLower(info.State))
	}
	for _, ip := range info.IPs {
		if p := net.ParseIP(ip); p != nil && p.To4() != nil {
			return ip, nil
		}
	}
	if len(info.MAC) != 12 {
		return "", errors.New("no MAC yet")
	}
	mac := strings.ToUpper(info.MAC)
	dashed := strings.Join([]string{mac[0:2], mac[2:4], mac[4:6], mac[6:8], mac[8:10], mac[10:12]}, "-")
	out, err := hyperv.Run(ctx, fmt.Sprintf(`Get-NetNeighbor -AddressFamily IPv4 -LinkLayerAddress %s -ErrorAction SilentlyContinue |
  Where-Object { $_.State -ne 'Unreachable' } | Select-Object -First 1 -ExpandProperty IPAddress`, hyperv.Quote(dashed)))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// powerOff shuts the guest down cleanly and waits until Hyper-V sees it off.
func (l *Lab) powerOff(ctx context.Context, m *Machine) error {
	_, _ = m.Run(ctx, "sudo systemctl poweroff")
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		info, _, err := hyperv.Get(ctx, m.VM)
		if err != nil {
			return err
		}
		if info.State == "Off" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return hyperv.Stop(ctx, m.VM, true)
}

// Down powers off every lab VM, keeping them and their checkpoints.
func (l *Lab) Down(ctx context.Context, names ...string) error {
	var errs []error
	for _, n := range names {
		errs = append(errs, hyperv.Stop(ctx, VMPrefix+n, true))
	}
	return errors.Join(errs...)
}

// Destroy removes lab VMs and their disks (the base image stays).
func (l *Lab) Destroy(ctx context.Context, names ...string) error {
	var errs []error
	for _, n := range names {
		errs = append(errs, hyperv.Remove(ctx, VMPrefix+n))
	}
	return errors.Join(errs...)
}

// keyPath is the lab's SSH key, made on first use. The lab VMs are throwaway
// and only reachable from this host, so one unprotected key is fine.
func (l *Lab) keyPath() string { return filepath.Join(l.Dir, "ssh", "id_ed25519") }

func (l *Lab) signer() (ssh.Signer, error) {
	path := l.keyPath()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("ssh dir: %w", err)
		}
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		block, err := ssh.MarshalPrivateKey(priv, "mcp-mesh e2e lab")
		if err != nil {
			return nil, err
		}
		b = pem.EncodeToMemory(block)
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, fmt.Errorf("write ssh key: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("read ssh key: %w", err)
	}
	s, err := ssh.ParsePrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parse ssh key: %w", err)
	}
	return s, nil
}

func (l *Lab) publicKey() (string, error) {
	s, err := l.signer()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(s.PublicKey()))), nil
}

// KeyPath is the private key for logging in by hand: ssh -i <KeyPath> mesh@<ip>.
func (l *Lab) KeyPath() string { return l.keyPath() }
