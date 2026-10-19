package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/PDK1744/gams/internal/config"
	"github.com/PDK1744/gams/internal/db"
	"github.com/PDK1744/gams/internal/server"
)

var (
	// Version is populated at build time via -ldflags="-X 'main.Version=...'"
	Version = "0.1.0"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	if len(args) > 0 {
		switch args[0] {
		case "version", "--version", "-v":
			fmt.Printf("gams version %s\n", Version)
			return nil
		case "help", "--help", "-h":
			printUsage()
			return nil
		case "serve":
			args = args[1:]
		}
	}

	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	configPath := fs.String("config", "gams.toml", "path to configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("loading configuration: %w", err)
	}

	if cfg.Server.AdminKey == "" {
		logger.Warn("server.admin_key is not set; running with insecure admin credentials (acceptable for dev)")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	database, err := db.Open(ctx, cfg.Data.Path)
	if err != nil {
		return fmt.Errorf("initializing database: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Error("error closing database", "error", err)
		}
	}()

	srv := server.New(cfg, database, Version, logger)

	// Listen for OS shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	errChan := make(chan error, 1)
	go func() {
		if err := srv.Start(); err != nil {
			errChan <- err
		}
	}()

	select {
	case sig := <-sigChan:
		logger.Info("received termination signal", "signal", sig.String())
	case err := <-errChan:
		return fmt.Errorf("server error: %w", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("server shutdown error: %w", err)
	}

	logger.Info("GAMS server stopped cleanly")
	return nil
}

func printUsage() {
	fmt.Print(`Governed Agent Memory Server (GAMS) - Persistent Memory Infrastructure for AI Agents

Usage:
  gams [command] [options]

Commands:
  serve       Start the GAMS server (default)
  version     Show version information
  help        Show this help message

Options for 'serve':
  --config    Path to TOML configuration file (default: gams.toml)
`)
}
