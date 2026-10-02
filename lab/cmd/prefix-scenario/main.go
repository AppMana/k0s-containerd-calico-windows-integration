// prefix-scenario runs inside the existing isolated controller VM. It owns
// workloads and deliberate pool faults only, never cluster installation.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/AppMana/k0s-containerd-calico-windows-integration/internal/prefixcheck"
	"github.com/AppMana/k0s-containerd-calico-windows-integration/lab/internal/prefixscenario"
	"github.com/appmana/labcontainers/pkg/windows"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

var pools = schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "ippools"}

type runtimeSnapshot struct {
	PodUID         string `json:"podUid"`
	RuntimeVersion string `json:"runtimeVersion"`
	Sandboxes      []struct {
		ID    string   `json:"id"`
		State string   `json:"state"`
		IPs   []string `json:"ips"`
	} `json:"sandboxes"`
	Containers []struct {
		ID         string `json:"id"`
		SandboxID  string `json:"sandboxId"`
		State      string `json:"state"`
		ExitCode   int    `json:"exitCode"`
		FinishedAt int64  `json:"finishedAt"`
	} `json:"containers"`
}
type event struct {
	RunID  string `json:"runId"`
	Phase  string `json:"phase"`
	SHA256 string `json:"sha256"`
}
type observed struct {
	prefixcheck.Observation
	RunID string
}
type scenario struct {
	k                                           *kubernetes.Clientset
	d                                           dynamic.Interface
	namespace, root, hostPod, version, evidence string
	linuxImage, linuxProbe                      string
	http                                        *http.Client
}

func (s *scenario) host(ctx context.Context, script string) ([]byte, error) {
	args := []string{"kubectl", "exec", "-n", "kube-system", s.hostPod, "-c", "node", "--"}
	args = append(args, windows.PowerShellCommand(script)...)
	command := exec.CommandContext(ctx, "/usr/local/bin/k0s", args...)
	var stderr bytes.Buffer
	command.Stderr = &stderr
	command.WaitDelay = 2 * time.Second
	out, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("HostProcess observation/setup: %w: %s", err, stderr.String())
	}
	return out, nil
}

func (s *scenario) evidenceFile(name string, data []byte) error {
	f, err := os.OpenFile(filepath.Join(s.evidence, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func wait(ctx context.Context, duration time.Duration, check func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	var last error
	for {
		call, done := context.WithTimeout(ctx, 15*time.Second)
		last = check(call)
		done()
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("deadline: %w; last observation: %v", ctx.Err(), last)
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *scenario) observe(ctx context.Context, name string) (observed, error) {
	var result observed
	p, err := s.k.CoreV1().Pods(s.namespace).Get(ctx, name, meta.GetOptions{})
	if err != nil {
		return result, err
	}
	result.PodUID = string(p.UID)
	for _, c := range p.Status.Conditions {
		if c.Type == core.PodReady && c.Status == core.ConditionTrue {
			result.Ready = true
		}
	}
	for _, ip := range p.Status.PodIPs {
		if a, e := netip.ParseAddr(ip.IP); e == nil && a.Is6() {
			result.IPv6 = ip.IP
		}
	}
	if !result.Ready || result.IPv6 == "" {
		return result, fmt.Errorf("%s not ready dual-stack: %+v", name, p.Status)
	}
	for _, c := range p.Status.ContainerStatuses {
		if c.Name == "probe" && c.Ready {
			result.ContainerID = strings.TrimPrefix(c.ContainerID, "containerd://")
		}
	}
	if result.ContainerID == "" {
		return result, fmt.Errorf("no ready CRI identity for %s", name)
	}
	if p.Spec.NodeName == "windows" {
		body, err := s.host(ctx, `& '`+s.root+`\bin\prefix-runtime.exe' --pod-uid '`+result.PodUID+`'; if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}`)
		if err != nil {
			return result, err
		}
		var snap runtimeSnapshot
		if err = json.Unmarshal(body, &snap); err != nil {
			return result, err
		}
		result.SandboxID, err = readySandbox(snap, result.PodUID, s.version, result.ContainerID, result.IPv6)
		if err != nil {
			return result, err
		}
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://"+net.JoinHostPort(result.IPv6, "8080")+"/", nil)
	resp, err := s.http.Do(req)
	if err != nil {
		return result, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return result, fmt.Errorf("IPv6 HTTP status %d", resp.StatusCode)
	}
	var e event
	if err = json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return result, err
	}
	if e.Phase != "started" || e.RunID == "" {
		return result, fmt.Errorf("missing process identity")
	}
	result.RunID = e.RunID
	result.PayloadSHA256 = e.SHA256
	return result, nil
}

func unchanged(a, b observed) error {
	if a != b {
		return fmt.Errorf("stable control changed: before=%+v after=%+v", a, b)
	}
	return nil
}

func (s *scenario) pool(ctx context.Context, name, prefix, selector string, disabled, manual bool) error {
	p := prefixscenario.IPv6Pool(name, prefix, selector, disabled, manual)
	_, err := s.d.Resource(pools).Create(ctx, p, meta.CreateOptions{})
	return err
}

func (s *scenario) disable(ctx context.Context, name string) error {
	p, err := s.d.Resource(pools).Get(ctx, name, meta.GetOptions{})
	if err != nil {
		return err
	}
	if err = unstructured.SetNestedField(p.Object, true, "spec", "disabled"); err != nil {
		return err
	}
	_, err = s.d.Resource(pools).Update(ctx, p, meta.UpdateOptions{})
	return err
}

func (s *scenario) execute(ctx context.Context, image string) error {
	// This consumer is deliberately not usable against an arbitrary cluster.
	nodes, err := s.k.CoreV1().Nodes().List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	if len(nodes.Items) != 2 {
		return fmt.Errorf("expected exactly two disposable fixture nodes")
	}
	for _, n := range nodes.Items {
		if n.Name != "linux" && n.Name != "windows" {
			return fmt.Errorf("unexpected node %s", n.Name)
		}
		want := "192.0.2.10"
		if n.Name == "windows" {
			want = "192.0.2.20"
		}
		found := false
		for _, address := range n.Status.Addresses {
			if address.Type == core.NodeInternalIP && address.Address == want {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("node %s is outside the explicit lab subnet", n.Name)
		}
	}
	hosts, err := s.k.CoreV1().Pods("kube-system").List(ctx, meta.ListOptions{LabelSelector: "k8s-app=calico-node-windows"})
	if err != nil {
		return err
	}
	if len(hosts.Items) != 1 {
		return fmt.Errorf("expected one Windows Calico HostProcess")
	}
	s.hostPod = hosts.Items[0].Name
	// Seed the Linux control once; never place a second IPv6 pool on the
	// Windows single-network node. The portable probe refuses to reseed.
	linuxRoot := filepath.Join(s.evidence, "linux")
	if err = seedLinuxControl(linuxRoot, s.linuxProbe); err != nil {
		return err
	}
	_, err = s.k.CoreV1().Namespaces().Create(ctx, &core.Namespace{ObjectMeta: meta.ObjectMeta{Name: s.namespace}}, meta.CreateOptions{})
	if err != nil {
		return err
	}
	// Prepare unique data once, before any workload. Restarted probes cannot seed.
	_, err = s.host(ctx, `$root='`+s.root+`'; if(Test-Path $root){throw 'scenario directory already exists'}; New-Item -ItemType Directory -Path ($root+'\bin') | Out-Null; $media=(Get-Volume -FileSystemLabel LCQUAL).DriveLetter+':\'; Copy-Item ($media+'prefix-workload.exe') ($root+'\bin\'); Copy-Item ($media+'prefix-runtime.exe') ($root+'\bin\'); foreach($name in @('target','control')){$dir=New-Item -ItemType Directory -Path ($root+'\'+$name);$bytes=New-Object byte[] 32768;[Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes);$f=[IO.File]::Open(($dir.FullName+'\payload.bin'),[IO.FileMode]::CreateNew);try{$f.Write($bytes,0,$bytes.Length);$f.Flush($true)}finally{$f.Dispose()}}; & ([IO.Path]::Combine([Environment]::SystemDirectory,'icacls.exe')) $root /grant '*S-1-1-0:(OI)(CI)M' /T | Out-Null; if($LASTEXITCODE -ne 0){exit $LASTEXITCODE}`)
	if err != nil {
		return err
	}
	selector := "kubernetes.io/hostname == 'windows'"
	if err = s.pool(ctx, s.namespace+"-linux", "2001:db8:100:90::/64", "kubernetes.io/hostname == 'linux'", false, false); err != nil {
		return err
	}
	if err = s.pool(ctx, s.namespace+"-0", "2001:db8:100:1::/64", selector, false, false); err != nil {
		return err
	}
	all, err := s.d.Resource(pools).List(ctx, meta.ListOptions{})
	if err != nil {
		return err
	}
	for _, p := range all.Items {
		cidr, _, _ := unstructured.NestedString(p.Object, "spec", "cidr")
		prefix, e := netip.ParsePrefix(cidr)
		if e != nil {
			return e
		}
		if prefix.Addr().Is6() && !strings.HasPrefix(p.GetName(), s.namespace+"-") {
			if err = s.disable(ctx, p.GetName()); err != nil {
				return err
			}
		}
	}
	for _, pod := range []*core.Pod{
		prefixscenario.WindowsWorkload(s.namespace, "target", image, s.root, ""),
		prefixscenario.LinuxControl(s.namespace, s.linuxImage, linuxRoot),
	} {
		if _, err = s.k.CoreV1().Pods(s.namespace).Create(ctx, pod, meta.CreateOptions{}); err != nil {
			return err
		}
	}
	var current, control observed
	err = wait(ctx, 5*time.Minute, func(ctx context.Context) error {
		var e error
		current, e = s.observe(ctx, "target")
		if e != nil {
			return e
		}
		control, e = s.observe(ctx, "control")
		return e
	})
	if err != nil {
		return err
	}
	initial, _ := json.Marshal(map[string]any{"target": current, "control": control})
	if err = s.evidenceFile("baseline.json", initial); err != nil {
		return err
	}
	// Observe native CRI continuously so short-lived old exit records survive GC.
	// Keep the foreground remote exec owned by this Go context. PowerShell
	// Start-Process inside HostProcess exec can keep its parent exec open even
	// with redirected streams; it is not a reliable detached-job API.
	observerCtx, stopObserver := context.WithCancel(ctx)
	defer stopObserver()
	observerDone := make(chan struct{})
	var observerErr error
	go func() {
		_, observerErr = s.host(observerCtx, `& '`+s.root+`\bin\prefix-runtime.exe' --pod-uid '`+current.PodUID+`' --watch '`+s.root+`\runtime.jsonl' --duration '20m'; exit $LASTEXITCODE`)
		close(observerDone)
	}()
	err = wait(ctx, 20*time.Second, func(ctx context.Context) error {
		select {
		case <-observerDone:
			return fmt.Errorf("observer exited before fault injection: %v", observerErr)
		default:
		}
		_, e := s.host(ctx, `$entry=Get-Content -LiteralPath '`+s.root+`\runtime.jsonl' -TotalCount 1 | ConvertFrom-Json; if(!$entry.snapshot -or $entry.error){throw 'observer has not recorded a successful CRI snapshot'}`)
		return e
	})
	if err != nil {
		return err
	}
	for round := 1; round <= 3; round++ {
		select {
		case <-observerDone:
			return fmt.Errorf("observer exited during qualification: %v", observerErr)
		default:
		}
		nextPrefix := fmt.Sprintf("2001:db8:100:%x::/64", round+1)
		if err = s.pool(ctx, fmt.Sprintf("%s-%d", s.namespace, round), nextPrefix, selector, false, false); err != nil {
			return err
		}
		// Adding a pool without invalidating the existing address must not restart.
		for sample := 0; sample < 5; sample++ {
			now, e := s.observe(ctx, "target")
			if e != nil {
				return e
			}
			if e = unchanged(current, now); e != nil {
				return e
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
		if err = s.disable(ctx, fmt.Sprintf("%s-%d", s.namespace, round-1)); err != nil {
			return err
		}
		var after observed
		err = wait(ctx, 4*time.Minute, func(ctx context.Context) error {
			var e error
			after, e = s.observe(ctx, "target")
			if e != nil {
				return e
			}
			if after.SandboxID == current.SandboxID || after.RunID == current.RunID {
				return fmt.Errorf("waiting for native replacement")
			}
			return nil
		})
		if err != nil {
			return err
		}
		shutdown, log, err := s.shutdown(ctx, current)
		if err != nil {
			return err
		}
		if err = s.evidenceFile(fmt.Sprintf("round-%d-runtime.jsonl", round), log); err != nil {
			return err
		}
		if err = prefixcheck.ValidateReplacement(current.Observation, after.Observation, shutdown, netip.MustParsePrefix(nextPrefix)); err != nil {
			return err
		}
		now, err := s.observe(ctx, "control")
		if err != nil {
			return err
		}
		if err = unchanged(control, now); err != nil {
			return err
		}
		// Confirm the old endpoint is removed, without deleting or repairing it.
		err = wait(ctx, time.Minute, func(ctx context.Context) error {
			_, e := s.host(ctx, `$eps=@(Get-HnsEndpoint);foreach($ep in $eps){if(($ep | ConvertTo-Json -Depth 30 -Compress).Contains('`+current.IPv6+`')){throw 'old IPv6 endpoint remains'}}`)
			return e
		})
		if err != nil {
			return err
		}
		data, _ := json.Marshal(map[string]any{"before": current, "after": after, "shutdown": shutdown, "control": now})
		if err = s.evidenceFile(fmt.Sprintf("round-%d.json", round), data); err != nil {
			return err
		}
		fmt.Printf("rotation %d: %s -> %s; original data and stable control retained\n", round, current.IPv6, after.IPv6)
		current = after
	}
	return nil
}

func (s *scenario) shutdown(ctx context.Context, before observed) (prefixcheck.Shutdown, []byte, error) {
	shutdown := prefixcheck.Shutdown{ContainerID: before.ContainerID}
	body, err := s.host(ctx, `Get-Content -Raw '`+s.root+`\runtime.jsonl'`)
	if err != nil {
		return shutdown, nil, err
	}
	shutdown, err = runtimeShutdown(body, before, s.version)
	if err != nil {
		return shutdown, body, err
	}
	for _, phase := range []string{"notified", "flushed"} {
		data, e := s.host(ctx, `Get-Content -Raw '`+s.root+`\target\`+before.RunID+`-`+phase+`.json'`)
		if e != nil {
			return shutdown, body, e
		}
		var event event
		if e = json.Unmarshal(data, &event); e != nil {
			return shutdown, body, e
		}
		if event.RunID != before.RunID || event.Phase != phase || event.SHA256 != before.PayloadSHA256 {
			return shutdown, body, fmt.Errorf("shutdown evidence identity mismatch")
		}
		if phase == "notified" {
			shutdown.Notified = true
		} else {
			shutdown.Flushed = true
		}
	}
	return shutdown, body, nil
}

func run() error {
	image := flag.String("windows-image", "", "exact preloaded ordinary Windows image")
	linuxImage := flag.String("linux-image", "", "exact preloaded ordinary Linux image")
	linuxProbe := flag.String("linux-probe", "/mnt/qualification/prefix-workload", "already staged portable Linux workload executable")
	version := flag.String("runtime-version", "", "expected CRI runtime version")
	flag.Parse()
	if !strings.Contains(*image, "@sha256:") || !strings.Contains(*linuxImage, "@sha256:") || *version == "" {
		return fmt.Errorf("explicit pinned image and runtime version required")
	}
	config, err := clientcmd.BuildConfigFromFlags("", "/var/lib/k0s/pki/admin.conf")
	if err != nil {
		return err
	}
	config.Timeout = 15 * time.Second
	k, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	d, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("prefix-%d", time.Now().Unix())
	dir := filepath.Join("/var/tmp", id)
	if err = os.Mkdir(dir, 0700); err != nil {
		return err
	}
	s := &scenario{k: k, d: d, namespace: id, root: `C:\LabPrefix\` + id, version: *version, evidence: dir, linuxImage: *linuxImage, linuxProbe: *linuxProbe, http: &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	fmt.Println("prefix evidence:", dir)
	if err = s.execute(ctx, *image); err != nil {
		return err
	}
	fmt.Println("PREFIX_ROTATION_COMPLETE")
	return nil
}

func seedLinuxControl(root, probe string) error {
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	for _, sub := range []string{"bin", "control"} {
		if err := os.Mkdir(filepath.Join(root, sub), 0700); err != nil {
			return err
		}
	}
	in, err := os.Open(probe)
	if err != nil {
		return err
	}
	defer in.Close()
	for _, file := range []struct {
		path   string
		mode   os.FileMode
		source io.Reader
	}{
		{filepath.Join(root, "bin", "prefix-workload"), 0755, in},
		{filepath.Join(root, "control", "payload.bin"), 0600, io.LimitReader(rand.Reader, 32768)},
	} {
		out, err := os.OpenFile(file.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, file.mode)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, file.source)
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
