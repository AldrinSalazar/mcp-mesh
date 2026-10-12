package main

import (
	"strings"
	"testing"
)

func TestWrite(t *testing.T) {
	var sums strings.Builder
	for i, target := range append(targets, "linux_armv7") {
		sums.WriteString(strings.Repeat(string(rune('a'+i)), 64) + "  mcp-mesh-node_" + target + ".tar.gz\n")
	}
	sums.WriteString(strings.Repeat("f", 64) + " *mcp-mesh-node_windows_amd64.zip\n")
	var out strings.Builder
	if err := write(&out, "v1.2.3", "Owner/mcp-mesh", strings.NewReader(sums.String())); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{
		`url "https://github.com/Owner/mcp-mesh/releases/download/v1.2.3/mcp-mesh-node_darwin_arm64.tar.gz"` + "\n      sha256 \"" + strings.Repeat("a", 64) + `"`,
		`url "https://github.com/Owner/mcp-mesh/releases/download/v1.2.3/mcp-mesh-node_linux_amd64.tar.gz"` + "\n      sha256 \"" + strings.Repeat("d", 64) + `"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("formula lacks %s:\n%s", want, got)
		}
	}
	if strings.Contains(got, "armv7") || strings.Contains(got, "<no value>") {
		t.Errorf("unexpected content:\n%s", got)
	}

	for _, tc := range []struct{ tag, sums string }{
		{"v1.2.3-rc.1", sums.String()},                      // pre-releases stay out of the tap
		{"v1.2.3", strings.Repeat("a", 64) + "  other.zip"}, // archives missing
	} {
		if err := write(&out, tc.tag, "Owner/mcp-mesh", strings.NewReader(tc.sums)); err == nil {
			t.Errorf("%s: no error", tc.tag)
		}
	}
}
