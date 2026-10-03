package lab_test

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	labv1 "github.com/appmana/labcontainers/api/v1"
	"github.com/appmana/labcontainers/pkg/artifact"
	"github.com/appmana/labcontainers/pkg/client"
	"k8s.io/apimachinery/pkg/util/version"
)

//go:embed runtime_workload_upgrade.ps1
var runtimeWorkloadUpgrade []byte

// Explicit retained baseline only: serial control performs the shared runtime
// transaction; the independent Linux consumer verifies actual workload state.
func TestRetainedWindowsWorkloadRuntimeUpgrade(t *testing.T) {
	if os.Getenv("INTEGRATION_WORKLOAD_RUNTIME_UPGRADE") != "1" {
		t.Skip("explicit forward workload upgrade required")
	}
	required := func(key string) string {
		t.Helper()
		v := os.Getenv(key)
		if v == "" {
			t.Fatalf("%s required", key)
		}
		return v
	}
	id, socket := required("LABCONTAINERS_RETAINED_SESSION"), required("LABCONTAINERS_RETAINED_SOCKET")
	namespace := required("INTEGRATION_UPGRADE_NAMESPACE")
	if !regexp.MustCompile(`^prefix-[0-9]+$`).MatchString(namespace) {
		t.Fatal("explicit existing workload namespace required")
	}
	transactionSHA, consumerSHA := required("INTEGRATION_RUNTIME_TRANSACTION_SHA256"), required("INTEGRATION_UPGRADE_CONSUMER_SHA256")
	for _, sha := range []string{transactionSHA, consumerSHA} {
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(sha) {
			t.Fatal("explicit SHA256 required")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	input, err := artifact.ReadFile(ctx, required("INTEGRATION_RUNTIME_UPGRADE_INPUTS"), required("INTEGRATION_RUNTIME_UPGRADE_INPUTS_SHA256"))
	if err != nil {
		t.Fatal(err)
	}
	var inputs struct {
		Baseline, Candidate struct{ Version, File, SHA256 string }
	}
	if err := json.Unmarshal(input, &inputs); err != nil {
		t.Fatal(err)
	}
	old, err := version.ParseSemantic("v" + inputs.Baseline.Version)
	if err != nil {
		t.Fatal(err)
	}
	next, err := version.ParseSemantic("v" + inputs.Candidate.Version)
	if err != nil || !old.LessThan(next) {
		t.Fatal("strictly forward runtime upgrade required", err)
	}
	c, err := client.Dial(ctx, socket)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	run := func(node string, timeout time.Duration, args ...string) []byte {
		t.Helper()
		r, err := c.RPC().Exec(ctx, &labv1.ExecRequest{Node: &labv1.NodeRef{SessionId: id, Node: node}, TimeoutMillis: timeout.Milliseconds(), Argv: args})
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: %s%s", node, r.Stdout, r.Stderr)
		if r.ExitCode != 0 {
			t.Fatalf("%s exit=%d", node, r.ExitCode)
		}
		return r.Stdout
	}
	var baseline struct {
		RuntimeVersion string `json:"runtimeVersion"`
	}
	if err := json.Unmarshal(run("linux", time.Minute, "cat", "/var/tmp/"+namespace+"/baseline.json"), &baseline); err != nil {
		t.Fatal(err)
	}
	if baseline.RuntimeVersion != "v"+inputs.Baseline.Version {
		t.Fatal("dataset was not prepared on the selected baseline runtime")
	}
	hash := strings.Fields(string(run("linux", time.Minute, "sha256sum", "/usr/local/bin/kubernetes-workload")))
	if len(hash) != 2 || hash[0] != consumerSHA {
		t.Fatal("independent consumer binary identity mismatch")
	}
	path := `C:\LabQualification\runtime-workload-upgrade.ps1`
	if _, err := c.RPC().Put(ctx, &labv1.PutRequest{Node: &labv1.NodeRef{SessionId: id, Node: "windows"}, Path: path, Mode: 0600, Content: runtimeWorkloadUpgrade}); err != nil {
		t.Fatal(err)
	}
	run("windows", 7*time.Minute, "powershell.exe", "-NoProfile", "-NonInteractive", "-File", path, "-TransactionSHA256", transactionSHA, "-BaselineVersion", inputs.Baseline.Version, "-CandidateVersion", inputs.Candidate.Version, "-ArchiveName", inputs.Candidate.File, "-ArchiveSHA256", inputs.Candidate.SHA256)
	result := run("linux", 6*time.Minute, "/usr/local/bin/kubernetes-workload", "--verify-runtime-upgrade="+namespace, "--runtime-version=v"+inputs.Candidate.Version)
	found := false
	for _, line := range strings.Split(string(result), "\n") {
		if strings.TrimSpace(line) == "RUNTIME_WORKLOAD_UPGRADE_COMPLETE" {
			found = true
		}
	}
	if !found {
		t.Fatal("independent workload verification did not complete")
	}
	t.Log("RETAINED_RUNTIME_WORKLOAD_UPGRADE_COMPLETE")
}
