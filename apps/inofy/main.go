// Command inofy is the single-process App (§11): authenticated
// /api/v1 over loopback by default, SQLite the sole authority,
// bounded dispatcher, governed node catalog.
//
// Startup is explicit: state directory + owner token file are
// required; non-loopback listen addresses are rejected without an
// explicit TLS/auth deployment decision.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ProjectViVy/inofy/apps/inofy/internal/auth"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/dispatch"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/httpapi"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/nodes"
	"github.com/ProjectViVy/inofy/apps/inofy/internal/storage"
	"github.com/ProjectViVy/inofy/definitions"
)

func main() {
	stateDir := flag.String("state", ".inofy", "state directory (owner-only)")
	listen := flag.String("listen", "127.0.0.1:8377", "listen address (loopback only)")
	connections := flag.String("connections", "", "JSON file of approved node connections")
	flag.Parse()

	if err := run(*stateDir, *listen, *connections); err != nil {
		log.Fatal(err)
	}
}

func run(stateDir, listen, connFile string) error {
	if err := loopbackOnly(listen); err != nil {
		return err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	if st, err := os.Stat(stateDir); err == nil && st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("state directory %s must be user-only (0700)", stateDir)
	}
	tokFile := filepath.Join(stateDir, "owner.token")
	if _, err := os.Stat(tokFile); os.IsNotExist(err) {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return err
		}
		if err := os.WriteFile(tokFile, []byte(hex.EncodeToString(b)), 0o600); err != nil {
			return err
		}
		log.Printf("inofy: wrote new owner token to %s", tokFile)
	}
	a, err := auth.New(tokFile, auth.Options{})
	if err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	st, err := storage.Open(context.Background(), filepath.Join(stateDir, "app.db"))
	if err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	defer st.Close()

	deps, err := nodeDeps(connFile)
	if err != nil {
		return err
	}
	deps.Ledger = st
	exec := nodes.New(deps)
	cat := nodes.Catalog(deps)

	d := dispatch.New(st, dispatch.Options{}, httpapi.Factory(st, cat, exec))
	if err := d.Start(context.Background()); err != nil {
		return err
	}
	defer d.Stop()

	srv := &http.Server{
		Addr:              listen,
		Handler:           httpapi.NewHandler(httpapi.Dependencies{
			Auth:        a,
			Store:       st,
			Definitions: definitions.NewService(st),
			Dispatch:    d,
			Catalog:     cat,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	log.Printf("inofy: serving %s (api under /api/v1)", listen)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("inofy: %s, draining", s)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	case err := <-errCh:
		return err
	}
}

// loopbackOnly rejects non-loopback listens — remote exposure needs
// an explicit TLS/auth deployment decision (S09 plan acceptance).
func loopbackOnly(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if host == "" || host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("refusing non-loopback listen %q without explicit deployment decision", addr)
}

// nodeDeps loads approved connections from a JSON map (env-name
// secrets only — the file never holds keys).
func nodeDeps(connFile string) (nodes.Dependencies, error) {
	d := nodes.Dependencies{
		Secrets: func(env string) (string, bool) { return os.LookupEnv(env) },
	}
	if connFile == "" {
		return d, nil
	}
	b, err := os.ReadFile(connFile)
	if err != nil {
		return d, err
	}
	if strings.Contains(string(b), "sk-") {
		return d, fmt.Errorf("connection file %s appears to hold a raw key — env names only", connFile)
	}
	if err := json.Unmarshal(b, &d.Connections); err != nil {
		return d, fmt.Errorf("parse connections: %w", err)
	}
	return d, nil
}
