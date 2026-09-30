package integration_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestK0sSourceBundleReconstructsExactCommit(t *testing.T) {
	baseSource := os.Getenv("INTEGRATION_K0S_BASE_SOURCE")
	if baseSource == "" {
		t.Skip("set INTEGRATION_K0S_BASE_SOURCE to initialized upstream k0s source")
	}
	root, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	command(t, target, time.Minute, "git", "init")
	command(t, target, time.Minute, "git", "fetch", "--depth=1", baseSource, "bdf1c22c23a5af23ec6a1ff764179f6a6cf6f7dc")
	command(t, target, time.Minute, "git", "checkout", "--detach", "FETCH_HEAD")
	for i := 0; i < 2; i++ {
		out := command(t, root, time.Minute, "bash", "tools/materialize-k0s.sh", target)
		if !strings.Contains(out, "K0S_SOURCE_READY:13893f0ab766ab03eafecaa4807ce6bf3bc59668") {
			t.Fatal(out)
		}
	}
	if got := strings.TrimSpace(command(t, target, time.Minute, "git", "rev-parse", "HEAD^{tree}")); got != "3fbf5bcdf7276d21f218fb2e1e77fac5bee6b245" {
		t.Fatal(got)
	}
	if err := os.WriteFile(filepath.Join(target, "user-work.txt"), []byte("preserve me"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("bash", filepath.Join(root, "tools/materialize-k0s.sh"), target)
	if out, err := cmd.CombinedOutput(); err == nil || !strings.Contains(string(out), "refusing dirty source checkout") {
		t.Fatalf("dirty checkout accepted: %v %s", err, out)
	}
	if data, err := os.ReadFile(filepath.Join(target, "user-work.txt")); err != nil || string(data) != "preserve me" {
		t.Fatalf("user work changed: %v %s", err, data)
	}
}
