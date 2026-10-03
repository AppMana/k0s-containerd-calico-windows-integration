package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestWaitDiagnosticsPreserveCancellationAndLastError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out bytes.Buffer
	err := waitWithDiagnostics(ctx, time.Minute, func(call context.Context) error {
		deadline, ok := call.Deadline()
		if !ok || time.Until(deadline) > 15*time.Second {
			t.Fatal("per-call deadline changed")
		}
		cancel()
		return errors.New("original observation failure")
	}, &out)
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "original observation failure") {
		t.Fatalf("failure changed: %v", err)
	}
	var entry struct {
		Attempt       int
		Error         string
		Time          string
		ElapsedMillis int64
	}
	if err := json.Unmarshal(out.Bytes(), &entry); err != nil || entry.Attempt != 1 || entry.Error != "original observation failure" || entry.ElapsedMillis < 0 {
		t.Fatalf("lost diagnostic: %s %v", out.String(), err)
	}
	if _, err := time.Parse(time.RFC3339Nano, entry.Time); err != nil {
		t.Fatal(err)
	}
}

func TestWaitDiagnosticsRetainTransientFailureThenSuccess(t *testing.T) {
	var out bytes.Buffer
	attempts := 0
	err := waitWithDiagnostics(context.Background(), 5*time.Second, func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("transient observation")
		}
		return nil
	}, &out)
	if err != nil || attempts != 2 {
		t.Fatal(attempts, err)
	}
	decoder := json.NewDecoder(&out)
	for i, want := range []string{"transient observation", ""} {
		var entry struct {
			Attempt int
			Error   string
		}
		if err := decoder.Decode(&entry); err != nil || entry.Attempt != i+1 || entry.Error != want {
			t.Fatalf("observation %d: %+v %v", i, entry, err)
		}
	}
}
