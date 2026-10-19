package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bingdilotus/bots/internal/chronotope"
	"github.com/bingdilotus/bots/internal/config"
	"github.com/bingdilotus/bots/internal/httpapi"
	"github.com/bingdilotus/bots/internal/service"
	"github.com/bingdilotus/bots/internal/slack"
	"github.com/bingdilotus/bots/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := start(logger); err != nil {
		logger.Error("bots stopped", "error", err)
		os.Exit(1)
	}
}

func start(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serve(ctx, cfg, logger)
}

func serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	seed, err := config.LoadSeed(cfg.SeedPath)
	if err != nil {
		return err
	}
	// The seed is import-only. A restart uses the persisted governance edits.
	startupCtx, cancelStartup := context.WithTimeout(ctx, 15*time.Second)
	defer cancelStartup()
	db, err := store.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		// A driver's parse/connection error can contain the credential-bearing
		// DSN. Startup logs identify the setting without echoing that value.
		return fmt.Errorf("open Bots database: check BOTS_DATABASE_URL and database connectivity")
	}
	defer db.Close()
	if err := db.Bootstrap(startupCtx); err != nil {
		return fmt.Errorf("initialize Bots schema: %w", err)
	}
	bot, err := db.ImportBot(startupCtx, seed)
	if err != nil {
		return fmt.Errorf("import Bot: %w", err)
	}
	for _, ref := range []string{cfg.RuntimeTokenEnv, cfg.AdminTokenEnv, cfg.InternalTokenEnv, bot.Config.TokenEnv, bot.Config.SigningSecretEnv} {
		if _, err := config.Secret(ref); err != nil {
			return err
		}
	}
	runtime, err := chronotope.NewClient(chronotope.Config{BaseURL: cfg.ChronotopeURL, TokenRef: cfg.RuntimeTokenEnv,
		ResolveToken: func(_ context.Context, ref string) (string, error) { return config.Secret(ref) }})
	if err != nil {
		return err
	}
	channel, err := slack.NewClient(cfg.SlackAPIURL, nil)
	if err != nil {
		return err
	}
	engine := &service.Engine{Store: db, Runtime: runtime, Slack: channel, TenantID: bot.TenantID, Interval: cfg.PollInterval, Logger: logger}
	release, err := engine.Initialize(startupCtx)
	if err != nil {
		return err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = release(closeCtx)
	}()
	mux := httpapi.New()
	app := &httpapi.App{Store: db, TenantID: bot.TenantID, AdminTokenEnv: cfg.AdminTokenEnv, InternalTokenEnv: cfg.InternalTokenEnv, UpdateConfig: engine.UpdateConfig}
	app.Register(mux)
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErr := make(chan error, 1)
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	workerErr := make(chan error, 1)
	go func() { workerErr <- engine.Run(workerCtx) }()
	go func() {
		serveErr <- server.Serve(listener)
	}()
	logger.Info("http server listening", "addr", listener.Addr().String())

	var failure error
	serverDone, workerDone := false, false
	select {
	case err := <-serveErr:
		serverDone, failure = true, fmt.Errorf("serve: %w", err)
	case err := <-workerErr:
		workerDone, failure = true, fmt.Errorf("dispatcher: %w", err)
	case <-ctx.Done():
	}
	stopWorker()

	logger.Info("http server shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		_ = server.Close()
		return fmt.Errorf("shutdown: %w", err)
	}
	if !serverDone {
		if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
			failure = fmt.Errorf("serve: %w", err)
		}
	}
	if !workerDone {
		select {
		case err := <-workerErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				failure = fmt.Errorf("dispatcher: %w", err)
			}
		case <-shutdownCtx.Done():
			return fmt.Errorf("dispatcher shutdown timed out")
		}
	}
	logger.Info("http server stopped")
	return failure
}
