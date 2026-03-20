<p align="center">
  <h1 align="center">kc-hunter</h1>
  <p align="center">
    <strong>The Missing Observability Tool for Strimzi Kafka Connect</strong>
  </p>
  <p align="center">
    Diagnose memory pressure in high-density Kafka Connect clusters.<br/>
    Correlate Pod RAM, Connector Topology, and Task State into Ranked Suspects.
  </p>
  <p align="center">
    <img src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat&logo=go" alt="Go 1.25"/>
    <img src="https://img.shields.io/badge/License-MIT-green?style=flat" alt="License"/>
    <img src="https://img.shields.io/badge/Platform-Linux-FCC624?style=flat&logo=linux" alt="Linux"/>
    <img src="https://img.shields.io/badge/Read--Only-Safe%20for%20Prod-brightgreen?style=flat" alt="Read-Only"/>
  </p>
</p>

---

## Why kc-hunter?

When you run **many connectors** across a Strimzi Kafka Connect cluster, the JVM shares a single heap among all tasks. Standard monitoring tells you *which Pod is hot* — but not **which connector is responsible**.

**kc-hunter** bridges that gap. It correlates infrastructure metrics (Pod RAM, CPU) with the logical topology of Kafka Connect (Connectors, Tasks, Worker assignments) and produces a **Suspicion Score** ranking the most likely memory culprits.

> It doesn't claim per-connector memory attribution — that's impossible with a shared JVM heap. Instead, it uses **indirect evidence signals** to rank suspects by probability.

---

## Quick Start

### Build from source

```bash
git clone git@github.com:dougdalo/kc-hunter.git
cd kc-hunter
go build -o bin/kc-hunter ./cmd/kc-hunter/
```

### Static binary (recommended for bastion hosts)

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -ldflags='-s -w' -o bin/kc-hunter ./cmd/kc-hunter/
```

The resulting binary is fully static with no `glibc` dependency — copy it to any Linux x86_64 host.

### Run

```bash
# Interactive wizard (no arguments)
./bin/kc-hunter

# Direct CLI — rank suspects in a namespace
./bin/kc-hunter suspect -n kafka-prod --top 5
```

---

## Execution Modes

kc-hunter supports three transport modes for reaching the Kafka Connect REST API.

| Mode | Flag | How it works | When to use |
|------|------|-------------|-------------|
| **exec** *(default)* | *(none)* | Runs `curl`/`wget` inside pods via `kubectl exec`, hitting `localhost:8083` | Works everywhere including GKE. Requires exec permissions on pods. |
| **proxy** | `--use-proxy` | Routes through the K8s API server proxy (`/api/v1/.../pods/{pod}:8083/proxy/...`) | Bastion hosts that can reach the API server but not pod IPs. No exec permissions needed. |
| **direct** | `--connect-url <url>` | Sends HTTP requests directly to the provided URL(s) | In-cluster clients or environments where pod IPs are routable. Fastest option. |

### Mode selection logic

```
--use-proxy set?        → proxy mode
--connect-url provided? → direct mode
neither?                → exec mode (default)
```

### Examples

```bash
# Default: exec into pods (works everywhere, including GKE)
kc-hunter suspect -n kafka-prod

# Bastion host: route through API server proxy
kc-hunter suspect -n kafka-prod --use-proxy

# In-cluster or routable pod network: direct HTTP
kc-hunter suspect --connect-url http://10.0.5.12:8083
```

---

## Commands

### `suspect` — Rank Memory Culprits

The primary diagnostic command. Correlates Pod metrics with connector/task topology to produce a ranked suspect report.

```bash
kc-hunter suspect -n kafka-prod --top 5
kc-hunter suspect -n kafka-prod -o json
kc-hunter suspect -n kafka-prod --metrics prometheus --prometheus-url http://prometheus:9090
```

Works even without Prometheus — falls back to K8s + Connect REST signals.

Use `--explain` to show a detailed score breakdown per suspect, including signal weights, thresholds, and observed values.

```bash
kc-hunter suspect -n kafka-prod --explain
```

**Example output:**

```
========================================
 SUSPECT REPORT: my-cluster
========================================

  SUMMARY
  Connectors analyzed: 12
  Tasks analyzed:      28
  Failed tasks:        1
  Hottest pod:         connect-worker-3 (7.2Gi/8.0Gi 90.1%, node: node-pool-2a)
  Top suspect:         jdbc-inventory-source/task-0 (score: 75/100)

  1. jdbc-inventory-source / task-0
     Worker: 10.0.5.12:8083 (pod: connect-worker-3)
     Score:  [###############.....] 75/100
     Reasons:
       - assigned to hottest pod connect-worker-3 (90.1% mem)
       - task state: FAILED
       - high-risk type: JdbcSourceConnector
     Recommendation: investigate failure trace and restart task

  2. s3-archive-sink / task-2
     Worker: 10.0.5.12:8083 (pod: connect-worker-3)
     Score:  [#########...........] 45/100
     Reasons:
       - assigned to hottest pod connect-worker-3 (90.1% mem)
       - 8 tasks (threshold: 5)
     Recommendation: distribute tasks across more workers or reduce tasks.max
```

### `pods` — Infrastructure Overview

List Connect pods with resource usage:

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

List all connectors with state and class:

```bash
kc-hunter connectors -n kafka-prod
```

```
CONNECTOR                    TYPE    STATE     TASKS              WORKER            CLASS
jdbc-inventory-source        source  FAILED    1 FAILED           10.0.5.12:8083    JdbcSourceConnector
s3-archive-sink              sink    RUNNING   8 RUNNING          10.0.5.12:8083    S3SinkConnector
debezium-orders-cdc          source  RUNNING   3 RUNNING          10.0.5.14:8083    PostgresConnector
```

### `deep-inspect` — JVM Diagnostics

Execute `jcmd` inside pods via K8s exec to collect heap, thread, and GC data:

```bash
# Single pod
kc-hunter deep-inspect connect-worker-3 -n kafka-prod

# All pods in namespace
kc-hunter deep-inspect -n kafka-prod

# Custom container name
kc-hunter deep-inspect connect-worker-3 -c my-container
```

Returns heap summary, class histogram, thread count, GC info, and suspicious classes matching known Kafka Connect patterns.

### `inspect-worker` / `inspect-connector` — Targeted Detail

```bash
# Inspect by worker ID or pod name
kc-hunter inspect-worker 10.0.5.12:8083
kc-hunter inspect-worker connect-worker-3

# Inspect a specific connector with full task detail and error traces
kc-hunter inspect-connector jdbc-inventory-source
```

### `snapshot save` / `snapshot diff` — Incident Comparison

Capture cluster state at a point in time and compare two snapshots to see what changed.

```bash
# Save current state
kc-hunter snapshot save -O before.json -n kafka-prod

# ... time passes, incident occurs ...

kc-hunter snapshot save -O after.json -n kafka-prod

# Compare
kc-hunter snapshot diff before.json after.json
```

The diff shows added/removed/changed connectors and suspects, including score deltas and signal changes.

### Interactive Mode

Running `kc-hunter` without arguments launches a guided wizard:

1. **Action** — Suspect Report, Pod Overview, Worker Map, or Deep JVM Inspect
2. **Namespace** — Dynamically fetched from the cluster
3. **Pod** *(deep-inspect only)* — Pick a specific pod or inspect all

---

## Configuration

### Config File

All settings can be defined in a YAML file and loaded with `--config`:

```bash
kc-hunter --config config.yaml suspect -n kafka-prod
```

See [`config.example.yaml`](config.example.yaml) for a fully documented template.

**Merge priority:** defaults < config file < CLI flags.

### Global Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--config` | — | Path to config file (YAML) |
| `-n, --namespace` | *(all namespaces)* | Target namespace(s) — repeatable |
| `-l, --selector` | `strimzi.io/kind=KafkaConnect` | Label selector for Connect pods |
| `-o, --output` | `table` | Output format: `table` or `json` |
| `--timeout` | `30s` | Per-request timeout |
| `--top` | `10` | Number of top suspects to display |
| `--concurrency` | `10` | Max parallel Connect REST requests |
| `--explain` | `false` | Show detailed score breakdown per suspect |
| `--use-proxy` | `false` | Route through K8s API server proxy |
| `--connect-url` | *(auto-discovered)* | Explicit Connect REST URL(s) |
| `--connect-port` | `8083` | Kafka Connect REST API port |
| `--metrics` | `none` | Metrics source: `prometheus`, `scrape`, or `none` |
| `--prometheus-url` | — | Prometheus base URL (required when `--metrics=prometheus`) |
| `--metrics-port` | `9404` | JMX exporter metrics port |
| `--kubeconfig` | *(auto-resolved)* | Path to kubeconfig |

### Kubeconfig Resolution

| Priority | Source |
|----------|--------|
| 1 | `--kubeconfig` flag |
| 2 | `$KUBECONFIG` environment variable |
| 3 | `~/.kube/config` |
| 4 | In-cluster service account |

---

## Scoring Engine

The scoring engine evaluates 9 weighted signals per connector task. Scores are capped at 100. Multiple signals can fire simultaneously.

| Signal | Weight | Fires when |
|--------|--------|------------|
| `on_hottest_worker` | 25 | Task runs on the pod with highest memory pressure (>= 80% mem) |
| `task_failed` | 20 | Task state is `FAILED` or `UNASSIGNED` |
| `high_retry_or_errors` | 15 | Retry count > 10 or error rate > 0 |
| `high_poll_time` | 10 | Source connector poll latency > 5000ms |
| `high_put_time` | 10 | Sink connector put latency > 5000ms |
| `high_batch_size` | 10 | Average batch size > 10,000 records |
| `high_task_count` | 10 | Connector runs >= 5 tasks |
| `risky_connector_class` | 5 | Known high-memory connector type |
| `high_offset_commit` | 5 | Offset commit avg > 10,000ms |

All thresholds and weights are configurable via config file or `config.example.yaml`.

### Known Risky Connector Classes

- `JdbcSourceConnector` / `JdbcSinkConnector`
- `MySqlConnector` / `PostgresConnector` (Debezium)
- `S3SinkConnector`
- `ElasticsearchSinkConnector`
- `CamelFtpSourceConnector`
- `FileStreamSourceConnector`

Custom patterns can be added via the `scoring.riskyClasses` config. Matching is done by substring, so `"io.debezium"` matches all Debezium connectors.

---

## Safety

- **Read-Only** — Only `GET` requests against K8s API and Connect REST. Never modifies cluster state.
- **Bounded Concurrency** — Semaphore-based pool (default: 10) prevents API server or Connect REST overload.
- **Credential Reuse** — Uses your existing kubeconfig. No additional tokens or service accounts required.
- **No Dependencies** — Static binary. No agents, sidecars, or CRDs to install.

---

## Project Structure

```
kc-hunter/
├── cmd/kc-hunter/        # Entrypoint
│   └── main.go
├── internal/
│   ├── app/              # CLI commands & orchestration
│   ├── k8s/              # Pod discovery, metrics, proxy, SPDY exec
│   ├── connect/          # Connect REST client (exec, proxy, direct transports)
│   ├── scoring/          # Suspicion scoring engine
│   ├── discovery/        # Pod/worker clustering
│   ├── jvm/              # jcmd execution, heap parsing
│   ├── metrics/          # Prometheus, scrape, and noop providers
│   ├── config/           # Defaults and YAML config loading
│   ├── output/           # Table & JSON formatters (colorized terminal output)
│   └── snapshot/         # Save/load/diff cluster state snapshots
└── pkg/models/           # Shared domain types
```

---

<p align="center">
  Built for SREs and Platform Engineers managing Strimzi Kafka Connect at scale.
</p>
