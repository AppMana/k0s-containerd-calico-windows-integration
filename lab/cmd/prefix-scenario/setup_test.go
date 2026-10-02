package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestLinuxControlSeedIsExclusive(t *testing.T) {
	dir := t.TempDir()
	probe := filepath.Join(dir, "probe")
	if err := os.WriteFile(probe, []byte("probe-bytes"), 0755); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "scenario")
	if err := seedLinuxControl(root, probe); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(root, "control", "payload.bin")
	before, err := os.ReadFile(payload)
	if err != nil || len(before) != 32768 {
		t.Fatalf("original payload: len=%d err=%v", len(before), err)
	}
	if err := seedLinuxControl(root, probe); err == nil {
		t.Fatal("existing scenario must not be reseeded")
	}
	after, err := os.ReadFile(payload)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("retry changed original data", err)
	}
	if err := os.Remove(payload); err != nil {
		t.Fatal(err)
	}
	if err := seedLinuxControl(root, probe); err == nil {
		t.Fatal("lost payload must not be silently repaired")
	}
	if _, err := os.Stat(payload); !os.IsNotExist(err) {
		t.Fatal("lost payload was recreated", err)
	}
}
