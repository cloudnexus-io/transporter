# Project Overview

This document provides a high-level overview of the Transporter project, a Kubernetes-native solution for live pod migration.

## Architecture

The Transporter project follows a standard Kubernetes operator pattern. It consists of three main components:

1.  **Transporter CLI (`transporter`):** A command-line interface for users to initiate and manage pod migrations.
2.  **PodMigration Controller (`controller`):** The control plane component that orchestrates the migration process.
3.  **Migration Agent (`migration-agent`):** A daemon running on each node that executes the low-level migration tasks.

## Components

### Transporter CLI

The Transporter CLI provides a user-friendly interface for interacting with the migration system.

**Commands:**

*   `transporter migrate <pod-name> -n <namespace> -t <target-node>`: Starts a pod migration.
*   `transporter status <migration-id>`: Checks the status of a migration.
*   `transporter list`: Lists all migrations in progress.

### PodMigration Controller

The controller is the core of the system. It watches for `PodMigration` custom resources and manages the entire migration lifecycle.

**Key Functions:**

*   **Reconcile:** The main controller loop that watches for `PodMigration` resources.
*   **prepareTargetNode:** Prepares the target node for migration by pulling necessary container images.
*   **startMigration:** Triggers the migration on the source node using CRIU.
*   **finalizeMigration:** Handles the completion and cleanup of the migration.
*   **handleMigrationError:** Ensures proper rollback on failures.

### Migration Agent

The migration agent is a gRPC server that runs on each node in the cluster. It receives commands from the controller to perform node-level operations.

## CRD Definition

The `PodMigration` Custom Resource Definition (CRD) is the primary API for the system.

**`PodMigrationSpec`:**

*   `podName` (string): The name of the pod to migrate.
*   `namespace` (string): The namespace of the pod.
*   `sourceNode` (string): The name of the source node.
*   `targetNode` (string): The name of the target node.
*   `strategy` (string): The migration strategy (e.g., "live", "cold").

**`PodMigrationStatus`:**

*   `migrationID` (string): A unique identifier for the migration.
*   `phase` (string): The current phase of the migration (e.g., `Pending`, `Syncing`, `Finalizing`, `Completed`, `Failed`).
*   `startTime` (metav1.Time): The timestamp when the migration started.
*   `message` (string): A human-readable message about the current status.

## Workflow

1.  A user creates a `PodMigration` resource using the Transporter CLI or `kubectl`.
2.  The PodMigration Controller detects the new resource.
3.  The controller communicates with the migration agent on the target node to prepare for the migration (e.g., pull container images).
4.  The controller communicates with the migration agent on the source node to initiate the migration (e.g., using CRIU to checkpoint the pod).
5.  The controller updates the `PodMigration` resource's status throughout the process.
6.  Once the migration is complete, the controller finalizes the process and cleans up any temporary resources.

## File Structure

*   `api/`: Contains the CRD definitions for the `PodMigration` resource.
*   `cmd/transporter/`: The source code for the Transporter CLI tool.
*   `controller/`: The source code for the PodMigration Controller.
*   `migration-agent/`: The source code for the migration agent.
*   `helm-chart/`: A Helm chart for deploying the controller and agent.
*   `README.md`: The main README for the CLI tool.
*   `CONTROLLER_README.md`: The README for the controller.
