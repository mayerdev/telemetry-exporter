package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

type Config struct {
	NATSURL              string `yaml:"nats_url"`
	ExporterID           string `yaml:"exporter_id"`
	StorageFile          string `yaml:"storage_file"`
	CollectionIntervalMS int    `yaml:"collection_interval_ms"`
	InitFromSystem       bool   `yaml:"init_from_system"`
}

type InterfaceStats struct {
	RX    uint64 `json:"rx"`
	TX    uint64 `json:"tx"`
	Total uint64 `json:"total"`
}

type GlobalStats struct {
	Interfaces map[string]*InterfaceStats `json:"interfaces"`
	LastOSRX   map[string]uint64          `json:"-"`
	LastOSTX   map[string]uint64          `json:"-"`
}

type Exporter struct {
	config Config
	nc     *nats.Conn
	mu     sync.RWMutex
	stats  GlobalStats
}

func NewExporter(cfg Config) *Exporter {
	return &Exporter{
		config: cfg,
		stats: GlobalStats{
			Interfaces: make(map[string]*InterfaceStats),
			LastOSRX:   make(map[string]uint64),
			LastOSTX:   make(map[string]uint64),
		},
	}
}

func (e *Exporter) loadStats() error {
	data, err := os.ReadFile(e.config.StorageFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	return json.Unmarshal(data, &e.stats.Interfaces)
}

func (e *Exporter) saveStats() error {
	e.mu.RLock()
	data, err := json.MarshalIndent(e.stats.Interfaces, "", "  ")
	e.mu.RUnlock()
	if err != nil {
		return err
	}

	return os.WriteFile(e.config.StorageFile, data, 0644)
}

func (e *Exporter) collect() {
	file, err := os.Open("/proc/net/dev")
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, ":") {
			continue
		}

		parts := strings.Split(line, ":")
		if len(parts) < 2 {
			continue
		}

		iface := strings.TrimSpace(parts[0])
		fields := strings.Fields(parts[1])
		if len(fields) < 9 {
			continue
		}

		rx, _ := strconv.ParseUint(fields[0], 10, 64)
		tx, _ := strconv.ParseUint(fields[8], 10, 64)

		e.mu.Lock()
		prevOSRX, okRX := e.stats.LastOSRX[iface]
		prevOSTX, okTX := e.stats.LastOSTX[iface]

		var deltaRX, deltaTX uint64
		if okRX {
			if rx >= prevOSRX {
				deltaRX = rx - prevOSRX
			} else {
				deltaRX = rx
			}
		} else {
			if _, exists := e.stats.Interfaces[iface]; !exists {
				if e.config.InitFromSystem {
					e.stats.Interfaces[iface] = &InterfaceStats{
						RX:    rx,
						TX:    tx,
						Total: rx + tx,
					}
				} else {
					e.stats.Interfaces[iface] = &InterfaceStats{}
				}
			}
		}

		if okTX {
			if tx >= prevOSTX {
				deltaTX = tx - prevOSTX
			} else {
				deltaTX = tx
			}
		}

		e.stats.Interfaces[iface].RX += deltaRX
		e.stats.Interfaces[iface].TX += deltaTX
		e.stats.Interfaces[iface].Total = e.stats.Interfaces[iface].RX + e.stats.Interfaces[iface].TX

		e.stats.LastOSRX[iface] = rx
		e.stats.LastOSTX[iface] = tx
		e.mu.Unlock()
	}
}

func (e *Exporter) Run(ctx context.Context) error {
	if err := e.loadStats(); err != nil {
		log.Error().Err(err).Msg("Failed to load stats")
	}

	nc, err := nats.Connect(e.config.NATSURL,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
			log.Warn().Err(err).Msg("NATS disconnected")
		}),
		nats.ReconnectHandler(func(nc *nats.Conn) {
			log.Info().Str("url", nc.ConnectedUrl()).Msg("NATS reconnected")
		}),
		nats.ErrorHandler(func(nc *nats.Conn, s *nats.Subscription, err error) {
			if s != nil {
				log.Error().Err(err).Str("subject", s.Subject).Msg("NATS error")
			} else {
				log.Error().Err(err).Msg("NATS error")
			}
		}),
	)
	if err != nil {
		return err
	}

	e.nc = nc
	defer e.nc.Close()

	queue := "exporter-group"
	id := e.config.ExporterID

	subjects := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{
			subject: fmt.Sprintf("telemetry.%s.stats", id),
			handler: e.handleStats,
		},
		{
			subject: fmt.Sprintf("telemetry.%s.stats.*", id),
			handler: e.handleInterfaceStats,
		},
		{
			subject: fmt.Sprintf("telemetry.%s.stats.*.reset", id),
			handler: e.handleReset,
		},
	}

	for _, s := range subjects {
		if _, err := e.nc.QueueSubscribe(s.subject, queue, s.handler); err != nil {
			return err
		}
	}

	ticker := time.NewTicker(time.Duration(e.config.CollectionIntervalMS) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			e.collect()
			if err := e.saveStats(); err != nil {
				log.Error().Err(err).Msg("Failed to save stats")
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (e *Exporter) handleStats(m *nats.Msg) {
	e.mu.RLock()
	data, _ := json.Marshal(e.stats.Interfaces)
	e.mu.RUnlock()
	m.Respond(data)
}

func (e *Exporter) handleInterfaceStats(m *nats.Msg) {
	parts := strings.Split(m.Subject, ".")
	if len(parts) != 4 {
		return
	}

	iface := parts[3]

	e.mu.RLock()
	stat, ok := e.stats.Interfaces[iface]
	var data []byte
	if ok {
		data, _ = json.Marshal(stat)
	} else {
		data = []byte("{}")
	}

	e.mu.RUnlock()
	m.Respond(data)
}

func (e *Exporter) handleReset(m *nats.Msg) {
	parts := strings.Split(m.Subject, ".")
	if len(parts) != 5 {
		return
	}

	iface := parts[3]

	e.mu.Lock()
	if s, ok := e.stats.Interfaces[iface]; ok {
		s.RX = 0
		s.TX = 0
		s.Total = 0
		delete(e.stats.LastOSRX, iface)
		delete(e.stats.LastOSTX, iface)
	}
	e.mu.Unlock()

	if err := e.saveStats(); err != nil {
		log.Error().Err(err).Str("iface", iface).Msg("Failed to save stats after reset")
	}

	m.Respond([]byte(`{"status":"ok"}`))
}

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339})

	cfgFile, err := os.ReadFile("config.yml")
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to read config")
	}

	var cfg Config
	if err := yaml.Unmarshal(cfgFile, &cfg); err != nil {
		log.Fatal().Err(err).Msg("Failed to parse config")
	}

	exporter := NewExporter(cfg)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info().Str("exporter_id", cfg.ExporterID).Msg("Starting exporter")

	if err := exporter.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("Exporter failed")
	}
}
