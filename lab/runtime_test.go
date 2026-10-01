package lab_test

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/client"
	clab "github.com/appmana/labcontainers/pkg/containerlab"
	"github.com/srl-labs/containerlab/core"
	"github.com/srl-labs/containerlab/types"
)

// This is native execution of component tests, NOT live CNI replacement or an
// upgrade qualification. It needs no NIC, cluster, egress, or production secret.
func TestWindowsRuntimeCandidate(t *testing.T) {
	if os.Getenv("INTEGRATION_WINDOWS_RUNTIME") != "1" {
		t.Skip("set INTEGRATION_WINDOWS_RUNTIME=1 and explicit pinned lab inputs")
	}
	required := func(key string) string {
		t.Helper()
		v := os.Getenv(key)
		if v == "" {
			t.Fatalf("%s required", key)
		}
		return v
	}
	image := required("LABCONTAINERS_WINDOWS_IMAGE")
	daemon := required("LABCONTAINERS_LABD")
	state := required("LABCONTAINERS_STATE_DIR")
	media := required("INTEGRATION_RUNTIME_MEDIA")
	digest := required("INTEGRATION_RUNTIME_MEDIA_SHA256")
	version := required("INTEGRATION_RUNTIME_VERSION")
	revision := required("INTEGRATION_RUNTIME_REVISION")
	if !filepath.IsAbs(state) || !filepath.IsAbs(media) || !filepath.IsAbs(daemon) {
		t.Fatal("durable absolute state, media, and daemon paths required")
	}
	data, err := os.ReadFile(media)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
		t.Fatal("media digest mismatch")
	}
	// Binary build metadata must match the SDK used by this module.
	metadata, err := exec.Command("go", "version", "-m", daemon).CombinedOutput()
	if err != nil || (!strings.Contains(string(metadata), "beb4a55a73bd") && !strings.Contains(string(metadata), "vcs.revision=beb4a55a73bdb2529e0ec4771d191c607c1e8988")) {
		t.Fatalf("daemon does not attest the pinned SDK source: %s: %v", metadata, err)
	}
	if strings.Contains(string(metadata), "vcs.modified=true") {
		t.Fatal("dirty daemon source is not the pinned SDK build")
	}
	imageRevision, err := exec.Command("docker", "image", "inspect", image, "--format", `{{index .Config.Labels "appmana.labcontainers.revision"}}`).CombinedOutput()
	if err != nil || strings.TrimSpace(string(imageRevision)) != "beb4a55a73bdb2529e0ec4771d191c607c1e8988" {
		t.Fatalf("Windows image helper must match SDK/daemon: %s: %v", imageRevision, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	c, err := client.Launch(ctx, client.Options{LabdPath: daemon, StateDir: state})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	}()
	topology, err := clab.Source(&core.Config{Topology: &types.Topology{Nodes: map[string]*types.NodeDefinition{
		"windows": {Kind: "generic_vm", Image: image, NetworkMode: "none", ImagePullPolicy: "Never",
			Binds: []string{media + ":/runtime.iso:ro"}, Env: map[string]string{
				"QEMU_MEMORY": "4096", "QEMU_SMP": "4",
				"QEMU_ADDITIONAL_ARGS": "-drive file=/runtime.iso,format=raw,media=cdrom,readonly=on",
			}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	lab, err := c.Start(ctx, &labv1.LabSpec{Topology: topology, Nodes: map[string]*labv1.NodeExtension{"windows": {Control: "qga"}}}, 17*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("retained artifacts: %s", lab.Artifacts())
	// No Keep: Close destroys only this owned lab, on success and failure.
	_, err = lab.RunTimeline(ctx, &labv1.TimelineAction{Action: &labv1.TimelineAction_WaitExec{WaitExec: &labv1.WaitExec{
		Exec:          &labv1.ExecRequest{Node: &labv1.NodeRef{Node: "windows"}, Argv: []string{"cmd.exe", "/c", "ver"}, TimeoutMillis: 10000},
		TimeoutMillis: 600000, RetryMillis: 2000, StdoutContains: []byte("Windows"),
	}}})
	if err != nil {
		t.Fatal(err)
	}
	// Inputs are data, not PowerShell source. Restrict version/revision before
	// inserting them into the single-quoted expected-output assertions.
	for _, value := range []string{version, revision} {
		if strings.ContainsAny(value, "'\"`$\r\n") {
			t.Fatal("invalid expected build identity")
		}
	}
	script := `$ErrorActionPreference='Stop';
$media=@(Get-Volume | Where-Object DriveType -eq 'CD-ROM' | ForEach-Object { "$($_.DriveLetter):\" } | Where-Object { Test-Path (Join-Path $_ 'SHA256SUMS') });
if($media.Count -ne 1){throw 'Expected exactly one runtime qualification medium'};
Set-Location $media[0];
foreach($line in Get-Content SHA256SUMS){$parts=$line -split '\s+',2; if((Get-FileHash $parts[1] -Algorithm SHA256).Hash.ToLowerInvariant() -ne $parts[0]){throw "Checksum mismatch: $($parts[1])"}};
$v=(& .\package\bin\containerd.exe --version); if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}; $v;
if(-not $v.Contains('` + version + `') -or -not $v.Contains('` + revision + `')){throw 'Runtime build identity mismatch'};
& .\package\bin\ctr.exe --version; if($LASTEXITCODE -ne 0){exit $LASTEXITCODE};
$testArgs=@('-test.v','-test.run','TestCheck|TestListPodSandbox.*Check|Test.*SandboxName|TestPodSandboxStatus','-test.timeout','5m');
& .\cri-server.test.exe @testArgs;
if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}; Write-Output 'WINDOWS_RUNTIME_COMPONENTS_COMPLETE'`
	result, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: lab.Node("windows").Ref(), Argv: []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script}, TimeoutMillis: int64((6 * time.Minute) / time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("native Windows output:\n%s\n%s", result.Stdout, result.Stderr)
	if result.ExitCode != 0 || !strings.Contains(string(result.Stdout), "WINDOWS_RUNTIME_COMPONENTS_COMPLETE") || !strings.Contains(string(result.Stdout), "--- PASS: TestCheck") {
		t.Fatal("native Windows component tests did not pass")
	}
}
