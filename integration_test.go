package integration_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Gitlinks, not floating branches, define the component matrix. Local worktrees
// may replace submodule directories only when their commits match those pins.
func source(t *testing.T, name string) string {
	t.Helper()
	path := os.Getenv("INTEGRATION_" + strings.ToUpper(name) + "_SOURCE")
	if path == "" {
		path = filepath.Join("sources", name)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimSpace(command(t, ".", time.Minute, "git", "rev-parse", "HEAD:sources/"+name))
	got := strings.TrimSpace(command(t, abs, time.Minute, "git", "rev-parse", "HEAD"))
	if got != want {
		t.Fatalf("%s source is %s; pinned %s", name, got, want)
	}
	if dirty := command(t, abs, time.Minute, "git", "status", "--porcelain", "--untracked-files=no"); dirty != "" {
		t.Fatalf("%s tracked source has uncommitted changes", name)
	}
	return abs
}

func command(t *testing.T, dir string, timeout time.Duration, executable string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.WaitDelay = 2 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", executable, args, err, out)
	}
	return string(out)
}

func TestComponentCheckBoundaries(t *testing.T) {
	if os.Getenv("INTEGRATION_COMPONENT_TESTS") != "1" {
		t.Skip("set INTEGRATION_COMPONENT_TESTS=1 and initialize pinned submodules")
	}
	for _, tc := range []struct{ name, pkg, pattern string }{
		{"calico", "./cni-plugin/pkg/plugin", "TestCheck|TestWorkloadEndpointIPsInEnabledPools|TestPodIPNetworksForCheck|TestKubernetesPodIPsInEnabledPools"},
		{"containerd", "./internal/cri/server", "TestCheck|TestListPodSandbox.*Check|Test.*SandboxName|TestPodSandboxStatus"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Log(command(t, source(t, tc.name), 10*time.Minute, "go", "test", tc.pkg, "-run", tc.pattern, "-count=1", "-timeout=5m"))
		})
	}
}

func TestLiveWAN(t *testing.T) {
	if os.Getenv("INTEGRATION_LIVE_WAN") != "1" {
		t.Skip("set INTEGRATION_LIVE_WAN=1 and explicit LABCONTAINERS_* image/media inputs")
	}
	if os.Getenv("LABCONTAINERS_CALICO_MEDIA") == "" {
		t.Fatal("explicit offline media required; a skipped child test is not qualification")
	}
	out := command(t, filepath.Join(source(t, "calico"), "hack", "appmana", "lab"), 47*time.Minute, "go", "test", "-v", "-count=1", "-run", "^TestLiveK0sWindowsWAN$", "-timeout=45m", ".")
	if !strings.Contains(out, "--- PASS: TestLiveK0sWindowsWAN") {
		t.Fatal("WAN qualification did not run")
	}
	t.Log(out)
}
