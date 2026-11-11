package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"easynr/internal/auth"
	"easynr/internal/config"
	"easynr/internal/httpapi"
	"easynr/internal/registry"
	"easynr/internal/state"
)

func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		args = []string{"serve"}
	}
	if args[0] != "serve" && args[0] != "gc" && args[0] != "gc-dry-run" && !(len(args) == 2 && args[0] == "admin" && args[1] == "reset-password") {
		return errors.New("usage: easynr [serve|gc|gc-dry-run|admin reset-password]")
	}
	rest := args[1:]
	if args[0] == "admin" {
		rest = nil
	}
	c, err := config.Parse(rest)
	if err != nil {
		return err
	}
	if args[0] != "serve" {
		if _, err := os.Stat(filepath.Join(c.Data, "metadata.db")); err != nil {
			return fmt.Errorf("existing data directory required: %w", err)
		}
	}
	s, err := state.Open(c.Data)
	if err != nil {
		return err
	}
	defer s.Close()
	if args[0] == "admin" {
		return auth.ResetPassword(s, out)
	}
	if args[0] == "serve" {
		if err := auth.EnsureBootstrap(s, out); err != nil {
			return err
		}
	}
	engine, err := registry.New(s, filepath.Join(c.Data, "registry"), c.PublicURL)
	if err != nil {
		return err
	}
	if args[0] != "serve" {
		result, err := engine.GC(args[0] == "gc-dry-run")
		if err != nil {
			return err
		}
		return json.NewEncoder(out).Encode(result)
	}
	handler, err := httpapi.New(auth.Service{Store: s}, engine)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", c.Listen)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "EasyNR listening on %s\n", listener.Addr())
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20, BaseContext: func(net.Listener) context.Context { return ctx }}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stop, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := server.Shutdown(stop); err != nil {
			_ = server.Close()
			return err
		}
		err = <-done
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "easynr:", err)
		os.Exit(1)
	}
}
