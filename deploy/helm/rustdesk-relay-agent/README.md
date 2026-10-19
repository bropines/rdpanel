# RustDesk Relay Agent Helm Chart

Installs an extra remote `hbbr` relay agent supervised by **RD Panel** in Kubernetes.

## Quick start

```sh
# 1. Get an enrollment token from RD Panel:
# Server -> Relays -> Add Relay -> Relay agent

# 2. Install the chart:
helm install relay-nl ./deploy/helm/rustdesk-relay-agent \
  --set panel.url="https://desk-api.example.com" \
  --set panel.enrollToken="<one-time-token>" \
  --set service.type=LoadBalancer
```

## Host Network mode

For bare-metal or cloud clusters where real client IPs and raw UDP/TCP performance are desired:

```sh
helm install relay-nl ./deploy/helm/rustdesk-relay-agent \
  --set panel.url="https://desk-api.example.com" \
  --set panel.enrollToken="<one-time-token>" \
  --set hostNetwork=true \
  --set service.enabled=false
```

## Values

| Key | Description | Default |
|---|---|---|
| `panel.url` | Public client API host of the central RD Panel | `"https://desk-api.example.com"` |
| `panel.enrollToken` | One-time agent enrollment token | `""` |
| `panel.existingSecret` | Secret name containing `enroll-token` | `""` |
| `relay.port` | hbbr TCP port | `21117` |
| `hostNetwork` | Use host network namespace | `false` |
| `persistence.enabled` | Persist agent ed25519 identity key and config | `true` |
| `service.type` | Service type (`LoadBalancer`, `NodePort`, `ClusterIP`) | `LoadBalancer` |
| `serviceMonitor.enabled` | Prometheus Operator ServiceMonitor | `false` |
