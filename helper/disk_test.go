package main

import (
	"path/filepath"
	"testing"
)

// O sistema renomeia o pendrive (fake0 -> fake7): ele é achado pela identidade.
func TestFindDiskRenamed(t *testing.T) {
	t.Setenv("UAB_FAKE_DISK", filepath.Join(t.TempDir(), "disk.img"))
	before, err := findDisk("fake0", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("UAB_FAKE_DISK_ID", "fake7")
	if _, err := findDisk("fake0", ""); err == nil {
		t.Fatal("sem a identidade, o nome antigo não deveria achar o pendrive")
	}
	d, err := findDisk("fake0", before.Key)
	if err != nil || d.ID != "fake7" {
		t.Fatalf("esperava fake7 pela identidade, veio %q (%v)", d.ID, err)
	}
	if _, err := findDisk("fake7", "m:outro|1|USB"); err == nil {
		t.Fatal("identidade diferente no mesmo nome não pode ser aceita")
	}
}
