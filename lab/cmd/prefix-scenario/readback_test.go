package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AppMana/k0s-containerd-calico-windows-integration/internal/prefixcheck"
)

func TestReadbackNeverSeedsMissingEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing")
	s := &scenario{namespace: "prefix-123", evidence: root}
	if err := s.readback(context.Background()); !os.IsNotExist(err) {
		t.Fatalf("expected missing original evidence, got %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("readback created missing dataset", err)
	}
	s.namespace = "../outside"
	if err := s.readback(context.Background()); err == nil {
		t.Fatal("accepted non-qualification namespace")
	}
}

func TestReadbackRequiresOriginalDataAndStableControl(t *testing.T) {
	before := observed{Observation: prefixcheck.Observation{PodUID: "pod", SandboxID: "old", ContainerID: "old", IPv6: "2001:db8::1", Ready: true, PayloadSHA256: strings.Repeat("a", 64)}, RunID: "old"}
	after := before
	after.SandboxID, after.ContainerID, after.RunID = "new", "new", "new"
	control := before
	if err := validateReadback(before, after, control, control); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*observed){
		func(o *observed) { o.PodUID = "replacement" },
		func(o *observed) { o.PayloadSHA256 = strings.Repeat("b", 64) },
		func(o *observed) { o.Ready = false },
		func(o *observed) { o.SandboxID = "" },
		func(o *observed) { o.IPv6 = "192.0.2.1" },
	} {
		bad := after
		mutate(&bad)
		if err := validateReadback(before, bad, control, control); err == nil {
			t.Fatalf("accepted lost data/identity: %+v", bad)
		}
	}
	changedControl := control
	changedControl.RunID = "restarted"
	if err := validateReadback(before, after, control, changedControl); err == nil {
		t.Fatal("accepted restarted control")
	}
	if err := validateReadback(observed{}, after, control, control); err == nil {
		t.Fatal("accepted missing dataset baseline")
	}
}
