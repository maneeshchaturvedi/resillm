package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/resillm/resillm/internal/config"
	"github.com/resillm/resillm/internal/server"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Parse flags
	configPath := flag.String("config", "config.yaml", "Path to configuration file")
	flag.Parse()

	// Setup initial logging (will be reconfigured after config load)
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Add immediate feedback that doesn't depend on logger
	fmt.Fprintf(os.Stderr, "resillm starting with config: %s\n", *configPath)

	// Parse log level
	level, err := zerolog.ParseLevel(cfg.Logging.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}

	// Set global level first - this is the proper way to control level filtering
	zerolog.SetGlobalLevel(level)

	// Configure log format based on config (don't chain .Level() - use global level instead)
	if cfg.Logging.Format == "json" {
		log.Logger = zerolog.New(os.Stderr).With().Timestamp().Logger()
	} else {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	log.Debug().
		Str("level", cfg.Logging.Level).
		Str("format", cfg.Logging.Format).
		Bool("log_requests", cfg.Logging.LogRequests).
		Bool("log_responses", cfg.Logging.LogResponses).
		Msg("Logging configured")

	log.Info().
		Str("config", *configPath).
		Int("port", cfg.Server.Port).
		Msg("Starting resillm proxy")

	// Create and start server
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create server")
	}

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Info().Msg("Shutting down...")
		cancel()
	}()

	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("Server error")
	}
}
