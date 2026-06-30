// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package packer

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"

	"github.com/bradfitz/monogok/disklayout"
)

// TestDisklayoutMatchesMBRPartitionTable guards against the public
// github.com/bradfitz/monogok/disklayout package drifting from the
// canonical writeMBRPartitionTable / permSize implementations in this
// internal package. External tools (e.g. tailscale's
// `tailscale configure flash-appliance`) use the disklayout package to
// build the same partition table monogok writes when it produces a full
// disk image; if these drift, fresh-flashed disks would be incompatible
// with OTA updates against monogok-built images.
func TestDisklayoutMatchesMBRPartitionTable(t *testing.T) {
	const firstLBA = disklayout.DefaultBootPartitionStartLBA
	// 8 GiB disk; large enough to exercise PermSize's clamping path.
	const devsize uint64 = 8 * 1024 * 1024 * 1024

	var want bytes.Buffer
	if err := writeMBRPartitionTable(firstLBA, &want, devsize); err != nil {
		t.Fatalf("writeMBRPartitionTable: %v", err)
	}
	if want.Len() != 512 {
		t.Fatalf("writeMBRPartitionTable wrote %d bytes; want 512", want.Len())
	}

	got, err := disklayout.BuildMBR(nil, firstLBA, devsize)
	if err != nil {
		t.Fatalf("disklayout.BuildMBR: %v", err)
	}

	if !bytes.Equal(got[:], want.Bytes()) {
		t.Errorf("disklayout.BuildMBR output diverged from writeMBRPartitionTable\n got: %x\nwant: %x", got[:], want.Bytes())
	}
}

// Note: there is intentionally no test asserting
// disklayout.PermSize == packer.permSize. The standalone packer.permSize
// helper subtracts 33 sectors to reserve space for a secondary GPT
// header and is only used on the GPT path (writeGPT, PermSizeInKB);
// writeMBRPartitionTable writes the unclamped "fill the rest of the
// disk" value. disklayout is the MBR-side helper and matches
// writeMBRPartitionTable, so it diverges from packer.permSize by
// design. The byte-level TestDisklayoutPartitionEntriesMatch below
// guards what callers actually depend on.

// TestDisklayoutGPTMatchesPackerWriteGPT is the drift guard for the GPT
// path: it asserts disklayout.WriteGPT produces byte-identical output to
// PackerPack.Partition (which calls writePartitionTable + writeGPT) for
// the same inputs. Tailscale's flash-appliance CLI relies on this
// equivalence so freshly flashed disks accept OTA updates against
// monogok-built full images.
func TestDisklayoutGPTMatchesPackerWriteGPT(t *testing.T) {
	const (
		firstLBA          = disklayout.DefaultBootPartitionStartLBA
		devsize    uint64 = 8 * 1024 * 1024 * 1024 // 8 GiB
		partUUID   uint32 = 0xdd02023b             // FNV-1a("tsapp")
	)

	t.Setenv("GOARCH", "arm64")
	pack := NewPackForHost(firstLBA, disklayout.GokrazyHostname)
	if pack.Partuuid != partUUID {
		t.Fatalf("NewPackForHost partuuid = %#x; want %#x", pack.Partuuid, partUUID)
	}

	wantFile, err := os.CreateTemp("", "packer-want-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(wantFile.Name())
	defer wantFile.Close()
	if err := wantFile.Truncate(int64(devsize)); err != nil {
		t.Fatal(err)
	}
	if err := pack.Partition(wantFile, devsize); err != nil {
		t.Fatalf("Partition: %v", err)
	}

	gotFile, err := os.CreateTemp("", "disklayout-got-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(gotFile.Name())
	defer gotFile.Close()
	if err := gotFile.Truncate(int64(devsize)); err != nil {
		t.Fatal(err)
	}
	if err := disklayout.WriteGPT(gotFile, devsize, firstLBA, nil, partUUID, disklayout.ArchARM64); err != nil {
		t.Fatalf("disklayout.WriteGPT: %v", err)
	}

	for _, region := range []struct {
		name string
		off  int64
		size int64
	}{
		{"protective MBR (LBA 0)", 0, 512},
		{"primary GPT (LBA 1..33)", 512, 33 * 512},
		{"secondary GPT entries (last 33 LBAs)", int64(devsize) - 33*512, 33 * 512},
	} {
		got := readAt(t, gotFile, region.off, region.size)
		want := readAt(t, wantFile, region.off, region.size)
		if !bytes.Equal(got, want) {
			t.Errorf("region %s diverged at offset %d (size %d)", region.name, region.off, region.size)
		}
	}
}

func readAt(t *testing.T, f *os.File, off, size int64) []byte {
	t.Helper()
	buf := make([]byte, size)
	if _, err := f.ReadAt(buf, off); err != nil {
		t.Fatalf("ReadAt(%d, %d): %v", off, size, err)
	}
	return buf
}

// TestDisklayoutPartitionEntriesMatch checks the partition entry bytes
// (offsets 446..510 of the MBR) match between the two implementations
// for several disk sizes, including ones small enough that permSize
// trims against the GPT-reserved tail.
func TestDisklayoutPartitionEntriesMatch(t *testing.T) {
	const firstLBA = disklayout.DefaultBootPartitionStartLBA
	for _, gib := range []uint64{2, 4, 8, 32, 128} {
		devsize := gib * 1024 * 1024 * 1024

		var want bytes.Buffer
		if err := writeMBRPartitionTable(firstLBA, &want, devsize); err != nil {
			t.Fatalf("writeMBRPartitionTable(%d GiB): %v", gib, err)
		}
		got, err := disklayout.BuildMBR(nil, firstLBA, devsize)
		if err != nil {
			t.Fatalf("disklayout.BuildMBR(%d GiB): %v", gib, err)
		}
		if !bytes.Equal(got[446:510], want.Bytes()[446:510]) {
			t.Errorf("%d GiB: partition table bytes diverged\n got: %x\nwant: %x", gib, got[446:510], want.Bytes()[446:510])
		}
		// Sanity: 0xAA55 boot signature at the end of both.
		if binary.LittleEndian.Uint16(got[510:512]) != 0xAA55 {
			t.Errorf("%d GiB: disklayout signature not 0xAA55", gib)
		}
	}
}
