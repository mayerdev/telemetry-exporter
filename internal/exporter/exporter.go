package exporter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"telemetry-exporter/internal/collector"
	"telemetry-exporter/internal/config"
	"telemetry-exporter/internal/hooks"
	"telemetry-exporter/internal/stats"
	"telemetry-exporter/internal/storage"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

type Exporter struct {
	config config.Config
	nc     *nats.Conn
	js     nats.JetStreamContext
	hooks  *hooks.Manager
	mu     sync.RWMutex
	stats  stats.GlobalStats
}

func NewExporter(cfg config.Config) *Exporter {
	return &Exporter{
		config: cfg,
		stats:  stats.NewGlobalStats(),
	}
}

func (e *Exporter) Run(ctx context.Context) error {
	interfaces, err := storage.LoadStats(e.config.StorageFile)
	if err != nil {
		log.Error().Err(err).Msg("Failed to load stats")
	} else {
		e.stats.Interfaces = interfaces
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

	js, jsErr := nc.JetStream()
	if jsErr != nil {
		log.Warn().Err(jsErr).Msg("JetStream unavailable, nats hooks will not work")
	} else {
		e.js = js
	}

	hooksFile := e.config.HooksFile
	if hooksFile == "" {
		hooksFile = "hooks.json"
	}

	hm, err := hooks.NewManager(hooksFile, e.js)
	if err != nil {
		return fmt.Errorf("hooks init: %w", err)
	}

	e.hooks = hm

	queue := "exporter-group"
	id := e.config.ExporterID

	subjects := []struct {
		subject string
		handler nats.MsgHandler
	}{
		{
			subject: fmt.Sprintf("telemetry.%s.stats.network", id),
			handler: e.handleStats,
		},
		{
			subject: fmt.Sprintf("telemetry.%s.stats.network.*", id),
			handler: e.handleInterfaceStats,
		},
		{
			subject: fmt.Sprintf("telemetry.%s.stats.network.*.reset", id),
			handler: e.handleReset,
		},
		{
			subject: fmt.Sprintf("telemetry.%s.hooks.add", id),
			handler: e.handleHookAdd,
		},
		{
			subject: fmt.Sprintf("telemetry.%s.hooks.remove", id),
			handler: e.handleHookRemove,
		},
		{
			subject: fmt.Sprintf("telemetry.%s.hooks.list", id),
			handler: e.handleHookList,
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

			e.mu.RLock()
			ifaces := e.stats.Interfaces
			e.mu.RUnlock()

			if err := e.hooks.CheckAll(ctx, e.config.ExporterID, ifaces); err != nil {
				log.Error().Err(err).Msg("Hook check failed")
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (e *Exporter) collect() {
	e.mu.Lock()
	defer e.mu.Unlock()
	collector.CollectNetwork(&e.stats, e.config.InitFromSystem)
}

func (e *Exporter) saveStats() error {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return storage.SaveStats(e.config.StorageFile, e.stats.Interfaces)
}

func (e *Exporter) handleStats(m *nats.Msg) {
	e.mu.RLock()
	data, _ := json.Marshal(e.stats.Interfaces)
	e.mu.RUnlock()
	m.Respond(data)
}

func (e *Exporter) handleInterfaceStats(m *nats.Msg) {
	parts := strings.Split(m.Subject, ".")
	if len(parts) != 5 {
		return
	}

	iface := parts[4]

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
	if len(parts) != 6 {
		return
	}

	iface := parts[4]

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

func (e *Exporter) handleHookAdd(m *nats.Msg) {
	var hook hooks.Hook
	if err := json.Unmarshal(m.Data, &hook); err != nil {
		respondError(m, "invalid payload: "+err.Error())
		return
	}

	if hook.ID == "" {
		respondError(m, "id is required")
		return
	}

	if err := e.hooks.Add(&hook); err != nil {
		respondError(m, err.Error())
		return
	}

	data, _ := json.Marshal(map[string]string{"id": hook.ID})
	m.Respond(data)
}

func (e *Exporter) handleHookRemove(m *nats.Msg) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(m.Data, &req); err != nil {
		respondError(m, "invalid payload: "+err.Error())
		return
	}

	if err := e.hooks.Remove(req.ID); err != nil {
		respondError(m, err.Error())
		return
	}

	m.Respond([]byte(`{"status":"ok"}`))
}

func (e *Exporter) handleHookList(m *nats.Msg) {
	list := e.hooks.List()
	data, _ := json.Marshal(list)
	m.Respond(data)
}

func respondError(m *nats.Msg, msg string) {
	data, _ := json.Marshal(map[string]string{"error": msg})
	m.Respond(data)
}
