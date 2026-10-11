// Package hyperv drives Hyper-V on the Windows host from WSL, for the VM
// end-to-end tests. Every operation is a PowerShell script run through
// powershell.exe; results come back as JSON. The user must be in the
// Hyper-V Administrators group: nothing here asks for elevation.
package hyperv

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"unicode/utf16"
)

// PowerShell is the executable used to reach the Windows host.
var PowerShell = "powershell.exe"

// prelude makes every cmdlet failure terminate the script with a non-zero
// exit, so a failed step is never mistaken for success.
const prelude = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
trap { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }
`

// epilogue ends a script that got this far as a success: powershell.exe
// otherwise exits 1 whenever the last command failed, even one the script
// silenced on purpose (-ErrorAction SilentlyContinue). Real failures exit
// through the prelude's trap.
const epilogue = "\nexit 0\n"

// Run runs script and returns its standard output. The script goes in
// -EncodedCommand so no quoting survives the trip through the WSL interop
// layer and cmd-line parsing.
func Run(ctx context.Context, script string) (string, error) {
	cmd := exec.CommandContext(ctx, PowerShell, "-NoProfile", "-NonInteractive",
		"-ExecutionPolicy", "Bypass", "-EncodedCommand", encode(prelude+script+epilogue))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := strings.ReplaceAll(stdout.String(), "\r", "")
	if err != nil {
		msg := strings.TrimSpace(strings.ReplaceAll(stderr.String(), "\r", ""))
		if msg == "" {
			msg = strings.TrimSpace(out)
		}
		return out, fmt.Errorf("powershell: %w: %s", err, firstLines(msg, 6))
	}
	return out, nil
}

// RunJSON runs script, which must write one JSON document (for example via
// ConvertTo-Json -Compress), and decodes it into v.
func RunJSON(ctx context.Context, script string, v any) error {
	out, err := Run(ctx, script)
	if err != nil {
		return err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return fmt.Errorf("powershell: no output, want JSON")
	}
	if err := json.Unmarshal([]byte(out), v); err != nil {
		return fmt.Errorf("powershell: decode %q: %w", firstLines(out, 3), err)
	}
	return nil
}

// Quote returns s as a single-quoted PowerShell string literal.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// encode is what -EncodedCommand expects: base64 of UTF-16LE.
func encode(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, r := range u {
		binary.LittleEndian.PutUint16(b[2*i:], r)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = append(lines[:n], "…")
	}
	return strings.Join(lines, " | ")
}

// WindowsPath converts a WSL path (/mnt/d/…) to its Windows form (D:\…).
func WindowsPath(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, "wslpath", "-w", path).Output()
	if err != nil {
		return "", fmt.Errorf("wslpath -w %s: %w", path, err)
	}
	return strings.TrimSpace(string(out)), nil
}
