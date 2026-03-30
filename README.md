# Transporter

Transporter is a Kubernetes-native solution for live pod migration using the **On-Demand Sidecar** strategy. It enables moving running pods between nodes with minimal downtime while preserving application state.

## Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [On-Demand Sidecar Strategy](#on-demand-sidecar-strategy)
- [Installation](#installation)
- [Transporter CLI](#transporter-cli)
- [Usage Examples](#usage-examples)
- [Technical Details](#technical-details)
- [Development](#development)

---

## Overview

Transporter implements **live pod migration** by:

1. Dynamically injecting a **transporter-proxy sidecar** into a Ghost Pod on the target node
2. Using **iptables REDIRECT** on the source node to intercept traffic
3. **Buffering TCP connections** during the migration window
4. Performing a **handover** to resume the application with buffered data

### Key Features

- **Zero-downtime migration**: Active connections are buffered and handed over seamlessly
- **Kubernetes Native**: Uses CRDs and operator pattern
- **On-Demand Sidecars**: Only injects sidecar during migration, not on all pods
- **No IP changes**: Application retains its network identity (with compatible CNI)
- **State preservation**: Uses CRIU for checkpoint/restore with filesystem overlay capture

---

## Architecture

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           Transporter Architecture                           │
└─────────────────────────────────────────────────────────────────────────────┘

  ┌──────────┐                           ┌──────────────┐
  │  CLI     │                           │   Controller │
  │(transporter)                          │  (Operator)  │
  └────┬─────┘                           └──────┬───────┘
       │                                        │
       │ Create PodMigration                    │ Reconcile
       │───────────────────────────────────────>│ gRPC
       │                                        │
       │              ┌──────────────────────────┤
       │              │                          │
       ▼              ▼                          ▼
┌──────────────────┐         ┌─────────────────────────────────────────┐
│  Source Node     │         │          Target Node                     │
│  (vlab-03)      │         │          (vlab-02)                        │
├──────────────────┤         ├─────────────────────────────────────────┤
│                  │         │                                          │
│  ┌────────────┐  │         │  ┌────────────────────────────────────┐  │
│  │  Source    │  │  TCP    │  │         Ghost Pod                 │  │
│  │  Pod       │──┼─────────┼─>│  ┌──────────┐ ┌────────────────┐  │  │
│  │ (app)      │  │ Forward │   │  │ transporter│ │    App         │  │  │
│  └────────────┘  │         │   │  │-proxy     │ │  (nginx)       │  │  │
│                  │         │   │  │ (buffer)  │ └────────────────┘  │  │
│  ┌────────────┐  │         │   │  │ sidecar   │                     │  │
│  │migration-  │  │         │   │  └──────────┘                     │  │
│  │agent       │  │         │   │                                     │  │
│  │(tap mode)  │  │         │   │  ┌────────────────────────────────┐  │  │
│  └────────────┘  │         │   │  │migration-agent                │  │  │
│                  │         │   │  │(gRPC server)                   │  │  │
│  iptables       │         │   │  └────────────────────────────────┘  │  │
│  REDIRECT       │         │   │                                          │
└──────────────────┘         └──────────────────────────────────────────────┘
```

### Components

| Component | Description |
|-----------|-------------|
| **Transporter CLI** | User command-line tool to initiate and manage migrations |
| **PodMigration Controller** | Kubernetes operator that orchestrates the migration lifecycle |
| **Migration Agent** | DaemonSet running on each node - performs checkpoint/restore via CRIU |
| **Transporter Proxy Sidecar** | Injected into Ghost Pod - buffers TCP connections during migration |

---

## On-Demand Sidecar Strategy

This is the core innovation of Transporter. Instead of running a sidecar on every pod (which adds overhead), we dynamically inject one only when needed.

### The Strategy

#### 1. Dynamic Injection (Syncing Phase)

When a `PodMigration` is created, the controller:

1. Creates a **Ghost Pod** on the target node with the same spec as the source
2. Injects a `transporter-proxy` sidecar container
3. Sets `shareProcessNamespace: true` so the sidecar can access app's sockets

```yaml
# Ghost Pod spec (simplified)
spec:
  nodeName: vlab-02
  shareProcessNamespace: true
  containers:
  - name: app
    image: nginx:latest
    ports:
    - containerPort: 80
  - name: transporter-proxy
    image: 192.168.1.20:5000/transporter-proxy:latest
    env:
    - name: MODE
      value: "buffer"
    - name: APP_PORT
      value: "80"
    ports:
    - containerPort: 50052  # Proxy port
    - containerPort: 50053  # Management port
```

#### 2. Source Node "Tap" (Intercepting)

The migration agent on the source node sets up **iptables REDIRECT** to intercept TCP traffic:

```bash
# Intercept all traffic destined for the source pod
iptables -t nat -A PREROUTING -p tcp -d 10.10.2.155 -j REDIRECT --to-port 50052
iptables -t nat -A OUTPUT -p tcp -d 10.10.2.155 -j REDIRECT --to-port 50052
```

Traffic is forwarded to the Ghost Sidecar on the target node via the node's internal IP.

#### 3. Ghost "Buffer" Mode

The sidecar operates in **buffer mode**:

1. **Accepts** incoming connections from the tap
2. **Buffers** all TCP data in memory
3. **Forwards** buffered data to the app when it starts

#### 4. Signal Handover

After CRIU restore completes, the controller signals the sidecar:

```
POST http://ghost-pod:50053/handover
```

The sidecar then:
1. **Closes** the old app connection
2. **Reconnects** to the app on 127.0.0.1
3. **Flushes** buffered data to the new connection
4. **Switches** to pass-through mode

#### 5. Transparentize (Cleanup)

After the buffer is flushed, call:

```
POST http://ghost-pod:50053/transparentize
```

The sidecar either:
- Acts as a simple pass-through proxy
- Exits (if the app is now handling connections directly)

---

## Installation

### Prerequisites

- Kubernetes cluster (v1.20+)
- containerd as container runtime
- CRIU installed on all nodes
- iptables available on all nodes
- Helm 3.x

### Deploy via Helm

```bash
# Install the transporter operator
helm install transporter ./helm-chart

# Or with custom values
helm install transporter ./helm-chart --set controller.replicaCount=2
```

### Verify Installation

```bash
# Check controller and agents are running
kubectl get pods -n transporter

# Expected output:
# NAME                          READY   STATUS
# transporter-controller-xxx    1/1     Running
# transporter-agent-xxx         1/1     Running
```

---

## Transporter CLI

The `transporter` CLI provides commands to manage pod migrations.

### Global Flags

```bash
--kubeconfig string   Path to kubeconfig (defaults to KUBECONFIG or ~/.kube/config)
-n, --namespace string  Kubernetes namespace (default "default")
```

### Commands

#### migrate

Start a pod migration by creating a PodMigration CR.

```bash
transporter migrate <pod-name> [flags]
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--target-node` | `-t` | required | Target node name for migration |
| `--strategy` | | "live" | Migration strategy (live or cold) |

**Examples:**

```bash
# Migrate pod 'web-0' from current node to 'node-02'
transporter migrate web-0 -n production -t node-02

# Migrate using specific kubeconfig
transporter migrate myapp -n default -t worker-1 --kubeconfig ~/.kube/config

# Check migration status immediately after
transporter status mig-abc123 -n production
```

#### status

Show the status of a PodMigration.

```bash
transporter status <migration-id> [flags]
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | global namespace | PodMigration namespace |

**Examples:**

```bash
# Check status in default namespace
transporter status mig-abc123

# Check status in specific namespace
transporter status mig-xyz -n production

# Watch status continuously
watch transporter status mig-abc123 -n production
```

#### list

List all PodMigration resources.

```bash
transporter list [flags]
```

**Flags:**

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--namespace` | `-n` | global namespace | Namespace to list from |

**Examples:**

```bash
# List all migrations in default namespace
transporter list

# List migrations in production namespace
transporter list -n production

# Watch for new migrations
watch transporter list -n production
```

---

## Usage Examples

### Basic Migration Workflow

```bash
# 1. Check pods available for migration
kubectl get pods -n production

# Output:
# NAME    READY   STATUS    NODE
# web-0   1/1     Running   node-01
# web-1   1/1     Running   node-02

# 2. Start migration to target node
transporter migrate web-0 -n production -t node-02

# Output:
# Migration request created
# Migration ID: mig-xyz123

# 3. Monitor migration status
transporter status mig-xyz123 -n production

# Output:
# +-----------+--------------------------------------+
# |  PHASE    | MESSAGE                              |
# +-----------+--------------------------------------+
# | Completed | Successful                            |
# +-----------+--------------------------------------+

# 4. Verify pod is now on target node
kubectl get pod web-0 -n production -o wide

# Output:
# NAME    READY   NODE
# web-0   2/2     node-02   # Note: 2 containers (app + sidecar)
```

### Migration with kubectl

You can also create migrations directly with kubectl:

```bash
cat <<EOF | kubectl apply -f -
apiVersion: migration.transporter.io/v1alpha1
kind: PodMigration
metadata:
  name: web-migration
  namespace: production
spec:
  podName: web-0
  namespace: production
  targetNode: node-02
EOF
```

### Reverse Migration

```bash
# Migrate back to original node
transporter migrate web-0 -n production -t node-01
```

---

## Technical Details

### Migration Phases

```
┌─────────────────────────────────────────────────────────────────┐
│                    Migration State Machine                       │
└─────────────────────────────────────────────────────────────────┘

    ┌──────────┐
    │ Pending  │  Initial state, controller discovers source pod
    └────┬─────┘
         │
         ▼
    ┌──────────┐  Create Ghost Pod with sidecar, wait for running
    │ Syncing  │<─────────────────────────────────────────────┐
    └────┬─────┘                                              │
         │                                                    │
         ▼                                                    │
    ┌──────────┐  Start tap, capture, inject, restore        │
    │Finalizing│ ────────────────────────────────────────────►│
    └────┬─────┘                                              │
         │                                                    │
         ▼                                                    │
    ┌──────────┐  Create final pod, delete ghost              │
    │Completed │                                              │
    └──────────┘                                              │
                                                             │
    ┌──────────┐  On error                                    │
    │ Failed   │ ─────────────────────────────────────────────┘
    └──────────┘
```

### Environment Variables (Controller)

The PodMigration controller accepts these environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `TRANSPORTER_SIDECAR_IMAGE` | "transporter-proxy:latest" | Sidecar image to inject into Ghost Pods |

**Via Helm:**
```yaml
# values.yaml
sidecar:
  image: "my-registry.com/transporter-proxy:v1.0.0"
```

Or via command line:
```bash
helm install transporter ./helm-chart --set sidecar.image=my-registry.com/transporter-proxy:v1.0.0
```

### Environment Variables (Sidecar)

The transporter-proxy sidecar uses these environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `MODE` | "buffer" | Operation mode: buffer, tap, passthrough |
| `TARGET_IP` | "" | Target node IP (for buffer mode) |
| `APP_PORT` | 80 | Application port |
| `MANAGEMENT_PORT` | 50053 | HTTP management server port |
| `BUFFER_SIZE` | 65536 | Max buffer size in bytes |

### Management Endpoints (Sidecar)

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/ready` | GET | Health check |
| `/stats` | GET | Buffer statistics |
| `/handover` | POST | Signal handover to app |
| `/transparentize` | POST | Switch to pass-through mode |

### gRPC API

The migration-agent exposes these RPC methods:

```protobuf
service Migration {
  rpc Prepare(PrepareRequest) returns (PrepareResponse) {}
  rpc StartMigration(StartMigrationRequest) returns (StartMigrationResponse) {}
  rpc ApplyLayer(ApplyLayerRequest) returns (ApplyLayerResponse) {}
  rpc SignalHandover(SignalHandoverRequest) returns (SignalHandoverResponse) {}
  rpc StartTap(StartTapRequest) returns (StartTapResponse) {}
  rpc StopTap(StopTapRequest) returns (StopTapResponse) {}
  rpc TransferFilesystem(stream FileChunk) returns (TransferResponse) {}
}
```

### File Structure

```
transporter/
├── api/                          # CRD definitions
│   └── v1alpha1/
│       └── podmigration_types.go
├── cmd/
│   ├── transporter/             # CLI tool
│   │   └── main.go
│   ├── transporter-proxy/       # Sidecar container
│   │   └── main.go
│   └── transporter-ebpf/       # eBPF utilities (optional)
├── controller/                   # Kubernetes controller
│   ├── podmigration_controller.go
│   └── Dockerfile
├── migration-agent/             # Node agent
│   ├── main.go
│   └── Dockerfile
├── pkg/
│   ├── agent/api/              # gRPC definitions
│   │   └── migration.proto
│   └── sidecar/               # Sidecar injection logic
│       └── injector.go
├── helm-chart/                  # Kubernetes deployment
└── Makefile                    # Build automation
```

### CRIU Considerations

Transporter uses CRIU for checkpoint/restore. Key considerations:

1. **CRIU Requirements**: The node must have CRIU installed
2. **Cgroup Handling**: Uses `--manage-cgroups=none` for compatibility
3. **Network**: Uses `--tcp-established` for existing connections
4. **Filesystem**: Captures overlay upperdir as tar archive

### Limitations

- **Same container runtime**: Source and target must both use containerd
- **Compatible CNI**: For IP preservation, CNI must support cluster-wide IPAM
- **Process namespace**: Requires `shareProcessNamespace: true`
- **Privileged operations**: Agent requires NET_ADMIN capabilities

---

## Development

### Building from Source

```bash
# Clone the repository
git clone https://github.com/your-repo/transporter.git
cd transporter

# Build all components
make build

# Build specific component
go build -o transporter ./cmd/transporter
go build -o controller ./controller
go build -o migration-agent ./migration-agent
```

### Building Docker Images

```bash
# Build and push all images
docker build -t 192.168.1.20:5000/controller:latest -f Dockerfile.controller .
docker build -t 192.168.1.20:5000/migration-agent:latest -f migration-agent/Dockerfile .
docker build -t 192.168.1.20:5000/transporter-proxy:latest -f cmd/transporter-proxy/Dockerfile .

# Push to registry
docker push 192.168.1.20:5000/controller:latest
docker push 192.168.1.20:5000/migration-agent:latest
docker push 192.168.1.20:5000/transporter-proxy:latest
```

### Running Tests

```bash
# Deploy and test
helm upgrade --install transporter ./helm-chart

# Run a migration
transporter migrate nginx -n default -t node-02
```

---

## Troubleshooting

### Common Issues

**Migration Stuck in Syncing**
- Check if ghost pod is running: `kubectl get pod <pod>-ghost -n <ns>`
- Check controller logs: `kubectl logs -n transporter deploy/transporter-controller`

**CRIU Restore Fails**
- Ensure CRIU is installed: `criu --version`
- Check for port conflicts on target node

**Connection Dropped**
- Verify iptables rules: `iptables -t nat -L -n`
- Check sidecar logs: `kubectl logs <pod> transporter-proxy`

### Debug Mode

```bash
# Enable debug logging in controller
kubectl set env deploy/transporter-controller -n transporter DEBUG=true

# Check agent logs
kubectl logs -n transporter ds/transporter-migration-agent
```

---

## License

Apache License 2.0 - See LICENSE file for details
