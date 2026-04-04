# Telemetry Exporter

A telemetry exporter designed for hosting providers to monitor resource usage. Currently, it supports exporting network statistics from Linux systems (`/proc/net/dev`) via NATS.

## Features

- Real-time network statistics collection (RX/TX/Total bytes).
- Robust counters handling (survives interface recreation and OS counters' resets).
- State persistence between restarts.
- NATS-based API for remote monitoring and management.
- Traffic threshold hooks: fire a JetStream event or run a local command when traffic exceeds a limit.
- Stats change events: periodic JetStream notifications listing interfaces whose traffic delta exceeds a configurable threshold.
- Low overhead and easy deployment.

## Requirements

- **Go 1.26+** (for building).
- **Linux** (for `/proc/net/dev` access).
- **NATS Server** must be running on the master/central server to receive telemetry data.

## Building

Building the project requires Go 1.26+.

### Build for the current OS:
```bash
go build -o telemetry-exporter ./cmd/exporter/main.go
```

### Build for Linux x86_64 via script:
```bash
chmod +x build.sh
./build.sh
```
This will create a binary named `telemetry-exporter-linux-x64`.

## Configuration

Configuration is done via the `config.yml` file in the project root:

```yaml
nats_url: 'nats://localhost:4222'    # NATS server address
exporter_id: 'node-01'               # Unique ID for this exporter
storage_file: 'stats.json'           # File to save accumulated statistics
hooks_file: 'hooks.json'             # File to persist hooks and their state (default: hooks.json)
collection_interval_ms: 1000         # Data collection interval in ms
init_from_system: false              # Whether to start counting from current system values
events:
  enabled: false                     # Enable periodic stats change events
  subject: 'telemetry.events.stats'  # JetStream subject to publish events to
  threshold: 1024                    # Minimum byte delta per interface to include in an event
  interval_ms: 60000                 # Minimum time between events in ms (0 = every collection tick)
```

## Usage

### Running
```bash
./telemetry-exporter
```

### Retrieving Statistics via NATS
The exporter subscribes to the following subjects, where `<id>` is the `exporter_id` from the config.

#### 1. All Network Statistics
Returns a JSON object containing all monitored network interfaces.

- **Subject:** `telemetry.<id>.stats.network`
- **Request Example:**
  ```bash
  nats request telemetry.local-node-01.stats.network ''
  ```
- **Response Example:**
  ```json
  {
    "eth0": {
      "rx": 1234567,
      "tx": 7654321,
      "total": 8888888
    },
    "lo": {
      "rx": 100,
      "tx": 100,
      "total": 200
    }
  }
  ```

#### 2. Specific Interface Statistics
Returns statistics for a specific network interface.

- **Subject:** `telemetry.<id>.stats.network.<interface>`
- **Request Example:**
  ```bash
  nats request telemetry.local-node-01.stats.network.eth0 ''
  ```
- **Response Example:**
  ```json
  {
    "rx": 1234567,
    "tx": 7654321,
    "total": 8888888
  }
  ```

#### 3. Reset Interface Counters
Resets the `rx`, `tx`, and `total` counters for the specified interface to zero.

- **Subject:** `telemetry.<id>.stats.network.<interface>.reset`
- **Request Example:**
  ```bash
  nats request telemetry.local-node-01.stats.network.eth0.reset ''
  ```
- **Response Example:**
  ```json
  {
    "status": "ok"
  }
  ```

## Stats Change Events

When `events.enabled` is `true`, the exporter publishes a JetStream message to `events.subject` containing interfaces whose accumulated delta since the last published event exceeds `events.threshold` bytes. `events.interval_ms` controls the minimum time between publishes — the event is skipped if not enough time has passed since the last one. If no interface crosses the threshold, nothing is published and the delta continues accumulating.

The delta baseline is only reset after a successful publish, so brief idle periods will not cause the threshold to be missed.

**Event payload:**
```json
{
  "exporter_id": "node-01",
  "ts": 1743800000,
  "changes": [
    {
      "interface": "eth0",
      "rx": 1073741824,
      "tx": 268435456,
      "total": 1342177280,
      "rx_delta": 204800,
      "tx_delta": 51200,
      "total_delta": 256000
    }
  ]
}
```

> **Note:** JetStream must be enabled on the NATS server and a stream must exist that covers the configured subject.

---

## Hooks

Hooks fire when accumulated traffic on an interface crosses a threshold. They are managed dynamically via NATS and persist across restarts.

### Hook fields

| Field           | Required  | Description                                                                                                                           |
|-----------------|-----------|---------------------------------------------------------------------------------------------------------------------------------------|
| `id`            | Yes       | Unique hook identifier (chosen by the client)                                                                                         |
| `interface`     | No        | Network interface to watch (empty = all interfaces)                                                                                   |
| `trigger`       | Yes       | `rx`, `tx`, or `total`                                                                                                                |
| `trigger_level` | Yes       | Threshold in bytes                                                                                                                    |
| `kind`          | Yes       | `nats` - publish a JetStream event; `command` - run a shell command                                                                   |
| `nats_subject`  | `nats`    | JetStream subject to publish to (default: `telemetry.hooks.threshold`)                                                                |
| `command`       | `command` | Shell command to execute (`/bin/sh -c`)                                                                                               |
| `once`          | No        | `true` - fire once and remove the hook; `false` - re-fire each time the threshold is crossed after a counter reset (default: `false`) |

### Add a hook

- **Subject:** `telemetry.<id>.hooks.add`
- **Request Example - JetStream event when eth0 total exceeds 100 GiB (once):**
  ```bash
  nats request telemetry.node-01.hooks.add '{
    "id": "eth0-100g",
    "interface": "eth0",
    "trigger": "total",
    "trigger_level": 107374182400,
    "kind": "nats",
    "nats_subject": "telemetry.hooks.threshold",
    "once": true
  }'
  ```
- **Request Example - run a script when any interface RX exceeds 10 GiB (repeating):**
  ```bash
  nats request telemetry.node-01.hooks.add '{
    "id": "any-rx-10g",
    "trigger": "rx",
    "trigger_level": 10737418240,
    "kind": "command",
    "command": "/usr/local/bin/notify.sh",
    "once": false
  }'
  ```
- **Response:**
  ```json
  { "id": "eth0-100g" }
  ```

The command receives context via environment variables: `EXPORTER_ID`, `HOOK_ID`, `IFACE`, `TRIGGER`, `VALUE`, `THRESHOLD`, `TS`.

For `kind: nats`, the published JetStream message will look like this:
```json
{
  "exporter_id": "node-01",
  "hook_id": "eth0-100g",
  "iface": "eth0",
  "trigger": "total",
  "value": 107374182401,
  "threshold": 107374182400,
  "ts": 1743800000
}
```

### Remove a hook

- **Subject:** `telemetry.<id>.hooks.remove`
- **Request Example:**
  ```bash
  nats request telemetry.node-01.hooks.remove '{"id": "eth0-100g"}'
  ```
- **Response:**
  ```json
  { "status": "ok" }
  ```

### List hooks

- **Subject:** `telemetry.<id>.hooks.list`
- **Request Example:**
  ```bash
  nats request telemetry.node-01.hooks.list ''
  ```
- **Response:**
  ```json
  [
    {
      "id": "eth0-100g",
      "interface": "eth0",
      "trigger": "total",
      "trigger_level": 107374182400,
      "kind": "nats",
      "nats_subject": "telemetry.hooks.threshold",
      "once": true,
      "active": false
    }
  ]
  ```
