// prefix-runtime observes the real Windows CRI through its local named pipe.
// It never stops a container, rewrites CNI state, or repairs a sandbox.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/Microsoft/go-winio"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	api "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type sandbox struct {
	ID    string   `json:"id"`
	State string   `json:"state"`
	IPs   []string `json:"ips"`
}
type container struct {
	ID         string `json:"id"`
	SandboxID  string `json:"sandboxId"`
	State      string `json:"state"`
	ExitCode   int32  `json:"exitCode"`
	FinishedAt int64  `json:"finishedAt"`
}
type snapshot struct {
	PodUID         string      `json:"podUid"`
	RuntimeVersion string      `json:"runtimeVersion"`
	Sandboxes      []sandbox   `json:"sandboxes"`
	Containers     []container `json:"containers"`
}

func observe(ctx context.Context, client api.RuntimeServiceClient, uid string) (snapshot, error) {
	s := snapshot{PodUID: uid}
	v, err := client.Version(ctx, &api.VersionRequest{})
	if err != nil {
		return s, err
	}
	s.RuntimeVersion = v.RuntimeVersion
	list, err := client.ListPodSandbox(ctx, &api.ListPodSandboxRequest{})
	if err != nil {
		return s, err
	}
	ids := map[string]bool{}
	for _, pod := range list.Items {
		if pod.GetMetadata().GetUid() != uid {
			continue
		}
		ids[pod.Id] = true
		status, err := client.PodSandboxStatus(ctx, &api.PodSandboxStatusRequest{PodSandboxId: pod.Id})
		if err != nil {
			return s, err
		}
		r := sandbox{ID: pod.Id, State: status.Status.State.String()}
		if n := status.Status.Network; n != nil {
			r.IPs = append(r.IPs, n.Ip)
			for _, ip := range n.AdditionalIps {
				r.IPs = append(r.IPs, ip.Ip)
			}
		}
		s.Sandboxes = append(s.Sandboxes, r)
	}
	containers, err := client.ListContainers(ctx, &api.ListContainersRequest{})
	if err != nil {
		return s, err
	}
	for _, c := range containers.Containers {
		if !ids[c.PodSandboxId] && c.Labels["io.kubernetes.pod.uid"] != uid {
			continue
		}
		status, err := client.ContainerStatus(ctx, &api.ContainerStatusRequest{ContainerId: c.Id})
		if err != nil {
			return s, err
		}
		s.Containers = append(s.Containers, container{ID: c.Id, SandboxID: c.PodSandboxId, State: status.Status.State.String(), ExitCode: status.Status.ExitCode, FinishedAt: status.Status.FinishedAt})
	}
	return s, nil
}

func run() error {
	endpoint := flag.String("endpoint", `\\.\pipe\containerd-containerd`, "local CRI named pipe")
	uid := flag.String("pod-uid", "", "exact Kubernetes Pod UID")
	watch := flag.String("watch", "", "exclusive JSONL output path; empty takes one snapshot")
	duration := flag.Duration("duration", 15*time.Minute, "bounded observation lifetime")
	flag.Parse()
	if *uid == "" || !strings.HasPrefix(*endpoint, `\\.\pipe\`) || *duration <= 0 || *duration > 30*time.Minute {
		return fmt.Errorf("explicit Pod UID, local pipe and bounded duration required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()
	conn, err := grpc.NewClient("passthrough:///cri", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return winio.DialPipeContext(ctx, *endpoint) }))
	if err != nil {
		return err
	}
	defer conn.Close()
	client := api.NewRuntimeServiceClient(conn)
	if *watch == "" {
		call, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		s, err := observe(call, client, *uid)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(s)
	}
	f, err := os.OpenFile(*watch, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	last := ""
	for ctx.Err() == nil {
		call, done := context.WithTimeout(ctx, 3*time.Second)
		s, err := observe(call, client, *uid)
		done()
		entry := map[string]any{"snapshot": s}
		if err != nil {
			entry["error"] = err.Error()
		}
		identity, _ := json.Marshal(entry)
		if string(identity) != last {
			entry["observedAt"] = time.Now().UTC()
			if err := json.NewEncoder(f).Encode(entry); err != nil {
				return err
			}
			if err := f.Sync(); err != nil {
				return err
			}
			last = string(identity)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(200 * time.Millisecond):
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
