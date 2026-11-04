# AWS ECR credentials sync

This controller replaces the management-cluster Quay pull-secret dependency for HyperShift HostedClusters. It uses the AWS SDK default credential chain, including EKS Pod Identity, to call `ecr:GetAuthorizationToken`.

## Development

Run `go test ./...` and `go build ./cmd/ecr-creds-sync`.

### LocalStack development

LocalStack for AWS 4.10 introduced EKS Pod Identity and EKS Auth emulation. The current release is `2026.08.0`, which retains those capabilities. With an Ultimate account, set `LOCALSTACK_AUTH_TOKEN` and start the development services:

```bash
export LOCALSTACK_AUTH_TOKEN=...
make localstack
```

This follows the LocalStack setup from HyperFleet PR 724: it runs `localstack/localstack-pro:latest` through Docker Compose compatibility, enables ECR, EC2, EKS, EKS Auth, IAM, and STS, mounts the container-engine socket, and waits for the health endpoint. The LocalStack EKS provider and credential webhook can then be used for a Pod Identity development cluster according to the [LocalStack EKS documentation](https://docs.localstack.cloud/aws/services/eks/). This is preferable to pretending a kind node has EKS Pod Identity.

For rootless Podman, the launcher starts the user socket, maps `${XDG_RUNTIME_DIR}/podman/podman.sock` into LocalStack as `/var/run/docker.sock`, and uses the `DOCKER_HOST`/`DOCKER_SOCK` Docker-API settings. The LocalStack image does not contain a Podman or Docker CLI, so `DOCKER_CMD` is intentionally not set:

```bash
systemctl --user start podman.socket
CONTAINER_ENGINE=podman make localstack
```

For a controller process running outside the emulated cluster, point the AWS SDK at LocalStack explicitly:

```bash
AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test \
ECR_REPOSITORY=000000000000.dkr.ecr.us-east-1.localhost.localstack.cloud/rosa/release \
AWS_REGION=us-east-1 AWS_ENDPOINT_URL=http://localhost:4566 \
go run ./cmd/ecr-creds-sync
```

Run the lightweight LocalStack ECR API smoke test with `make localstack-ecr-test`. It is skipped during the normal test suite unless `LOCALSTACK_INTEGRATION=1` is set. Override the port with `LOCALSTACK_PORT`; use `CONTAINER_ENGINE=podman` when Docker is not the preferred engine.

For the full EKS Pod Identity path, set `HYPERSHIFT_DIR` to the HyperShift checkout and run `make localstack-test`. This creates an embedded LocalStack EKS cluster, creates the Pod Identity IAM association, builds and pushes this image to LocalStack ECR, deploys it on the embedded control-plane node, installs the HostedCluster CRD, and creates a test HostedCluster. It avoids managed node groups because LocalStack documents those as not fully supported. It passes when `clusters/ecr-test` receives `ecr-pull-secret`. `make e2e-localstack-eks` remains an explicit alias for the same flow.

For every `HostedCluster`, it reads `spec.pullSecret.name` and writes a Kubernetes `kubernetes.io/dockerconfigjson` Secret with that name in the HostedCluster namespace. It does not modify the HostedCluster spec. HyperShift already copies that Secret to the HCP namespace as `pull-secret`, where the existing ignition and HCP consumers use it.

## Configuration

- `--ecr-repository` or `ECR_REPOSITORY`: ECR repository URI, including its path.
- `--aws-region` or `AWS_REGION`: AWS region. If omitted, the AWS SDK configuration is used.
- `--aws-endpoint-url` or `AWS_ENDPOINT_URL`: optional endpoint override for LocalStack and other AWS emulators; leave unset in production.
- `--refresh-after` or `REFRESH_AFTER`: duration between ECR authorization token refreshes. Defaults to `2h`.
- `--source-secrets` or `SOURCE_SECRETS`: comma-separated list of Secret names in each HostedCluster namespace to read and merge into the pull-secret before writing ECR credentials. Use when another controller (e.g. External Secrets Operator) writes base credentials such as `quay.io` tokens to a separate Secret (e.g. `base-pull-secret`). The final pull-secret will contain all credentials from the source Secrets plus the fresh ECR token. Missing source Secrets are skipped with a log warning; malformed source Secrets are also skipped. When empty (default), only ECR credentials are written.

The controller mints one ECR authorization token and keeps it in memory for all HostedClusters managed by the process. New HostedClusters reuse that token. After the refresh interval, the first reconciliation mints a replacement and subsequent reconciliations reuse it. The default refresh interval is 2 hours, while ECR authorization tokens are valid for 12 hours. A process restart clears the in-memory token and causes a new token to be minted on the first reconciliation.

The controller watches owned Secrets and re-reconciles immediately when a source Secret is overwritten (e.g. by ESO), restoring the ECR credentials without waiting for the next scheduled refresh.

## Pod identity

Create an EKS Pod Identity association (or an equivalent workload identity binding) for the `hypershift/ecr-creds-sync` ServiceAccount with the policy in `config/iam-policy.json`. No AWS access keys are stored in Kubernetes.

Replace the example repository, region, and image in `config/deployment.yaml` before applying `config/rbac.yaml` and `config/deployment.yaml`. Build with `docker build -t ecr-creds-sync .`.
