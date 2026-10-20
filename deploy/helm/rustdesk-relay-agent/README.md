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
| `podSecurityContext`, `securityContext` | s6-overlay's `/init` starts as root and drops to `PUID:PGID` (10001) for the agent and hbbr; capabilities are limited to `CHOWN`, `DAC_OVERRIDE`, `FOWNER`, `SETGID`, `SETUID`, `KILL` | see `values.yaml` |

## Monitoring

The agent has no metrics endpoint of its own (port 21117 is hbbr's relay port), so the
chart ships no ServiceMonitor. Relay health, sessions and traffic of every agent are
exported by the central RD Panel's `/metrics` (`rdpanel_relay_*`); scrape the panel.
