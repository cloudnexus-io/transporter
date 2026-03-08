# Transporter Helm Chart

This Helm chart deploys the Kubernetes pod migration tool. It includes both the transporter service and the migration agent daemonset.

## Deployment Options

### Deploying Migration Agent Only

To deploy only the migration agent (without the transporter service):

1. Set the replica count to 0 in values.yaml:
   ```yaml
   replicaCount: 0
   ```

2. Ensure migration agent is enabled:
   ```yaml
   migrationAgent:
     enabled: true
   ```

3. Deploy using Helm:
   ```bash
   helm install my-release . --values values.yaml
   ```

This will deploy only the migration agent daemonset while skipping the transporter deployment.

## Configuration Options

| Parameter             | Description                          | Default         |
|----------------------|--------------------------------------|-----------------|
| `replicaCount`       | Number of transporter replicas       | `1`             |
| `migrationAgent.enabled` | Enable migration agent daemonset  | `true`          |
| `image.repository`   | Transporter image repository         | `192.168.1.20:5000/transporter`|
| `image.tag`          | Transporter image tag                | `latest`        |
| `migrationAgent.image.repository` | Migration agent image repository | `192.168.1.20:5000/migration-agent`|
| `migrationAgent.image.tag` | Migration agent image tag     | `latest`        |

## Notes

- The transporter service will not be deployed when `replicaCount` is set to 0
- The migration agent daemonset will run on all nodes in the cluster