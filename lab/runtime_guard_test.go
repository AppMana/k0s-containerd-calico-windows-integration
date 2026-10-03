package lab_test

import (
	"context"
	_ "embed"
	"os"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/artifact"
	"github.com/appmana/labcontainers/pkg/client"
)

//go:embed runtime_guard_live.ps1
var runtimeGuardLive []byte

// Read-only transaction precondition comparison; does not start/stop services,
// change the installed runtime, or assume ownership of the retained VM.
func TestRetainedWindowsRuntimeGuard(t *testing.T) {
	if os.Getenv("INTEGRATION_RUNTIME_GUARD") != "1" {
		t.Skip("explicit retained Windows precondition qualification required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	socket, id := os.Getenv("LABCONTAINERS_RETAINED_SOCKET"), os.Getenv("LABCONTAINERS_RETAINED_SESSION")
	if socket == "" || id == "" {
		t.Fatal("explicit existing lab required")
	}
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	node := &labv1.NodeRef{SessionId: id, Node: "windows"}
	put := func(path string, body []byte) {
		t.Helper()
		if _, err := c.RPC().Put(ctx, &labv1.PutRequest{Node: node, Path: path, Mode: 0600, Content: body}); err != nil {
			t.Fatal(err)
		}
	}
	put(`C:\LabQualification\runtime-guard-live.ps1`, runtimeGuardLive)
	for _, phase := range []string{"BASELINE", "CANDIDATE"} {
		sha := os.Getenv("INTEGRATION_RUNTIME_GUARD_" + phase + "_SHA256")
		body, err := artifact.ReadFile(ctx, os.Getenv("INTEGRATION_RUNTIME_GUARD_"+phase), sha)
		if err != nil {
			t.Fatal(err)
		}
		path := `C:\LabQualification\runtime-guard-` + strings.ToLower(phase) + `.ps1`
		put(path, body)
		r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: node, TimeoutMillis: 30000, Argv: []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-File", `C:\LabQualification\runtime-guard-live.ps1`, "-TransactionPath", path, "-ExpectedSHA256", sha}})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s exit=%d stdout=%s stderr=%s", phase, r.ExitCode, r.Stdout, r.Stderr)
		if phase == "BASELINE" {
			if r.ExitCode == 0 || !strings.Contains(string(r.Stderr), "Unsafe runtime upgrade precondition accepted a live supervised kubelet") {
				t.Fatal("baseline did not reproduce the precise unsafe precondition")
			}
		} else if r.ExitCode != 0 || !strings.Contains(string(r.Stdout), "SUPERVISED_KUBELET_GUARD_COMPLETE") {
			t.Fatal("candidate failed native precondition qualification")
		}
	}
}
