# Telemetry Exporter

A telemetry exporter designed for hosting providers to monitor resource usage. Currently, it supports exporting network statistics from Linux systems (`/proc/net/dev`) via NATS.

## Features

- Real-time network statistics collection (RX/TX/Total bytes).
- Robust counter handling (survives interface recreation and OS counter resets).
- State persistence between restarts.
- NATS-based API for remote monitoring and management.
- Low overhead and easy deployment.

## Requirements

- **Go 1.26+** (for building).
- **Linux** (for `/proc/net/dev` access).
- **NATS Server** must be running on the master/central server to receive telemetry data.

## Building

Building the project requires Go 1.26+.

### Build for current OS:
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
collection_interval_ms: 1000         # Data collection interval in ms
init_from_system: false              # Whether to start counting from current system values
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

## TODO

- **Traffic Volume Hooks:** Automatically send a message to a NATS JetStream topic when an interface's traffic exceeds a defined threshold.
- **Action Hooks:** Trigger execution of a local command when traffic reaches a certain limit.
- **Hook Persistence:** Support for both one-time (single trigger) and persistent hooks.
