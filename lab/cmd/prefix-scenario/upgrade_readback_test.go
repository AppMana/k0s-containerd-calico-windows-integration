package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AppMana/k0s-containerd-calico-windows-integration/internal/prefixcheck"
)

func TestUpgradeReadbackRequiresForwardVersionAndUnchangedWorkload(t *testing.T) {
	target := observed{Observation: prefixcheck.Observation{PodUID: "pod", SandboxID: "sandbox", ContainerID: "container", IPv6: "2001:db8::1", Ready: true, PayloadSHA256: strings.Repeat("a", 64)}, RunID: "process"}
	control := target
	control.PodUID, control.SandboxID = "control", ""
	before := upgradeBaseline{RuntimeVersion: "v2.2.1-post.3", Target: target, Control: control}
	if err := validateUpgradeReadback(before, "v2.3.5-appmana.post.1", target, control); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"", "garbage", "v2.2.1-post.3", "v2.1.0"} {
		if err := validateUpgradeReadback(before, candidate, target, control); err == nil {
			t.Fatalf("accepted non-upgrade %q", candidate)
		}
	}
	for _, mutate := range []func(*observed){
		func(o *observed) { o.PodUID = "replacement" },
		func(o *observed) { o.SandboxID = "replacement" },
		func(o *observed) { o.ContainerID = "replacement" },
		func(o *observed) { o.RunID = "replacement" },
		func(o *observed) { o.PayloadSHA256 = strings.Repeat("b", 64) },
		func(o *observed) { o.IPv6 = "2001:db8::2" },
		func(o *observed) { o.Ready = false },
	} {
		changed := target
		mutate(&changed)
		if err := validateUpgradeReadback(before, "v2.3.5-appmana.post.1", changed, control); err == nil {
			t.Fatalf("accepted changed target: %+v", changed)
		}
		changed = control
		mutate(&changed)
		if err := validateUpgradeReadback(before, "v2.3.5-appmana.post.1", target, changed); err == nil {
			t.Fatalf("accepted changed control: %+v", changed)
		}
	}
	if err := validateUpgradeReadback(upgradeBaseline{}, "v2.3.5-appmana.post.1", target, control); err == nil {
		t.Fatal("accepted absent original evidence")
	}
}

func TestUpgradeReadbackNeverSeedsMissingEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "absent")
	s := &scenario{namespace: "prefix-123", evidence: root, version: "v2.3.5-appmana.post.1"}
	if err := s.upgradeReadback(context.Background()); !os.IsNotExist(err) {
		t.Fatalf("expected absent evidence: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("verifier seeded missing evidence")
	}
	s.namespace = "../outside"
	if err := s.upgradeReadback(context.Background()); err == nil {
		t.Fatal("accepted unrelated namespace")
	}
}
