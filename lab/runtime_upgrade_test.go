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
	"github.com/appmana/labcontainers/pkg/windows"
)

//go:embed runtime_upgrade.ps1
var runtimeUpgradeScript []byte

//go:embed runtime_content_gc.ps1
var runtimeContentGC []byte

// Targeted diagnosis uses an explicitly selected retained lab, not another
// bootstrap. The SDK owns reattachment, lifecycle and timed serial execution.
func TestRetainedWindowsRuntimeScript(t *testing.T) {
	if os.Getenv("INTEGRATION_WINDOWS_RETAINED_SCRIPT") != "1" {
		t.Skip("explicit retained runtime script required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	body, err := artifact.ReadFile(ctx, os.Getenv("INTEGRATION_RUNTIME_SCRIPT"), os.Getenv("INTEGRATION_RUNTIME_SCRIPT_SHA256"))
	if err != nil {
		t.Fatal(err)
	}
	dialCtx, dialDone := context.WithTimeout(ctx, 10*time.Second)
	c, err := client.Dial(dialCtx, os.Getenv("LABCONTAINERS_RETAINED_SOCKET"))
	dialDone()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	lab, err := c.Resume(ctx, os.Getenv("LABCONTAINERS_RETAINED_SESSION"))
	if err != nil {
		t.Fatal(err)
	}
	node := lab.Node("windows")
	defer func() {
		cleanup, done := context.WithTimeout(context.Background(), time.Minute)
		defer done()
		if err := node.PowerOff(cleanup); err != nil {
			t.Error(err)
			return
		}
		if err := lab.Keep(cleanup, 2*time.Hour); err != nil {
			t.Error(err)
		}
	}()
	if err := node.Start(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = lab.RunTimeline(ctx, &labv1.TimelineAction{Action: &labv1.TimelineAction_WaitExec{WaitExec: &labv1.WaitExec{
		Exec:          &labv1.ExecRequest{Node: &labv1.NodeRef{Node: "windows"}, Argv: []string{"cmd.exe", "/c", "ver"}, TimeoutMillis: 10000},
		TimeoutMillis: 300000, RetryMillis: 2000, StdoutContains: []byte("Windows"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Put(ctx, `C:\runtime-diagnostic.ps1`, 0600, body); err != nil {
		t.Fatal(err)
	}
	r, err := node.ExecWithTimeout(ctx, 3*time.Minute, "powershell.exe", "-NoProfile", "-NonInteractive", "-File", `C:\runtime-diagnostic.ps1`)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained runtime: %s\n%s", r.Stdout, r.Stderr)
	if r.ExitCode != 0 {
		t.Fatalf("retained runtime exit %d", r.ExitCode)
	}
}

func runWindowsRuntimeUpgrade(t *testing.T, ctx context.Context, lab *client.Session) {
	t.Helper()
	script, err := artifact.ReadFile(ctx, os.Getenv("INTEGRATION_RUNTIME_TRANSACTION_SCRIPT"), os.Getenv("INTEGRATION_RUNTIME_TRANSACTION_SHA256"))
	if err != nil {
		t.Fatal(err)
	}
	node := lab.Node("windows")
	if err := node.Put(ctx, `C:\containerd_transaction.ps1`, 0600, script); err != nil {
		t.Fatal(err)
	}
	if err := node.Put(ctx, `C:\runtime_upgrade.ps1`, 0600, runtimeUpgradeScript); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) *labv1.ExecResponse {
		t.Helper()
		r, err := node.ExecWithTimeout(ctx, 7*time.Minute, args...)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("upgrade guest: %s\n%s", r.Stdout, r.Stderr)
		if r.ExitCode != 0 {
			t.Fatalf("upgrade guest exit %d", r.ExitCode)
		}
		return r
	}
	if err := windows.ConfigureUnattendedRecovery(ctx, node); err != nil {
		t.Fatal(err)
	}
	if err := windows.EnsureFeatures(ctx, node, windows.FeatureOptions{Names: []string{"Containers"}, AllowReboot: true}); err != nil {
		t.Fatal(err)
	}
	r := run("powershell.exe", "-NoProfile", "-NonInteractive", "-File", `C:\runtime_upgrade.ps1`)
	if !strings.Contains(string(r.Stdout), "WINDOWS_RUNTIME_UPGRADE_COMPLETE") {
		t.Fatal("upgrade assertions did not complete")
	}
	if err := node.Put(ctx, `C:\runtime-content-gc.ps1`, 0600, runtimeContentGC); err != nil {
		t.Fatal(err)
	}
	control := run("powershell.exe", "-NoProfile", "-NonInteractive", "-File", `C:\runtime-content-gc.ps1`)
	if !strings.Contains(string(control.Stdout), "SAME_VERSION_GC_CONTROL_COMPLETE") {
		t.Fatal("same-runtime GC control did not complete")
	}
}
