# Example Usage - PodMigration Controller

## Overview
This is a Kubernetes controller for managing pod migrations between nodes using CRDs.

## Key Functions:

1. **Reconcile** - Main controller loop watching PodMigration resources
2. **prepareTargetNode** - Prepares target node (pull images via gRPC)
3. **startMigration** - Triggers migration on source node (CRIU via gRPC)
4. **finalizeMigration** - Handles completion and cleanup
5. **handleMigrationError** - Ensures proper rollback on failures

## Workflow:

1. Create PodMigration CRD with source pod, namespace, source/target nodes, and strategy
2. Controller detects new CRD and begins migration process:
   - Identify source/target node IPs
   - Call Prepare() gRPC method on target node to pre-pull containers
   - Call StartMigration() gRPC method on source node (triggers CRIU)
3. Update PodMigration status at each step
4. On success or failure, cleanup resources as needed

## Implementation Details:

### Controller Phases:
- Pending: Initial setup and IP discovery
- Syncing: Prepare target node with images
- Finalizing: Trigger migration on source node  
- Completed/Finalized: Migration complete

### Error Handling:
- If any step fails, controller ensures:
  - Source pod is unfrozen (if frozen)
  - Target node resources are cleaned up
  - PodMigration status shows failure with details

This approach provides a robust foundation that can be extended based on specific requirements for your migration agent implementation.