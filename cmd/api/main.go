package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FlyTOmeLight/agentic-kit-api/internal/config"
	"github.com/FlyTOmeLight/agentic-kit-api/internal/httpapi"
	"github.com/FlyTOmeLight/agentic-kit-api/pkg/durable"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		slog.Error("api stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	path := flag.String("config", os.Getenv("CONFIG_FILE"), "TOML configuration file")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if err = cfg.ValidateAPI(); err != nil {
		return err
	}
	agents, err := config.LoadAgents(cfg.Agents.File)
	if err != nil {
		return err
	}
	tokens, err := config.LoadTokens(cfg.Auth.TokensJSON)
	if err != nil {
		return err
	}
	store, err := durable.Open(ctx, cfg.Database.URL)
	if err != nil {
		return err
	}
	defer store.Close()
	opts, err := redis.ParseURL(cfg.Redis.URL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		store.Relay(ctx, rdb, func(err error) { slog.Error("outbox publish failed", "error", err) })
	}()
	addr := cfg.HTTP.ListenAddr
	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewRouter(store, rdb, agents, tokens),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-done:
		stop()
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err = server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
	}
	stop()
	<-relayDone
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}
