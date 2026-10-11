package hyperv

import (
	"encoding/binary"
	"testing"
)

func TestVHDFooter(t *testing.T) {
	const size = 3 << 30
	b := vhdFooter(size)
	if len(b) != 512 || string(b[:8]) != "conectix" {
		t.Fatalf("footer starts %q, len %d", b[:8], len(b))
	}
	if got := binary.BigEndian.Uint64(b[48:]); got != size {
		t.Fatalf("current size = %d", got)
	}
	if got := binary.BigEndian.Uint32(b[60:]); got != 2 {
		t.Fatalf("disk type = %d, want 2 (fixed)", got)
	}
	want := binary.BigEndian.Uint32(b[64:])
	c := append([]byte(nil), b...)
	c[64], c[65], c[66], c[67] = 0, 0, 0, 0
	var sum uint32
	for _, x := range c {
		sum += uint32(x)
	}
	if ^sum != want {
		t.Fatalf("checksum = %08x, want %08x", want, ^sum)
	}
}

// Values from the VHD specification's algorithm, worked by hand.
func TestCHS(t *testing.T) {
	tests := []struct {
		sectors uint64
		cyl     uint16
		heads   uint8
		spt     uint8
	}{
		{sectors: 3 << 30 / 512, cyl: 6241, heads: 16, spt: 63},
		{sectors: 65535 * 16 * 255 * 2, cyl: 65535, heads: 16, spt: 255},
		{sectors: 2048, cyl: 30, heads: 4, spt: 17},
	}
	for _, tt := range tests {
		c, h, s := chs(tt.sectors)
		if c != tt.cyl || h != tt.heads || s != tt.spt {
			t.Errorf("chs(%d) = %d/%d/%d, want %d/%d/%d", tt.sectors, c, h, s, tt.cyl, tt.heads, tt.spt)
		}
	}
}
