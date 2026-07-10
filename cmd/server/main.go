package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	webui "github.com/xtawa/classing-backend"
	"github.com/xtawa/classing-backend/internal/config"
	"github.com/xtawa/classing-backend/internal/httpapi"
	"github.com/xtawa/classing-backend/internal/store"
	"github.com/xtawa/classing-backend/internal/worker"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	data, err := store.Open(ctx, cfg.DatabaseDriver, cfg.DatabaseURL)
	if err != nil {
		logger.Error("open data store", "error", err)
		os.Exit(1)
	}
	defer data.Close()

	created, err := data.BootstrapAdmin(ctx, cfg.BootstrapAdminUser, cfg.BootstrapAdminEmail, cfg.BootstrapAdminPass)
	if err != nil {
		logger.Error("bootstrap administrator", "error", err)
		os.Exit(1)
	}
	if created {
		logger.Info("bootstrap administrator created", "email", cfg.BootstrapAdminEmail)
	}

	api := httpapi.New(cfg, data, webui.Files(), logger)
	if cfg.SchedulerEnabled {
		go worker.New(data, logger).Run(ctx)
	}
	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		logger.Info("Classing backend listening", "address", cfg.HTTPAddr, "environment", cfg.Environment, "database_driver", cfg.DatabaseDriver)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("http server stopped", "error", err)
			cancel()
		}
	}()

	<-ctx.Done()
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown", "error", err)
	}
}
