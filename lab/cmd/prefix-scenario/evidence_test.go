package main

import (
	"encoding/json"
	"testing"
)

func TestCRIIdentityMustMatchReadyPodAndAddress(t *testing.T) {
	data := []byte(`{"podUid":"pod","runtimeVersion":"candidate","sandboxes":[{"id":"box","state":"SANDBOX_READY","ips":["2001:db8::1"]}],"containers":[{"id":"container","sandboxId":"box","state":"CONTAINER_RUNNING"}]}`)
	var snap runtimeSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatal(err)
	}
	if id, err := readySandbox(snap, "pod", "candidate", "container", "2001:db8::1"); err != nil || id != "box" {
		t.Fatal(id, err)
	}
	for _, args := range [][4]string{{"wrong", "candidate", "container", "2001:db8::1"}, {"pod", "vanilla", "container", "2001:db8::1"}, {"pod", "candidate", "different", "2001:db8::1"}, {"pod", "candidate", "container", "2001:db8::2"}} {
		if _, err := readySandbox(snap, args[0], args[1], args[2], args[3]); err == nil {
			t.Fatal("accepted mismatched identity", args)
		}
	}
	snap.Sandboxes[0].State = "SANDBOX_NOTREADY"
	if _, err := readySandbox(snap, "pod", "candidate", "container", "2001:db8::1"); err == nil {
		t.Fatal("accepted invalid sandbox")
	}
}

func TestShutdownRequiresObservedInvalidationAndExactExit(t *testing.T) {
	before := observed{}
	before.PodUID = "pod"
	before.SandboxID = "old-box"
	before.ContainerID = "old-container"
	valid := []byte(`{"snapshot":{"podUid":"pod","runtimeVersion":"candidate","sandboxes":[{"id":"old-box","state":"SANDBOX_NOTREADY"}]}}
{"snapshot":{"podUid":"pod","runtimeVersion":"candidate","containers":[{"id":"old-container","sandboxId":"old-box","state":"CONTAINER_EXITED","exitCode":0,"finishedAt":1}]}}
`)
	shutdown, err := runtimeShutdown(valid, before, "candidate")
	if err != nil || !shutdown.Exited || shutdown.ExitCode != 0 {
		t.Fatal(shutdown, err)
	}
	for _, data := range [][]byte{nil, []byte(`{"snapshot":{"podUid":"pod","runtimeVersion":"candidate","containers":[{"id":"old-container","sandboxId":"old-box","state":"CONTAINER_EXITED","finishedAt":1}]}}`), []byte(`{"snapshot":{"podUid":"pod","runtimeVersion":"candidate","sandboxes":[{"id":"old-box","state":"SANDBOX_NOTREADY"}]}}`)} {
		if _, err := runtimeShutdown(data, before, "candidate"); err == nil {
			t.Fatal("incomplete lifecycle evidence accepted")
		}
	}
	if _, err := runtimeShutdown(valid, before, "different"); err == nil {
		t.Fatal("wrong runtime accepted")
	}
}
