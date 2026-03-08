Here’s what I fixed and how it maps to your original prompts.
Phase A – CRD (“Source of Truth”)
Files:
api/v1alpha1/groupversion_info.go
api/v1alpha1/podmigration_types.go
What’s implemented:
CRD type PodMigration with controller-gen style annotations:
  //+kubebuilder:object:root=true  //+kubebuilder:subresource:status  //+kubebuilder:resource:path=podmigrations,scope=Namespaced,shortName=pm
Spec fields (exactly as you asked):
PodName string
Namespace string
SourceNode string (optional; controller can fill)
TargetNode string
Strategy string ("live" / "cold")
Status fields:
MigrationID string
Phase PodMigrationPhase (string; Pending, Syncing, Finalizing, Completed, Failed)
StartTime *metav1.Time
Message string
GroupVersion = migration.transporter.io/v1alpha1 and a SchemeBuilder/AddToScheme function, plus minimal DeepCopyObject methods so the types satisfy runtime.Object.
This is now a proper Kubebuilder-style API package; you can generate a CRD YAML later with controller-gen if you want.
Phase B – CLI (“transporter” tool)
Files:
cmd/transporter/main.go
go.mod (updated)
Makefile (build target updated)
Key changes:
New Cobra-based CLI at cmd/transporter/main.go. Root command transporter with subcommands:
transporter migrate <pod-name> -n <namespace> -t <target-node> [--strategy live|cold]
transporter status <migration-id> [-n <namespace>]
transporter list [-n <namespace>]
Kubeconfig handling:
--kubeconfig flag
Falls back to $KUBECONFIG or ~/.kube/config
Migrate command (Phase B behavior):
Verifies pod existence via client-go:
    clientset.CoreV1().Pods(namespace).Get(...)
Uses dynamic client to create a PodMigration CR:
    GVR: Group= migration.transporter.io, Version= v1alpha1, Resource= podmigrations    metadata.generateName = "mig-"    metadata.namespace    = <namespace>    spec:      podName, namespace, targetNode, strategy
Immediately prints the Migration ID (CR name) and exits.
Status command:
Fetches PodMigration via dynamic client.
Reads .status.phase and .status.message with unstructured.NestedString.
Prints a simple table:
    +-----------+--------------------------------------+    |  PHASE    | MESSAGE                              |    +-----------+--------------------------------------+    | Pending   | ...                                  |    +-----------+--------------------------------------+
List command:
Lists PodMigration objects in the given namespace.
Prints ID, Namespace, Phase for each.
Build wiring:
Makefile now builds CLI correctly:
    build:        @echo "Building main transporter binary..."        go build -o transporter ./cmd/transporter        @echo "Main transporter binary built successfully"
Dependencies:
go.mod now uses Go 1.21 and k8s stack that works on your current toolchain:
    go 1.21    require (        k8s.io/apimachinery v0.28.0        k8s.io/client-go v0.28.0        sigs.k8s.io/controller-runtime v0.15.0        github.com/spf13/cobra v1.8.0    )
Everything under the root module now builds cleanly: go build ./... succeeds.
Phase C – Controller (“Brain”)
Files:
controller/go.mod
controller/podmigration_controller.go
controller/main.go
What’s implemented and fixed:
Module wiring
controller/go.mod:
     module controller     go 1.21     require (         k8s.io/apimachinery v0.28.0         k8s.io/client-go v0.28.0         sigs.k8s.io/controller-runtime v0.15.0         transporter v0.0.0     )     replace transporter => ..
This lets the controller import your CRD types from the root module:
     import migrationv1alpha1 "transporter/api/v1alpha1"
Reconciler (PodMigrationReconciler)
In controller/podmigration_controller.go:
     type PodMigrationReconciler struct {         client.Client         Scheme *runtime.Scheme     }     // RBAC:     // +kubebuilder:rbac:groups=migration.transporter.io,resources=podmigrations,...
Reconcile:
Fetches PodMigration by name.
Logs basic info.
Simulates migration by setting:
       podMigration.Status.Phase = "Completed"       podMigration.Status.Message = "Pod migration completed successfully"       r.Status().Update(ctx, podMigration)
This is the placeholder where you will later:
Resolve SourceNode/TargetNode IPs.
Call gRPC agents’ Prepare() and StartMigration() on target/source.
Walk through phases: Pending → Syncing → Finalizing → Completed/Failed.
Controller main (manager)
New controller/main.go:
Registers schemes:
       var scheme = runtime.NewScheme()       func init() {           utilruntime.Must(clientgoscheme.AddToScheme(scheme))           utilruntime.Must(migrationv1alpha1.AddToScheme(scheme))       }
Sets up a standard controller-runtime manager with:
       mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{           Scheme: scheme,           Metrics: server.Options{               BindAddress: metricsAddr,           },           HealthProbeBindAddress: probeAddr,           LeaderElection:         enableLeaderElection,           LeaderElectionID:       "podmigration-controller.transporter.io",       })
Registers the reconciler:
       if err = (&controllers.PodMigrationReconciler{           Client: mgr.GetClient(),           Scheme: mgr.GetScheme(),       }).SetupWithManager(mgr); err != nil { ... }
Adds health/ready probes.
Uses Zap logger and standard --metrics-bind-address, --health-probe-bind-address, --leader-elect flags.
This is now a proper, independent controller binary (module controller) that your Helm chart can build and run.
Phase D – DaemonSet Agent (“Heavy Lifter”)
Files:
migration-agent/main.go
migration-agent/go.mod
migration-agent/Dockerfile
What’s fixed:
Go toolchain compatibility:
migration-agent/go.mod:
    module migration-agent    go 1.21    require (        github.com/containerd/containerd v1.7.30    )
migration-agent/Dockerfile uses golang:1.21-alpine instead of 1.25.
The agent is still a stub (it prints status and loops), but it now builds and runs as a long-lived process, ready for you to layer in:
gRPC server
PerformCheckpoint and PerformRestore APIs using containerd + CRIU + checkpointctl
UNIX-socket communication with containerd shim
The Helm chart for the DaemonSet (helm-chart/templates/daemonset.yaml) can deploy this stub image as a privileged agent while you iterate on the actual kernel-level logic.
Build Status
go build ./... at repo root succeeds.
The root Makefile build target now builds the new Cobra-based CLI from cmd/transporter.
controller and migration-agent modules both build independently using Go 1.21.
How it all ties together
Intent (CRD): PodMigration in migration.transporter.io/v1alpha1 stores migration request + status.
CLI: transporter migrate creates a PodMigration CR and exits, printing the CR name as the Migration ID; transporter status and transporter list query the CRD to show live phases/messages.
Controller: Watches PodMigration CRs, updates phases and messages; currently simulates completion but is wired correctly to be extended with real gRPC calls to your DaemonSet agents.
Agent: Compilable stub, ready for real PerformCheckpoint / PerformRestore implementation.
If you’d like, next steps can be:
Generate and apply the actual CRD YAML via controller-gen.
Flesh out the controller phases (Pending, Syncing, Finalizing, Completed, Failed) and node IP resolution.
Design the gRPC API and message shapes for Prepare, StartMigration, PerformCheckpoint, and PerformRestore.
