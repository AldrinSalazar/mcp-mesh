package lab

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/e2e/hyperv"
)

// userData is the machine's first-boot setup. hyperv-daemons reports the
// guest's IP to Hyper-V; iproute2's tc (for network faults) ships with the
// image. Nothing of mcp-mesh is installed here: scenarios install it the
// way a user would.
const userData = `#cloud-config
hostname: {{name}}
manage_etc_hosts: true
users:
  - name: mesh
    groups: [sudo]
    sudo: "ALL=(ALL) NOPASSWD:ALL"
    shell: /bin/bash
    lock_passwd: true
    ssh_authorized_keys:
      - {{key}}
package_update: true
packages:
  - hyperv-daemons
  - jq
  - curl
  - ca-certificates
`

const metaData = `instance-id: {{name}}-1
local-hostname: {{name}}
`

// writeSeed writes the machine's cloud-init files and packs them into
// seed.iso in dir.
func (l *Lab) writeSeed(ctx context.Context, dir, name, pubKey string) (string, error) {
	src := filepath.Join(dir, "seed")
	if err := os.MkdirAll(src, 0o755); err != nil {
		return "", fmt.Errorf("seed dir: %w", err)
	}
	r := strings.NewReplacer("{{name}}", name, "{{key}}", pubKey)
	files := map[string]string{"user-data": r.Replace(userData), "meta-data": r.Replace(metaData)}
	for f, content := range files {
		if err := os.WriteFile(filepath.Join(src, f), []byte(content), 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", f, err)
		}
	}
	iso := filepath.Join(dir, "seed.iso")
	_ = os.Remove(iso)
	winSrc, err := hyperv.WindowsPath(ctx, src)
	if err != nil {
		return "", err
	}
	winISO, err := hyperv.WindowsPath(ctx, iso)
	if err != nil {
		return "", err
	}
	if err := hyperv.SeedISO(ctx, winSrc, winISO); err != nil {
		return "", err
	}
	return iso, nil
}

func (l *Lab) cutPower(ctx context.Context, vm string) error {
	return hyperv.Stop(ctx, vm, true)
}
