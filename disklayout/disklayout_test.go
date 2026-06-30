// Copyright (c) Tailscale Inc & contributors
// SPDX-License-Identifier: BSD-3-Clause

package disklayout

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestStartLBAs(t *testing.T) {
	const firstLBA = DefaultBootPartitionStartLBA
	tests := []struct {
		name string
		got  uint32
		want uint32
	}{
		{"BootStartLBA", BootStartLBA(firstLBA), 8192},
		{"RootAStartLBA", RootAStartLBA(firstLBA), 8192 + 100*mb/sectorSize},
		{"RootBStartLBA", RootBStartLBA(firstLBA), 8192 + 600*mb/sectorSize},
		{"PermStartLBA", PermStartLBA(firstLBA), 8192 + 1100*mb/sectorSize},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %d; want %d", tt.name, tt.got, tt.want)
		}
	}
}

func TestPermSize(t *testing.T) {
	// 8 GiB disk: perm should fill from PermStartLBA through the last
	// addressable sector inclusive (no clamping; see disklayout.go).
	devsize := uint64(8) * 1024 * 1024 * 1024
	sz := PermSize(DefaultBootPartitionStartLBA, devsize)
	if sz == 0 {
		t.Fatal("PermSize for 8 GiB disk is 0")
	}
	totalLBA := uint32(devsize / sectorSize)
	endLBA := PermStartLBA(DefaultBootPartitionStartLBA) + sz
	if endLBA != totalLBA {
		t.Errorf("perm end LBA = %d; want %d", endLBA, totalLBA)
	}
}

func TestBuildMBR(t *testing.T) {
	bootCode := bytes.Repeat([]byte{0xAB}, 446)
	devsize := uint64(4) * 1024 * 1024 * 1024 // 4 GiB
	mbr, err := BuildMBR(bootCode, DefaultBootPartitionStartLBA, devsize)
	if err != nil {
		t.Fatalf("BuildMBR: %v", err)
	}

	// First 446 bytes are the boot code.
	if !bytes.Equal(mbr[:446], bootCode) {
		t.Error("boot code not preserved in MBR[:446]")
	}
	// 0xAA55 boot signature at the end.
	if mbr[510] != 0x55 || mbr[511] != 0xAA {
		t.Errorf("boot signature = %02x %02x; want 55 AA", mbr[510], mbr[511])
	}

	// Partition 1 (boot): type FAT, start LBA 8192, size 100 MiB.
	checkEntry(t, mbr[446:462], 1, mbrActive, mbrPartitionFAT, 8192, 100*mb/sectorSize)
	// Partition 2 (root A).
	checkEntry(t, mbr[462:478], 2, mbrInactive, mbrPartitionLinux, 8192+100*mb/sectorSize, 500*mb/sectorSize)
	// Partition 3 (root B).
	checkEntry(t, mbr[478:494], 3, mbrInactive, mbrPartitionLinux, 8192+600*mb/sectorSize, 500*mb/sectorSize)
	// Partition 4 (perm): just check type and start.
	checkEntry(t, mbr[494:510], 4, mbrInactive, mbrPartitionLinux, 8192+1100*mb/sectorSize, 0)
}

func checkEntry(t *testing.T, entry []byte, num int, active, typ byte, startLBA, sizeLBA uint32) {
	t.Helper()
	if entry[0] != active {
		t.Errorf("part %d: active byte = %#x; want %#x", num, entry[0], active)
	}
	if entry[4] != typ {
		t.Errorf("part %d: type = %#x; want %#x", num, entry[4], typ)
	}
	if got := binary.LittleEndian.Uint32(entry[8:12]); got != startLBA {
		t.Errorf("part %d: start LBA = %d; want %d", num, got, startLBA)
	}
	if sizeLBA != 0 {
		if got := binary.LittleEndian.Uint32(entry[12:16]); got != sizeLBA {
			t.Errorf("part %d: size LBA = %d; want %d", num, got, sizeLBA)
		}
	}
}

func TestBuildMBRBootCodeTooLong(t *testing.T) {
	bootCode := bytes.Repeat([]byte{0}, 447)
	if _, err := BuildMBR(bootCode, DefaultBootPartitionStartLBA, 4*1024*1024*1024); err == nil {
		t.Fatal("expected error for boot code > 446 bytes")
	}
}

func TestWritePartitionTableTinyDisk(t *testing.T) {
	var buf bytes.Buffer
	if err := WritePartitionTable(&buf, DefaultBootPartitionStartLBA, 100*1024*1024); err == nil {
		t.Fatal("expected error for too-small disk")
	}
}
