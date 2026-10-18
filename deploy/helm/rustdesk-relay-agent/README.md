# RustDesk Relay Agent Helm Chart

Helm chart for deploying a remote relay host (`hbbr` + `rdpanel agent`) connecting to an
[RD Panel](https://github.com/bropines/rdpanel) instance.

## Overview

Remote relays allow distributing RustDesk traffic across multiple geographic regions or
network locations. The relay agent:
- Runs in `RDPANEL_MODE=agent` (supervising `hbbr`);
- Enrolls with the RD Panel instance using a one-time enrollment token;
- Establishes a persistent WebSocket connection for health reporting and configuration
  management;
- Accepts client relay connections on TCP port 21117.

> [!IMPORTANT]
> **Single replica only:** Never increase `replicaCount` above 1 behind a single Service.
> Session pairing is local to each `hbbr` process (`PEERS`). Each remote relay host or
> pod must enroll and run as its own relay instance.

## Installation

```sh
helm repo add rdpanel https://bropines.github.io/rdpanel # or OCI registry
helm install relay-agent deploy/helm/rustdesk-relay-agent \
  --namespace rustdesk --create-namespace \
  --set agent.panelUrl="https://panel.example.com" \
  --set agent.token="<ENROLLMENT_TOKEN>"
```

## Parameters

| Parameter | Description | Default |
|---|---|---|
| `image.repository` | Agent container image | `ghcr.io/bropines/rdpanel` |
| `image.tag` | Agent container tag | `""` (chart appVersion) |
| `agent.panelUrl` | Base URL of the RD Panel instance | `""` |
| `agent.token` | One-time enrollment token issued by the panel | `""` |
| `agent.existingSecret` | Existing Secret containing the enrollment token | `""` |
| `agent.publicHost` | Host[:port] handed to RustDesk clients | `""` |
| `agent.mgmtHost` | Host[:port] used by the panel for reachability checks | `""` |
| `persistence.enabled` | Enable state persistence for agent identity | `true` |
| `persistence.size` | Volume size | `1Gi` |
| `service.type` | Service type (`LoadBalancer`, `NodePort`, `ClusterIP`) | `LoadBalancer` |
| `service.port` | Relay listening port | `21117` |
