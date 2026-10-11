package hyperv

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"time"
)

// RawToVHD turns a raw disk image into a fixed VHD in place by appending the
// 512-byte VHD footer, then renames it to vhdPath. A fixed VHD is exactly the
// raw disk plus that footer, so this needs no conversion tool; Hyper-V's
// Convert-VHD then makes a dynamic VHDX from it.
func RawToVHD(rawPath, vhdPath string) error {
	f, err := os.OpenFile(rawPath, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open raw image: %w", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat raw image: %w", err)
	}
	size := st.Size()
	if size%512 != 0 {
		f.Close()
		return fmt.Errorf("raw image size %d is not a multiple of 512", size)
	}
	if _, err := f.WriteAt(vhdFooter(uint64(size)), size); err != nil {
		f.Close()
		return fmt.Errorf("write VHD footer: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close VHD: %w", err)
	}
	if err := os.Rename(rawPath, vhdPath); err != nil {
		return fmt.Errorf("rename to VHD: %w", err)
	}
	return nil
}

// vhdFooter builds a fixed-disk footer as the VHD specification lays it
// out: big-endian fields, CHS geometry, and a one's-complement checksum.
func vhdFooter(size uint64) []byte {
	b := make([]byte, 512)
	copy(b[0:], "conectix")
	binary.BigEndian.PutUint32(b[8:], 2)           // features: reserved bit
	binary.BigEndian.PutUint32(b[12:], 0x00010000) // format version 1.0
	binary.BigEndian.PutUint64(b[16:], ^uint64(0)) // fixed disks have no data offset
	vhdEpoch := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	binary.BigEndian.PutUint32(b[24:], uint32(time.Since(vhdEpoch)/time.Second))
	copy(b[28:], "mmsh")
	binary.BigEndian.PutUint32(b[32:], 0x00010000)
	copy(b[36:], "Wi2k")
	binary.BigEndian.PutUint64(b[40:], size)
	binary.BigEndian.PutUint64(b[48:], size)
	cyl, heads, spt := chs(size / 512)
	binary.BigEndian.PutUint16(b[56:], cyl)
	b[58], b[59] = heads, spt
	binary.BigEndian.PutUint32(b[60:], 2) // fixed disk
	_, _ = rand.Read(b[68:84])            // unique id
	var sum uint32
	for _, c := range b {
		sum += uint32(c)
	}
	binary.BigEndian.PutUint32(b[64:], ^sum)
	return b
}

// chs is the VHD specification's geometry algorithm.
func chs(sectors uint64) (cyl uint16, heads, spt uint8) {
	if sectors > 65535*16*255 {
		sectors = 65535 * 16 * 255
	}
	var h, s, cth uint64
	if sectors >= 65535*16*63 {
		s, h = 255, 16
		cth = sectors / s
	} else {
		s = 17
		cth = sectors / s
		h = (cth + 1023) / 1024
		if h < 4 {
			h = 4
		}
		if cth >= h*1024 || h > 16 {
			s, h = 31, 16
			cth = sectors / s
		}
		if cth >= h*1024 {
			s, h = 63, 16
			cth = sectors / s
		}
	}
	return uint16(cth / h), uint8(h), uint8(s)
}

// ConvertToDynamicVHDX converts a VHD to a dynamic VHDX of sizeBytes (the
// guest grows its root partition at first boot). Paths are Windows paths.
func ConvertToDynamicVHDX(ctx context.Context, vhd, vhdx string, sizeBytes int64) error {
	_, err := Run(ctx, fmt.Sprintf(`Convert-VHD -Path %s -DestinationPath %s -VHDType Dynamic
Resize-VHD -Path %s -SizeBytes %d`, Quote(vhd), Quote(vhdx), Quote(vhdx), sizeBytes))
	if err != nil {
		return fmt.Errorf("convert %s to VHDX: %w", vhd, err)
	}
	return nil
}
