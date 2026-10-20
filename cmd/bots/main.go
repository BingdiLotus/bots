package main

import (
	"context"
	"encoding/json"
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
	"github.com/bingdilotus/bots/internal/domain"
	"github.com/bingdilotus/bots/internal/httpapi"
	"github.com/bingdilotus/bots/internal/service"
	"github.com/bingdilotus/bots/internal/slack"
	"github.com/bingdilotus/bots/internal/store"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 {
		if len(os.Args) == 2 && os.Args[1] == "readyz" {
			if err := probeReady(os.Getenv("BOTS_PROBE_URL")); err != nil {
				logger.Error("readiness probe failed", "error", err)
				os.Exit(1)
			}
			return
		}
		fmt.Fprintln(os.Stderr, "usage: bots [readyz]")
		os.Exit(2)
	}
	if err := start(logger); err != nil {
		logger.Error("bots stopped", "error", err)
		os.Exit(1)
	}
}

// probeReady lets a scratch container or service manager use the binary itself
// as a healthcheck. The response body is deliberately ignored; only readiness
// status matters and probes must not print dependency details.
func probeReady(target string) error {
	if target == "" {
		target = "http://127.0.0.1:8090/readyz"
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get(target)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness returned HTTP %d", response.StatusCode)
	}
	return nil
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
	refs := []string{cfg.RuntimeTokenEnv, cfg.AdminTokenEnv, cfg.InternalTokenEnv, bot.Config.TokenEnv}
	if bot.Config.SlackTransport() == domain.SlackSocket {
		refs = append(refs, bot.Config.AppTokenEnv)
	} else {
		refs = append(refs, bot.Config.SigningSecretEnv)
	}
	for _, ref := range refs {
		if _, err := config.Secret(ref); err != nil {
			return err
		}
	}
	runtime, err := chronotope.NewClient(chronotope.Config{BaseURL: cfg.ChronotopeURL, TokenRef: cfg.RuntimeTokenEnv,
		ResolveToken: func(_ context.Context, ref string) (string, error) { return config.Secret(ref) }})
	if err != nil {
		return err
	}
	var socket *slack.SocketClient
	if bot.Config.SlackTransport() == domain.SlackSocket {
		token, err := config.Secret(bot.Config.AppTokenEnv)
		if err != nil {
			return err
		}
		socket, err = slack.NewSocketClient(token)
		if err != nil {
			return err
		}
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
	readinessMaxAge := max(3*cfg.PollInterval, 60*time.Second)
	app.Ready = func(ctx context.Context) error {
		if err := db.Ping(ctx); err != nil {
			return fmt.Errorf("database: %w", err)
		}
		if err := engine.Ready(ctx, readinessMaxAge); err != nil {
			return fmt.Errorf("dispatcher: %w", err)
		}
		if socket != nil && !socket.Connected() {
			return errors.New("Slack Socket Mode is not connected")
		}
		return nil
	}
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
	var socketErr chan error
	if socket != nil {
		socketErr = make(chan error, 1)
		go func() {
			socketErr <- socket.Run(workerCtx, func(eventCtx context.Context, body json.RawMessage) error {
				err := app.AcceptSlackSocketEvent(eventCtx, bot.ID, body)
				if err != nil && workerCtx.Err() == nil {
					logger.Warn("Slack event admission unconfirmed", "bot_id", bot.ID)
				}
				return err
			})
		}()
		go func() {
			select {
			case <-socket.FirstConnected():
				logger.Info("Slack Socket Mode connected", "workspace_id", bot.Config.WorkspaceID, "bot_id", bot.ID)
			case <-workerCtx.Done():
			}
		}()
		logger.Info("Slack Socket Mode receiver started", "workspace_id", bot.Config.WorkspaceID, "bot_id", bot.ID)
	}
	go func() { workerErr <- engine.Run(workerCtx) }()
	go func() {
		serveErr <- server.Serve(listener)
	}()
	logger.Info("http server listening", "addr", listener.Addr().String())

	var failure error
	serverDone, workerDone, socketDone := false, false, socketErr == nil
	select {
	case err := <-serveErr:
		serverDone, failure = true, fmt.Errorf("serve: %w", err)
	case err := <-workerErr:
		workerDone = true
		if ctx.Err() == nil && err != nil {
			failure = fmt.Errorf("dispatcher: %w", err)
		}
	case err := <-socketErr:
		socketDone = true
		if ctx.Err() == nil && err != nil {
			failure = fmt.Errorf("Slack receiver: %w", err)
		}
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
	if !socketDone {
		select {
		case err := <-socketErr:
			if err != nil && !errors.Is(err, context.Canceled) {
				failure = fmt.Errorf("Slack receiver: %w", err)
			}
		case <-shutdownCtx.Done():
			return fmt.Errorf("Slack receiver shutdown timed out")
		}
	}
	logger.Info("http server stopped")
	return failure
}
