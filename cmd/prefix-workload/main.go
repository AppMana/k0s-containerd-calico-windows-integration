// prefix-workload is an ordinary-container probe, not a HostProcess repair tool.
// Its state directory is persistent lab storage provisioned before the Pod.
package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type event struct {
	RunID  string `json:"runId"`
	Phase  string `json:"phase"`
	SHA256 string `json:"sha256"`
}

func persist(directory string, e event) error {
	// Separate, exclusive files prevent a subsequent instance from rewriting
	// the old instance's evidence. Consumers reject partial JSON until complete.
	f, err := os.OpenFile(filepath.Join(directory, e.RunID+"-"+e.Phase+".json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(e)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func run(ctx context.Context, directory, address string, drain time.Duration) error {
	// Never seed on startup: a missing original payload is a hard failure.
	body, err := os.ReadFile(filepath.Join(directory, "payload.bin"))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("empty original payload")
	}
	sum := sha256.Sum256(body)
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	e := event{RunID: hex.EncodeToString(nonce), Phase: "started", SHA256: hex.EncodeToString(sum[:])}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	if err := persist(directory, e); err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(e); err != nil {
		return err
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(e)
	})}
	result := make(chan error, 1)
	go func() { result <- server.Serve(listener) }()
	select {
	case err := <-result:
		return fmt.Errorf("unexpected HTTP shutdown: %w", err)
	case <-ctx.Done():
	}
	termination := e
	termination.Phase = "notified"
	if err := persist(directory, termination); err != nil {
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return err
	}
	// Deliberate bounded drain distinguishes real grace from immediate killing.
	if drain > 0 {
		time.Sleep(drain)
	}
	f, err := os.OpenFile(filepath.Join(directory, "payload.bin"), os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	body, err = os.ReadFile(filepath.Join(directory, "payload.bin"))
	if err != nil {
		return err
	}
	if sha256.Sum256(body) != sum {
		return fmt.Errorf("original payload changed")
	}
	termination.Phase = "flushed"
	return persist(directory, termination)
}

func main() {
	dir := flag.String("state-dir", "", "existing lab directory containing payload.bin")
	address := flag.String("listen", ":8080", "dual-stack listener")
	drain := flag.Duration("drain", 5*time.Second, "graceful drain duration, bounded below the Pod grace period")
	flag.Parse()
	if *dir == "" || *drain < 0 || *drain > 30*time.Second {
		fmt.Fprintln(os.Stderr, "explicit state directory and drain between zero and thirty seconds required")
		os.Exit(2)
	}
	// Go translates Windows CTRL_SHUTDOWN/CLOSE notifications to SIGTERM when
	// registered. The real Windows control test must prove delivery by runhcs.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, *dir, *address, *drain); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
