package lab

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/AldrinSalazar/mcp-mesh/e2e/hyperv"
)

// The base image: Debian 12's generic cloud image as a raw disk. It has
// systemd (for the node's user service), cloud-init and the Hyper-V drivers,
// and a raw disk becomes a VHD by appending a footer, so no conversion tool
// is needed.
const (
	imageBase = "https://cloud.debian.org/images/cloud/bookworm/latest/"
	imageRaw  = "debian-12-genericcloud-amd64.raw"
	baseVHDX  = "base-debian12.vhdx"
	diskSize  = 10 << 30
)

// EnsureBase makes images/base-debian12.vhdx, downloading the image first if
// it isn't there. It does nothing once the VHDX exists.
func (l *Lab) EnsureBase(ctx context.Context) error {
	dir := filepath.Join(l.Dir, "images")
	vhdx := filepath.Join(dir, baseVHDX)
	if _, err := os.Stat(vhdx); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("images dir: %w", err)
	}
	raw := filepath.Join(dir, imageRaw)
	if err := l.verify(ctx, raw); err != nil {
		l.logf("base image: downloading %s (about 3 GB)", imageRaw)
		if err := download(ctx, imageBase+imageRaw, raw); err != nil {
			return err
		}
		if err := l.verify(ctx, raw); err != nil {
			return err
		}
	}
	l.logf("base image: converting to VHDX")
	vhd := strings.TrimSuffix(raw, ".raw") + ".vhd"
	if err := hyperv.RawToVHD(raw, vhd); err != nil {
		return err
	}
	winVHD, err := hyperv.WindowsPath(ctx, vhd)
	if err != nil {
		return err
	}
	winVHDX, err := hyperv.WindowsPath(ctx, vhdx)
	if err != nil {
		return err
	}
	if err := hyperv.ConvertToDynamicVHDX(ctx, winVHD, winVHDX, diskSize); err != nil {
		return err
	}
	return os.Remove(vhd)
}

// verify checks the raw image against Debian's published SHA-512.
func (l *Lab) verify(ctx context.Context, raw string) error {
	f, err := os.Open(raw)
	if err != nil {
		return fmt.Errorf("open %s: %w", raw, err)
	}
	defer f.Close()
	want, err := publishedSum(ctx, imageRaw)
	if err != nil {
		return err
	}
	l.logf("base image: verifying %s", imageRaw)
	h := sha512.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash %s: %w", raw, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return fmt.Errorf("%s: SHA-512 mismatch", raw)
	}
	return nil
}

func publishedSum(ctx context.Context, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, imageBase+"SHA512SUMS", nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch SHA512SUMS: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("read SHA512SUMS: %w", err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == name {
			return f[0], nil
		}
	}
	return "", fmt.Errorf("SHA512SUMS has no entry for %s", name)
}

func download(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create %s: %w", tmp, err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("download %s: %w", url, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}
