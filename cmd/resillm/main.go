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
	validateOnly := flag.Bool("validate", false, "Validate configuration and exit")
	flag.Parse()

	// Setup initial logging (will be reconfigured after config load)
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnix
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	// Load configuration
	cfg, err := config.Load(*configPath)
	if err != nil {
		if *validateOnly {
			fmt.Fprintf(os.Stderr, "Configuration INVALID: %v\n", err)
			os.Exit(1)
		}
		log.Fatal().Err(err).Msg("Failed to load configuration")
	}

	// Validate-only mode: print config summary and exit
	if *validateOnly {
		fmt.Println("Configuration valid!")
		fmt.Println()
		fmt.Printf("  Config file:  %s\n", *configPath)
		fmt.Printf("  Server:       %s:%d\n", cfg.Server.Host, cfg.Server.Port)
		fmt.Printf("  Metrics port: %d\n", cfg.Server.MetricsPort)
		fmt.Printf("  Providers:    %d configured\n", len(cfg.Providers))
		for name := range cfg.Providers {
			fmt.Printf("                - %s\n", name)
		}
		fmt.Printf("  Models:       %d configured\n", len(cfg.Models))
		for name, model := range cfg.Models {
			fmt.Printf("                - %s (primary: %s/%s)\n", name, model.Primary.Provider, model.Primary.Model)
		}
		fmt.Printf("  Budget:       %s\n", formatEnabled(cfg.Budget.Enabled))
		if cfg.Budget.Enabled {
			fmt.Printf("                - Max/hour: $%.2f\n", cfg.Budget.MaxCostPerHour)
			fmt.Printf("                - Max/day:  $%.2f\n", cfg.Budget.MaxCostPerDay)
		}
		fmt.Printf("  Metrics:      %s\n", formatEnabled(cfg.Metrics.Enabled))
		fmt.Printf("  Rate limit:   %s\n", formatEnabled(cfg.Server.RateLimit.Enabled))
		fmt.Printf("  Load shedding: %s\n", formatEnabled(cfg.Server.LoadShedding.Enabled))
		os.Exit(0)
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

// formatEnabled returns "enabled" or "disabled" based on the boolean value
func formatEnabled(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}
