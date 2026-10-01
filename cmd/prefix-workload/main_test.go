package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProbeNeverReseedsMissingPayload(t *testing.T) {
	dir := t.TempDir()
	if err := run(context.Background(), dir, "127.0.0.1:0", 0); err == nil {
		t.Fatal("accepted missing original data")
	}
	files, _ := os.ReadDir(dir)
	if len(files) != 0 {
		t.Fatal("created substitute data")
	}
}

func TestProbePersistsDistinctShutdownEvidence(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "payload.bin"), []byte("original acknowledged payload"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, dir, "127.0.0.1:0", 0) }()
	deadline := time.After(5 * time.Second)
	for {
		files, _ := filepath.Glob(filepath.Join(dir, "*-started.json"))
		if len(files) == 1 {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("probe did not start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 3 {
		t.Fatalf("expected start, notification, flush; got %d", len(files))
	}
	var original event
	for _, filename := range files {
		data, err := os.ReadFile(filename)
		if err != nil {
			t.Fatal(err)
		}
		var e event
		if err := json.Unmarshal(data, &e); err != nil {
			t.Fatal(err)
		}
		if original.RunID == "" {
			original = e
		}
		if e.RunID != original.RunID || e.SHA256 != original.SHA256 {
			t.Fatal("inconsistent process evidence")
		}
		if err := persist(dir, e); err == nil {
			t.Fatal("overwrote earlier evidence")
		}
	}
}
