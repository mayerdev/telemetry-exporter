package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
	"telemetry-exporter/internal/config"
	"telemetry-exporter/internal/exporter"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	cfgFile, err := os.ReadFile("config.yml")
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to read config")
	}

	var cfg config.Config
	if err := yaml.Unmarshal(cfgFile, &cfg); err != nil {
		log.Fatal().Err(err).Msg("Failed to parse config")
	}

	exp := exporter.NewExporter(cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("exporter_id", cfg.ExporterID).Msg("Starting exporter")

	if err := exp.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("Exporter failed")
	}
}
