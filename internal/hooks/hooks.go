package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"

	"telemetry-exporter/internal/stats"

	"github.com/nats-io/nats.go"
	"github.com/rs/zerolog/log"
)

type Hook struct {
	ID           string `json:"id"`
	Interface    string `json:"interface"`     // empty = all interfaces
	Trigger      string `json:"trigger"`       // "rx" | "tx" | "total"
	TriggerLevel uint64 `json:"trigger_level"` // bytes
	Kind         string `json:"kind"`          // "nats" | "command"
	NATSSubject  string `json:"nats_subject"`  // used when Kind == "nats"
	Command      string `json:"command"`       // used when Kind == "command"
	Once         bool   `json:"once"`

	// Runtime state
	Active bool `json:"active"`
}

type hooksFile struct {
	Hooks map[string]*Hook `json:"hooks"`
}

type HookEvent struct {
	ExporterID string `json:"exporter_id"`
	HookID     string `json:"hook_id"`
	Interface  string `json:"iface"`
	Trigger    string `json:"trigger"`
	Value      uint64 `json:"value"`
	Threshold  uint64 `json:"threshold"`
	Timestamp  int64  `json:"ts"`
}

type Manager struct {
	file  string
	hooks map[string]*Hook
	js    nats.JetStreamContext // nil if JetStream is unavailable
	mutex sync.Mutex
}

func NewManager(file string, js nats.JetStreamContext) (*Manager, error) {
	m := &Manager{
		file:  file,
		hooks: make(map[string]*Hook),
		js:    js,
	}

	if err := m.load(); err != nil {
		return nil, err
	}

	return m, nil
}

func (mgr *Manager) load() error {
	data, err := os.ReadFile(mgr.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}

		return err
	}

	var file hooksFile
	if err := json.Unmarshal(data, &file); err != nil {
		return fmt.Errorf("parse hooks file: %w", err)
	}

	if file.Hooks != nil {
		mgr.hooks = file.Hooks
	}

	return nil
}

func (mgr *Manager) save() error {
	data, err := json.MarshalIndent(hooksFile{Hooks: mgr.hooks}, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(mgr.file, data, 0644)
}

func (mgr *Manager) Add(hook *Hook) error {
	mgr.mutex.Lock()
	defer mgr.mutex.Unlock()

	if _, exists := mgr.hooks[hook.ID]; exists {
		return fmt.Errorf("hook %q already exists", hook.ID)
	}

	mgr.hooks[hook.ID] = hook
	return mgr.save()
}

func (mgr *Manager) Remove(id string) error {
	mgr.mutex.Lock()
	defer mgr.mutex.Unlock()

	if _, exists := mgr.hooks[id]; !exists {
		return fmt.Errorf("hook %q not found", id)
	}

	delete(mgr.hooks, id)
	return mgr.save()
}

func (mgr *Manager) List() []*Hook {
	mgr.mutex.Lock()
	defer mgr.mutex.Unlock()

	out := make([]*Hook, 0, len(mgr.hooks))
	for _, hook := range mgr.hooks {
		out = append(out, hook)
	}

	return out
}

func (mgr *Manager) CheckAll(ctx context.Context, exporterID string, interfaces map[string]*stats.InterfaceStats) error {
	mgr.mutex.Lock()
	defer mgr.mutex.Unlock()

	dirty := false
	var toDelete []string

	for _, hook := range mgr.hooks {
		var ifaceList []string
		if hook.Interface != "" {
			ifaceList = []string{hook.Interface}
		} else {
			for name := range interfaces {
				ifaceList = append(ifaceList, name)
			}
		}

		fired := false
		for _, iface := range ifaceList {
			st, ok := interfaces[iface]
			if !ok {
				continue
			}

			var value uint64
			switch hook.Trigger {
			case "rx":
				value = st.RX
			case "tx":
				value = st.TX
			default:
				value = st.Total
			}

			above := value >= hook.TriggerLevel

			if hook.Once {
				if above && !fired {
					mgr.fire(ctx, hook, exporterID, iface, value)
					fired = true
				}
			} else {
				if above && !hook.Active {
					mgr.fire(ctx, hook, exporterID, iface, value)
					hook.Active = true
					dirty = true
				} else if !above && hook.Active {
					hook.Active = false
					dirty = true
				}
			}
		}

		if fired {
			toDelete = append(toDelete, hook.ID)
			dirty = true
		}
	}

	for _, id := range toDelete {
		delete(mgr.hooks, id)
	}

	if dirty {
		if err := mgr.save(); err != nil {
			log.Error().Err(err).Msg("Failed to save hook states")
			return err
		}
	}

	return nil
}

func (mgr *Manager) fire(ctx context.Context, hook *Hook, exporterID, iface string, value uint64) {
	switch hook.Kind {
	case "nats":
		if mgr.js == nil {
			log.Warn().Str("hook_id", hook.ID).Msg("JetStream unavailable, skipping nats hook")
			return
		}

		event := HookEvent{
			ExporterID: exporterID,
			HookID:     hook.ID,
			Interface:  iface,
			Trigger:    hook.Trigger,
			Value:      value,
			Threshold:  hook.TriggerLevel,
			Timestamp:  time.Now().Unix(),
		}

		data, err := json.Marshal(event)
		if err != nil {
			log.Error().Err(err).Str("hook_id", hook.ID).Msg("Failed to marshal hook event")
			return
		}

		subject := hook.NATSSubject
		if subject == "" {
			subject = "telemetry.hooks.threshold"
		}

		if _, err := mgr.js.Publish(subject, data); err != nil {
			log.Error().Err(err).Str("hook_id", hook.ID).Str("subject", subject).Msg("Failed to publish hook event")
		}
	case "command":
		go mgr.fireCommand(ctx, hook, exporterID, iface, value)
	default:
		log.Warn().Str("hook_id", hook.ID).Str("kind", hook.Kind).Msg("Unknown hook kind")
	}
}

func (mgr *Manager) fireCommand(ctx context.Context, hook *Hook, exporterID, iface string, value uint64) {
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", hook.Command)
	cmd.Env = append(os.Environ(),
		"EXPORTER_ID="+exporterID,
		"IFACE="+iface,
		"TRIGGER="+hook.Trigger,
		"VALUE="+strconv.FormatUint(value, 10),
		"THRESHOLD="+strconv.FormatUint(hook.TriggerLevel, 10),
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			log.Warn().Str("hook_id", hook.ID).Str("stderr", stderr.String()).Msg("Hook command stderr")
		}

		log.Error().Err(err).Str("hook_id", hook.ID).Str("command", hook.Command).Msg("Hook command failed")
	}
}
