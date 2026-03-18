<p align="center">
  <h1 align="center">🔍 kc-hunter</h1>
  <p align="center">
    <strong>The Missing Observability Tool for Strimzi Kafka Connect</strong>
  </p>
  <p align="center">
    Diagnose memory pressure in high-density Kafka Connect clusters.<br/>
    Correlate Pod RAM · Connector Topology · Task State → Ranked Suspects.
  </p>
  <p align="center">
    <img src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat&logo=go" alt="Go 1.25"/>
    <img src="https://img.shields.io/badge/License-MIT-green?style=flat" alt="License"/>
    <img src="https://img.shields.io/badge/Platform-Linux-FCC624?style=flat&logo=linux" alt="Linux"/>
    <img src="https://img.shields.io/badge/Read--Only-Safe%20for%20Prod-brightgreen?style=flat" alt="Read-Only"/>
  </p>
</p>

---

## 🎯 Why kc-hunter?

When you're running **800+ connectors** across a Strimzi Kafka Connect cluster, the JVM shares a single heap among all tasks. Standard monitoring tells you *which Pod is hot* — but not **which connector is responsible**.

**kc-hunter** bridges that gap. It correlates infrastructure metrics (Pod RAM, CPU) with the logical topology of Kafka Connect (Connectors, Tasks, Worker assignments) and produces a **Suspicion Score** that ranks the most likely memory culprits.

> 💡 It doesn't claim per-connector memory attribution — that's impossible with a shared JVM heap. Instead, it uses **indirect evidence signals** to rank suspects by probability.

---

## ✨ Features

| Feature | Description |
|---------|-------------|
| 🧠 **Suspicion Scoring Engine** | Weighted scoring across 9 signal types — pod placement, task failures, retry counts, connector class risk, batch sizes, and more. Scores range from 0–100 with actionable recommendations. |
| 🏰 **Bastion-Host Optimized** | Routes all traffic through the **Kubernetes API Server Proxy** by default (`--use-proxy=true`). Reach any Pod from a bastion host that can only talk to the API server — no direct Pod network (10.x.x.x) access required. |
| ⚡ **Bounded Concurrency** | Semaphore-based worker pool (default: 10 parallel requests) protects both the K8s API server and Connect REST interface from being overwhelmed during mass scans of 800+ connectors. |
| 🔬 **Deep JVM Inspection** | Execute `jcmd` diagnostics (heap info, class histogram, thread dump, GC stats) inside containers via K8s SPDY exec — no `kubectl` needed. |
| 📊 **Flexible Metrics** | Three metrics backends: Prometheus (PromQL queries), direct JMX exporter scrape (`/metrics`), or none (scoring still works with K8s + Connect REST signals alone). |
| 🛡️ **Read-Only & Safe** | Zero mutations. No POST, DELETE, or PATCH calls. Uses your existing kubeconfig credentials and TLS tunnel. Safe for production at any scale. |
| 📋 **Multiple Output Formats** | Human-readable tables (default) or structured JSON for pipeline integration. |
| 🧙 **Interactive TUI Wizard** | Run without arguments for a guided experience — action select, namespace discovery via K8s API, and pod picker. Powered by K8s API Proxy and Exec-based transport for robust connectivity in any environment. |

---

## 🚀 Installation

### Static Binary (Recommended)

Build a fully static binary with no `glibc` dependency — ideal for bastion hosts and minimal Linux distributions:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags='-s -w' -o bin/kc-hunter ./cmd/kcdiag/
```

The resulting `bin/kc-hunter` binary is self-contained and can be copied to any Linux x86_64 host.

### From Source

```bash
git clone https://github.com/dougdalo/kcdiag.git
cd kcdiag
go build -o bin/kc-hunter ./cmd/kcdiag/
```

---

## 🧙 Interactive Mode

Running `kc-hunter` **without any arguments** launches an interactive guided wizard:

```bash
sudo ./kc-hunter
```

The wizard walks you through:

1. **Action Select** — Choose between Suspect Report, Pod Overview, Worker Map, or Deep JVM Inspect.
2. **Namespace Select** — Dynamically fetches all namespaces from the cluster and presents a searchable list.
3. **Pod Select** *(Deep JVM Inspect only)* — Pick a specific Kafka Connect pod or inspect all pods at once.

This is the easiest way to get started — no flags or subcommands required. All standard CLI flags still work when you want to script or automate.

---

## 📖 Usage

### Interactive vs CLI

```bash
# Interactive — guided wizard
./kc-hunter

# CLI — direct subcommand with flags
./kc-hunter suspect -n kafka-prod --top 5
```

### `suspect` — Rank Memory Culprits

The primary command. Correlates Pod metrics with connector/task topology to produce a ranked suspect report:

```bash
# Scan default namespace
kc-hunter suspect

# Top 5 suspects in a specific namespace
kc-hunter suspect -n kafka-prod --top 5

# Multiple namespaces with JSON output
kc-hunter suspect -n kafka-prod -n kafka-staging -o json

# With Prometheus metrics for deeper signal coverage
kc-hunter suspect -n kafka-prod --metrics prometheus --prometheus-url http://prometheus:9090
```

**Example output:**

```
🔥 Hottest Pod: connect-worker-3  (Memory: 7.2Gi / 8.0Gi = 90.1%)

  #   CONNECTOR                    TASK  WORKER               POD                SCORE  REASONS
  1   jdbc-inventory-source        0     10.0.5.12:8083       connect-worker-3   75     ########### .....
      → assigned to hottest pod connect-worker-3 (90.1% mem)
      → task state: FAILED
      → risky connector class: JdbcSourceConnector
      ✅ Recommendation: investigate failure trace and restart task

  2   s3-archive-sink              2     10.0.5.12:8083       connect-worker-3   45     ######..........
      → assigned to hottest pod connect-worker-3 (90.1% mem)
      → connector has 8 tasks (high task count)
      ✅ Recommendation: check sink backpressure; tune batch.size or add sink capacity
```

### `pods` — Infrastructure Overview

List Connect pods ranked by memory pressure:

```bash
kc-hunter pods -n kafka-prod
```

```
  POD                   CLUSTER         NODE              MEM USED   MEM LIMIT   MEM%    CPU(m)  READY  RESTARTS
  connect-worker-3      my-cluster      node-pool-2a      7.2Gi      8.0Gi       90.1%   1250    true   0
  connect-worker-1      my-cluster      node-pool-1b      5.8Gi      8.0Gi       72.5%   980     true   0
  connect-worker-0      my-cluster      node-pool-1a      4.1Gi      8.0Gi       51.2%   720     true   2
```

### `workers` — Task Distribution Map

See which connectors and tasks are assigned to each worker:

```bash
kc-hunter workers -n kafka-prod
```

### `connectors` — Connector Inventory

List all connectors with state and class information:

```bash
kc-hunter connectors -n kafka-prod
```

```
  CONNECTOR                    TYPE    STATE     TASKS  WORKER            CLASS
  jdbc-inventory-source        source  FAILED    1      10.0.5.12:8083   JdbcSourceConnector
  s3-archive-sink              sink    RUNNING   8      10.0.5.12:8083   S3SinkConnector
  debezium-orders-cdc          source  RUNNING   3      10.0.5.14:8083   PostgresConnector
```

### `deep-inspect` — JVM Diagnostics

When the suspect score points to a specific pod, drill into JVM internals:

```bash
kc-hunter deep-inspect connect-worker-3
```

This executes `jcmd` inside the container via K8s SPDY exec and returns:

- **Heap Summary** — JVM heap size, used, and GC configuration
- **Class Histogram** — Top classes by instance count and byte allocation
- **Thread Count** — Active JVM threads
- **GC Info** — Garbage collector type and statistics
- **Suspicious Classes** — Classes matching Kafka Connect patterns (connect, jdbc, debezium, etc.)

```bash
# Specify a custom container name
kc-hunter deep-inspect connect-worker-3 -c my-container
```

### `inspect-worker` / `inspect-connector` — Targeted Detail

```bash
# Inspect by worker ID or pod name
kc-hunter inspect-worker 10.0.5.12:8083
kc-hunter inspect-worker connect-worker-3

# Inspect a specific connector with full task detail and error traces
kc-hunter inspect-connector jdbc-inventory-source
```

---

## ⚙️ How It Works

```
┌─────────────────────────────────────────────────────────────────────┐
│                          kc-hunter                                  │
│                                                                     │
│  ┌──────────────┐   ┌──────────────────┐    ┌────────────────────┐  │
│  │  K8s Metrics │   │  Connect REST    │    │  JMX Metrics       │  │
│  │  Server      │   │  API             │    │  (optional)        │  │
│  │              │   │                  │    │                    │  │
│  │  • Pod RAM   │   │  • Connectors    │    │  • poll_batch_avg  │  │
│  │  • Pod CPU   │   │  • Task States   │    │  • put_batch_avg   │  │
│  │  • Mem Limit │   │  • Worker IDs    │    │  • batch_size_avg  │  │
│  │              │   │  • Error Traces  │    │  • retry_count     │  │
│  └──────┬───────┘   └────────┬─────────┘    └─────────┬──────────┘  │
│         │                    │                        │             │
│         └────────────┬───────┴────────────────────────┘             │
│                      ▼                                              │
│         ┌────────────────────────┐                                  │
│         │   Scoring Engine       │                                  │
│         │                        │                                  │
│         │   9 weighted signals   │                                  │
│         │   → 0-100 score        │                                  │
│         │   → recommendations    │                                  │
│         └────────────────────────┘                                  │
└─────────────────────────────────────────────────────────────────────┘
```

### Scoring Signals

| Signal | Weight | Description |
|--------|--------|-------------|
| `on_hottest_worker` | 25 | Task runs on the pod with the highest memory usage |
| `task_failed` | 20 | Task state is `FAILED` or `UNASSIGNED` |
| `high_retry_or_errors` | 15 | Retry count or error rate exceeds threshold |
| `high_poll_time` | 10 | Source connector poll latency > 5000ms |
| `high_put_time` | 10 | Sink connector put latency > 5000ms |
| `high_batch_size` | 10 | Average batch size > 10,000 records |
| `high_task_count` | 10 | Connector runs ≥ 5 tasks |
| `risky_connector_class` | 5 | Known high-memory connector type (JDBC, Debezium, S3, etc.) |
| `high_offset_commit` | 5 | Offset commit avg time > 10,000ms |

Scores are capped at 100. Multiple signals can fire simultaneously, and the engine generates **actionable recommendations** based on which signals are active.

### Known Risky Connector Classes

The scoring engine flags these connector types with a baseline suspicion bonus:

- `JdbcSourceConnector` / `JdbcSinkConnector`
- `MySqlConnector` / `PostgresConnector` (Debezium)
- `S3SinkConnector`
- `ElasticsearchSinkConnector`
- `CamelFtpSourceConnector`
- `FileStreamSourceConnector`

---

## 🔧 Configuration

### Kubeconfig Resolution

| Priority | Source |
|----------|--------|
| 1 | `--kubeconfig` flag |
| 2 | `$KUBECONFIG` environment variable |
| 3 | `~/.kube/config` default path |
| 4 | In-cluster service account |

### Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-n, --namespace` | `""` (all) | Target namespace(s) — can be repeated |
| `-l, --selector` | `strimzi.io/kind=KafkaConnect` | Label selector for Connect pods |
| `-o, --output` | `table` | Output format: `table` or `json` |
| `--timeout` | `30s` | Per-request timeout |
| `--top` | `10` | Number of top suspects to display |
| `--concurrency` | `10` | Max parallel Connect REST requests |
| `--use-proxy` | `true` | Route through K8s API server proxy |
| `--connect-port` | `8083` | Kafka Connect REST API port |
| `--metrics` | `none` | Metrics source: `prometheus`, `scrape`, or `none` |
| `--prometheus-url` | — | Prometheus base URL |
| `--metrics-port` | `9404` | JMX exporter metrics port |

---

## 🏗️ Project Structure

```
kcdiag/
├── cmd/kcdiag/           # Application entrypoint
│   └── main.go
├── internal/
│   ├── app/              # CLI commands & orchestration
│   │   ├── root.go       # Root command, shared flags
│   │   ├── suspect.go    # 🎯 Main diagnostic command
│   │   ├── pods.go       # Pod listing
│   │   ├── workers.go    # Worker-to-task mapping
│   │   ├── connectors.go # Connector inventory
│   │   ├── inspect.go      # Single connector/worker detail
│   │   ├── deep_inspect.go # JVM heap inspection
│   │   └── interactive.go  # TUI wizard (no-args mode)
│   ├── k8s/              # Kubernetes API integration
│   │   └── client.go     # Pod discovery, metrics, proxy, SPDY exec
│   ├── connect/          # Kafka Connect REST client
│   │   └── client.go     # Direct HTTP & API proxy transports
│   ├── scoring/          # Suspicion scoring engine
│   │   └── engine.go     # Signal weighting & suspect ranking
│   ├── discovery/        # Pod/worker clustering
│   │   └── discovery.go  # Cluster grouping, worker mapping
│   ├── jvm/              # JVM diagnostic utilities
│   │   └── inspector.go  # jcmd execution, heap parsing
│   ├── metrics/          # Connector metrics providers
│   │   ├── provider.go   # Provider interface
│   │   ├── prometheus.go # Prometheus PromQL backend
│   │   ├── scrape.go     # Direct /metrics scrape
│   │   └── noop.go       # Graceful no-op fallback
│   ├── config/           # Configuration management
│   │   └── config.go     # Defaults, YAML loading, flag binding
│   └── output/           # Result formatting
│       └── formatter.go  # Table & JSON renderers
└── pkg/models/           # Shared domain types
    └── models.go         # PodInfo, ConnectorInfo, SuspectReport, etc.
```

---

## 🛡️ Safety Guarantees

- **Read-Only** — kc-hunter performs only `GET` requests against the Kubernetes API and Connect REST interface. It will never modify your cluster state.
- **Bounded Concurrency** — All requests are throttled through a semaphore pool. Default of 10 parallel requests prevents API server or Connect REST overload.
- **Credential Reuse** — Authenticates using your existing kubeconfig. No additional credentials, tokens, or service accounts required.
- **No External Dependencies** — Static binary with zero runtime dependencies. No agents, sidecars, or CRDs to install.

---

## 📌 Quick Reference

```bash
# Build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o bin/kc-hunter ./cmd/kcdiag/

# Diagnose — who's eating the heap?
kc-hunter suspect -n kafka-prod --top 10

# Which pods are under pressure?
kc-hunter pods -n kafka-prod

# What's running where?
kc-hunter workers -n kafka-prod

# List all connectors
kc-hunter connectors -n kafka-prod

# Deep JVM inspection on a hot pod
kc-hunter deep-inspect connect-worker-3

# Full detail on a suspect connector
kc-hunter inspect-connector jdbc-inventory-source

# JSON output for automation
kc-hunter suspect -n kafka-prod -o json | jq '.suspects[:5]'
```

---

<p align="center">
  Built for SREs and Platform Engineers managing Strimzi Kafka Connect at scale.<br/>
</p>
