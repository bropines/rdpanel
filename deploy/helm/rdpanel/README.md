# rdpanel Helm chart (RD Panel)

Runs RD Panel and the RustDesk server stack (hbbs + hbbr + panel) on Kubernetes as **one
pod** from the image `ghcr.io/bropines/rdpanel`. For cluster operators; for a single
docker host see [docs/deploy.md](../../../docs/deploy.md).

## Contents

- [Install](#install) · [What runs in the pod](#what-runs-in-the-pod)
- [Networking modes](#networking-modes) · [Secrets](#secrets) ·
  [Legacy migration](#legacy-migration-lejianwenrustdesk-api) ·
  [Monitoring and policies](#monitoring-and-policies)
- [Values](#values) · [Examples](#examples)
- [Upgrading from the `rustdesk-panel` chart](#upgrading-from-the-rustdesk-panel-chart--01x) ·
  [Why a StatefulSet with exactly one replica](#why-a-statefulset-with-exactly-one-replica)
- [Development / validation](#development--validation) ·
  [Publishing the chart](#publishing-the-chart-release-procedure)

## Install

```sh
helm install rustdesk oci://ghcr.io/bropines/charts/rdpanel --version <version> \
  -n rustdesk --create-namespace \
  --set server.relay=rustdesk.example.com:21117 \
  --set panel.publicURL=https://rustdesk.example.com
# or from a checkout:
helm install rustdesk deploy/helm/rdpanel -n rustdesk --create-namespace -f my-values.yaml
```

Then:

```sh
kubectl -n rustdesk exec -it rustdesk-rustdesk-panel-0 -- rdpanel user create --admin admin
kubectl -n rustdesk exec rustdesk-rustdesk-panel-0 -- cat /data/id_ed25519.pub   # client "Key"
```

RustDesk client settings:

| client field | value |
|---|---|
| ID server | the rendezvous address (LoadBalancer IP/DNS or node address) |
| Relay server | `server.relay` |
| API server | `panel.publicURL` |
| Key | the public key shown above |

Resource names keep the historical base name `rustdesk-panel` (hence the pod
`rustdesk-rustdesk-panel-0` for a release called `rustdesk`); see
[Upgrading from the `rustdesk-panel` chart](#upgrading-from-the-rustdesk-panel-chart--01x).

## What runs in the pod

The image runs three processes under s6-overlay:

| process | role | ports |
|---|---|---|
| `hbbs` | rendezvous / ID server | 21115/tcp NAT test (+ loopback admin), 21116/tcp+udp, 21118/tcp WebSocket |
| `hbbr` | relay | 21117/tcp (+ loopback admin), 21119/tcp WebSocket |
| `rdpanel` | RustDesk API server + admin panel | 21114/tcp HTTP (`/api`, UI, `/healthz`, `/metrics`) |

The panel controls hbbs/hbbr through their **loopback-only** admin channels
(`127.0.0.1:21115`, `127.0.0.1:21117`), so the three processes must share a network
namespace. That is why they are one container and not three Deployments, and why the
chart runs a single-replica StatefulSet
([details](#why-a-statefulset-with-exactly-one-replica)).

## Networking modes

### 1. `hostNetwork: true` (recommended for production)

```yaml
hostNetwork: true
nodeSelector: {kubernetes.io/hostname: my-node}
rendezvous: {service: {enabled: false}}
```

hbbs binds 21115–21119 directly on the node. It sees real client addresses and replies
to UDP from the address that clients contacted, so NAT hole punching (P2P) behaves as it
does on bare metal. In this mode:

- `dnsPolicy` defaults to `ClusterFirstWithHostNet`.
- Pin the pod to one node, because clients are configured with that node's address. Open the ports in the node firewall.
- The loopback admin channels are on the **node's** loopback. Any process on that node gets
  unauthenticated admin access to hbbs/hbbr. Never run L4 forwarders (nginx `stream`, frp,
  `ssh -L`, …) to `127.0.0.1:21115/21117` on that node.
- NetworkPolicy is not enforced for hostNetwork pods.
- To mirror the docker setup (panel on `127.0.0.1:31114` behind a node-local
  reverse proxy), set `panel.listen: 127.0.0.1:31114`, `probes.type: exec` and
  `service.enabled: false`. See `ci/hostnetwork-values.yaml`.

### 2. LoadBalancer (default) / NodePort

The rendezvous Service publishes 21115/tcp, 21116/tcp+udp and 21117/tcp.

- **`externalTrafficPolicy: Local` (default).** kube-proxy does not SNAT, so hbbs
  sees the real client IP. hbbs needs it for NAT-type detection, for the
  addresses it hands to peers when punching holes, for the IP blocker and for
  logs. With `Cluster`, every client appears as a node IP. The trade-off is that
  only the node running the pod answers, and the LB health-checks
  `healthCheckNodePort` to find it.
- P2P still works less reliably than with hostNetwork, because the pod's outbound
  UDP is source-NATed by the node. When punching fails, clients fall back to the relay.
- **Mixed TCP/UDP on one LoadBalancer** (`MixedProtocolLBService`) is GA since
  Kubernetes **1.26**. If your cloud or MetalLB rejects it, set
  `rendezvous.service.split: true`. That creates `-rendezvous-tcp` and
  `-rendezvous-udp` Services. To give both the same IP with MetalLB:

  ```yaml
  rendezvous:
    service:
      split: true
      annotations:
        metallb.universe.tf/allow-shared-ip: rustdesk
        metallb.universe.tf/loadBalancerIPs: 203.0.113.10
  ```

  Cloud examples: AWS NLB `service.beta.kubernetes.io/aws-load-balancer-type: external`
  plus `...-nlb-target-type: ip`. GKE does mixed protocol natively on 1.26+.

### WebSocket (21118 / 21119)

Used by the RustDesk web client and by clients with "use WebSocket" enabled.
Client paths: `/ws/id` → hbbs 21118, `/ws/relay` → hbbr 21119.

> **Security:** hbbs and hbbr take the client IP from `X-Real-IP` /
> `X-Forwarded-For` on these ports **without any trust check**. Expose them
> only through a proxy that **overwrites** both headers with the real peer
> address. With ingress-nginx, keep `use-forwarded-headers: false` and
> `compute-full-forwarded-for: false` (the defaults). For that reason the WS
> ports are **disabled** on the L4 rendezvous Service by default.

To expose them:

- `websocket.ingress.enabled: true` creates a ClusterIP Service `<fullname>-ws` and a
  separate Ingress with long timeouts (set your controller's annotations);
- the Gateway API equivalent is `httpRoute.websocketRules: true`.

## Secrets

| Secret key | env | purpose |
|---|---|---|
| `jwt-key` | `RUSTDESK_API_JWT_KEY` | HS256 key for client tokens, shared by hbbs (`MUST_LOGIN`) and the panel. When migrating from lejianwen/rustdesk-api, use the **old** value, otherwise every client is logged out. |
| `secret-key` | `RDPANEL_SECRET_KEY` | base64 of 32 bytes, the AEAD key for data at rest (address-book passwords, TOTP, OIDC secrets). If you lose it, that data cannot be decrypted. |

You have three options:

1. **`secrets.existingSecret`** (recommended for GitOps):
   ```sh
   kubectl -n rustdesk create secret generic rustdesk-keys \
     --from-literal=jwt-key="$OLD_RUSTDESK_API_JWT_KEY" \
     --from-literal=secret-key="$(openssl rand -base64 32)"
   ```
2. Set explicit `secrets.jwtKey` / `secrets.secretKey` in the values.
3. Let the chart generate them on first install. Later upgrades read the live
   Secret with `lookup` and keep the values. The Secret carries
   `helm.sh/resource-policy: keep`, so it survives `helm uninstall`.

> **`lookup` caveat:** `helm template`, `helm install --dry-run` (client mode)
> and tools that render offline (Argo CD, `helmfile template`) cannot read the
> cluster. They generate **new random keys on every render**, which rotates
> the keys on every sync. In those setups always use option 1 or 2. Flux
> helm-controller runs a real install/upgrade, so lookup works there.

**Key file on the volume instead:** `secrets.setSecretKey: false` stops passing
`RDPANEL_SECRET_KEY`. The panel then uses or creates `/data/panel/secret.key` on the
volume, for example after you copy over a docker install's `secret.key`.

### Server key pair (optional)

To keep an existing server's key, so clients keep their configured "Key":

```sh
kubectl -n rustdesk create secret generic rustdesk-server-keypair \
  --from-file=id_ed25519=./id_ed25519 --from-file=id_ed25519.pub=./id_ed25519.pub
```
```yaml
serverKeys: {enabled: true, existingSecret: rustdesk-server-keypair}
```

The pair is passed as `KEY_PRIV` / `KEY_PUB`. The image's `key-secret` init step
writes it into `/data` and validates it. Alternatively, copy both files into the PVC.

## Legacy migration (lejianwen/rustdesk-api)

The Job runs `rdpanel migrate-legacy --from /legacy/rustdeskapi.db --report /data/panel/migration-report.json`
against the release's data PVC. The legacy database is mounted read-only from a PVC or a hostPath.
What the importer does and how to read its report: [docs/migration.md](../../../docs/migration.md).

1. Install the chart normally once (`replicaCount: 1`). This creates the PVC and
   the hbbs keys. Use the **old** `RUSTDESK_API_JWT_KEY` (see Secrets), and
   `serverKeys` if you are moving hbbs too. Do not create users yet.
2. Put `rustdeskapi.db` on a PVC (for example `legacy-db`) or a node directory.
3. Stop the server and run the Job. The chart refuses `migration.enabled` unless `replicaCount=0`:
   ```sh
   helm upgrade rustdesk <chart> -n rustdesk --reuse-values \
     --set replicaCount=0 --set migration.enabled=true \
     --set migration.legacy.existingClaim=legacy-db  # optionally --set migration.dryRun=true first
   kubectl -n rustdesk logs -f job/rustdesk-rustdesk-panel-migrate-<revision>
   ```
4. Check the report. It is in the data PVC at `panel/migration-report.json`, and
   the Job logs show it as well.
5. Start the server again:
   ```sh
   helm upgrade rustdesk <chart> -n rustdesk --reuse-values \
     --set replicaCount=1 --set migration.enabled=false
   ```

Notes:

- **hostPath source:** `migration.legacy.hostPath=/srv/rustdesk-api/data`. Use
  `migration.nodeSelector` so the Job runs on that node.
- **ReadWriteOnce data volume:** the Job has to land on the node where the volume can
  attach. Most CSI drivers handle that once the server pod is gone.
- The Job name contains the release revision because Job specs are immutable.
  `ttlSecondsAfterFinished` cleans it up.

## Monitoring and policies

- `serviceMonitor.enabled` scrapes `/metrics` on the panel Service. `/metrics`
  is IP-restricted: add the Prometheus pod CIDR to `panel.metricsAllow`, or scrapes return 403.
- `prometheusRule.enabled` installs the alert rules of
  `deploy/monitoring/prometheus/alerts.yaml` (copied in `files/alerts.yaml`) as a
  `PrometheusRule`, with `job` set to the panel Service name (what the
  ServiceMonitor produces; override with `prometheusRule.job`). Add your
  Prometheus `ruleSelector` labels under `prometheusRule.labels`; drop alerts with
  `disabledAlerts`, add labels with `additionalRuleLabels`, more groups with `extraGroups`.
- `grafanaDashboard.enabled` creates a ConfigMap with the dashboard
  (`files/rustdesk-panel.json`) labelled `grafana_dashboard: "1"` for the Grafana
  sidecar; `grafanaDashboard.folder` sets the `grafana_folder` annotation.
- `panel.metricsPerDevice` adds one `rdpanel_presence_device_online{id}` series per
  device (off by default). See [docs/monitoring.md](../../../docs/monitoring.md).
- `networkPolicy.enabled` allows 21114 only from the namespaces matched by
  `ingressControllerNamespaceSelector`, plus `metricsNamespaceSelector` and
  `extraPanelFrom`. WS ports are allowed only from the controller. Rendezvous
  and relay ports are allowed from `rendezvousFrom` (default: anywhere).
  Egress is unrestricted unless `networkPolicy.egress.enabled`.
- `podDisruptionBudget` is off by default. With a single replica it either blocks
  node drains or has no effect.
- Behind an Ingress, add the controller's pod CIDR to `panel.trustedProxies`.
  Otherwise the panel sees the controller IP as every client's IP, which breaks
  rate limits and audit.

## Values

The full list with comments is in [`values.yaml`](values.yaml). The main ones are below; the
panel's `RDPANEL_*` settings that have no dedicated key go into `panel.env`
([reference](../../../docs/configuration.md)).

| key | default | description |
|---|---|---|
| `image.repository` / `tag` / `digest` | `ghcr.io/bropines/rdpanel` / appVersion / — | image |
| `replicaCount` | `1` | `0` or `1` only |
| `server.relay` | `""` | `RELAY`, the public relay `host:21117` given to clients |
| `server.encryptedOnly` | `true` | `ENCRYPTED_ONLY=1` |
| `server.alwaysUseRelay` | `false` | `ALWAYS_USE_RELAY=Y/N` |
| `server.mustLogin` | `false` | `MUST_LOGIN=Y/N` (hbbs requires a panel JWT) |
| `server.tcpKeepalive` | `false` | `TCP_KEEPALIVE=Y/N`: keepalives for clients in TCP/WebSocket mode ("Use WebSocket", UDP blocked) so they stay online; see [docs/deploy.md](../../../docs/deploy.md#websocket-and-tcp-mode-clients) |
| `server.webrtc` | `false` | `WEBRTC=Y/N`: WebRTC signaling for RustDesk 1.5+ clients; see [docs/webrtc.md](../../../docs/webrtc.md) |
| `server.env` | `{}` | extra env for hbbs/hbbr (for example `RUST_LOG`) |
| `panel.listen` | `:21114` | `RDPANEL_LISTEN`. The container port comes from it. |
| `panel.publicURL` | `""` | `RDPANEL_PUBLIC_URL` |
| `panel.adminURL` | `""` | `RDPANEL_ADMIN_URL` (panel host when it differs from the public client-API host; expose only `/api/` minus `/api/v1/` publicly, see [docs/deploy.md](../../../docs/deploy.md#split-hosts-public-client-api--internal-admin-panel)) |
| `panel.dataDir` | `/data/panel` | `RDPANEL_DATA_DIR` |
| `panel.hbbsPublicKeyFile` | `/data/id_ed25519.pub` | `RDPANEL_HBBS_PUBLIC_KEY_FILE` |
| `panel.trustedProxies` | loopback | `RDPANEL_TRUSTED_PROXIES` |
| `panel.realIPHeader` | `X-Forwarded-For` | `RDPANEL_REAL_IP_HEADER` |
| `panel.metricsAllow` | loopback | `RDPANEL_METRICS_ALLOW` |
| `panel.metricsPerDevice` | `false` | `RDPANEL_METRICS_PER_DEVICE` |
| `panel.tokenLifetime`, `jwtLifetime`, `sessionIdle`, `sessionAbsolute` | `""` (binary default) | Go durations |
| `panel.log.level` / `format` | `info` / `json` | |
| `panel.env` | `{}` | any extra `RDPANEL_*` |
| `secrets.existingSecret` | `""` | Secret with `jwt-key` and `secret-key` |
| `secrets.jwtKey` / `secretKey` | `""` | explicit values. Generated when empty. |
| `secrets.setSecretKey` | `true` | pass `RDPANEL_SECRET_KEY` |
| `secrets.keep` | `true` | `helm.sh/resource-policy: keep` |
| `serverKeys.enabled` / `existingSecret` / `privateKey` / `publicKey` | off | `KEY_PRIV` / `KEY_PUB` |
| `hostNetwork` | `false` | see [Networking modes](#networking-modes) |
| `dnsPolicy` | auto | `ClusterFirstWithHostNet` when hostNetwork |
| `service.*` | ClusterIP 21114 | panel Service |
| `rendezvous.service.enabled` | `true` | |
| `rendezvous.service.type` | `LoadBalancer` | `LoadBalancer`, `NodePort` or `ClusterIP` |
| `rendezvous.service.externalTrafficPolicy` | `Local` | keeps the client IP |
| `rendezvous.service.split` | `false` | separate TCP and UDP Services |
| `rendezvous.service.loadBalancerIP` / `loadBalancerClass` / `loadBalancerSourceRanges` / `annotations` / `tcpAnnotations` / `udpAnnotations` | — | LB settings |
| `rendezvous.service.ports.<natTest\|rendezvousTcp\|rendezvousUdp\|relay\|rendezvousWs\|relayWs>` | WS off | `enabled`, `port`, `nodePort` |
| `ingress.*` | off | panel Ingress (`className`, `annotations`, `hosts`, `tls`) |
| `httpRoute.*` | off | Gateway API HTTPRoute (`parentRefs`, `hostnames`, `websocketRules`) |
| `websocket.ingress.*` | off | Ingress for `/ws/id` and `/ws/relay` |
| `persistence.enabled` / `existingClaim` / `storageClass` / `size` / `accessModes` / `retentionPolicy` | on, 1Gi | `/data` volume |
| `podSecurityContext` / `securityContext` | seccomp RuntimeDefault; caps dropped except CHOWN, DAC_OVERRIDE, FOWNER, SETGID, SETUID, KILL | the image runs s6 as root |
| `resources`, `nodeSelector`, `tolerations`, `affinity`, `topologySpreadConstraints`, `priorityClassName`, `podAnnotations`, `podLabels`, `terminationGracePeriodSeconds` | | scheduling |
| `probes.type` | `httpGet` | `exec` uses `wget` to `127.0.0.1` for loopback binds |
| `probes.startup` / `liveness` / `readiness` | | timings. `customXxxProbe` replaces a probe. |
| `extraEnv`, `extraEnvFrom`, `extraVolumes`, `extraVolumeMounts`, `extraContainers`, `initContainers` | `[]` | |
| `serviceMonitor.*` | off | Prometheus Operator |
| `prometheusRule.*` | off | alert rules as a PrometheusRule |
| `grafanaDashboard.*` | off | dashboard ConfigMap for the Grafana sidecar |
| `networkPolicy.*` | off | |
| `podDisruptionBudget.*` | off | |
| `migration.*` | off | legacy import Job |

## Examples

- [`ci/hostnetwork-values.yaml`](ci/hostnetwork-values.yaml): hostNetwork, panel on loopback, exec probes, no Services.
- [`ci/loadbalancer-values.yaml`](ci/loadbalancer-values.yaml): MetalLB split TCP/UDP with a shared IP, existing secrets and key pair, ServiceMonitor, PrometheusRule, Grafana dashboard, NetworkPolicy, PDB.
- [`ci/ingress-values.yaml`](ci/ingress-values.yaml): ingress-nginx and cert-manager, WebSocket Ingress, HTTPRoute, NodePort rendezvous.

## Upgrading from the `rustdesk-panel` chart (≤ 0.1.x)

Up to 0.1.x the chart was called `rustdesk-panel` and published as
`oci://ghcr.io/bropines/charts/rustdesk-panel`, with the image
`ghcr.io/bropines/rustdesk-panel`. The project is now **RD Panel**: chart
`rdpanel`, image `ghcr.io/bropines/rdpanel`.

Resource names, the `app.kubernetes.io/name` selector label and the PVC name
still use the historical base name `rustdesk-panel` (unless `nameOverride` is
set), so an existing release upgrades in place: the StatefulSet, its immutable
selector and `volumeClaimTemplates`, the PVC `data-<fullname>-0`, Services and
Secrets keep their names. Only the `helm.sh/chart` label changes, which rolls
the pod once.

```sh
helm upgrade rustdesk oci://ghcr.io/bropines/charts/rdpanel --version <version> \
  -n rustdesk --reuse-values
```

- If your values pin `image.repository: ghcr.io/bropines/rustdesk-panel`, change
  it to `ghcr.io/bropines/rdpanel` (or drop it to use the chart default); the
  old repository gets no new releases.
- Do not set `nameOverride` on an existing release: it renames every resource
  and the StatefulSet selector, which Kubernetes rejects. New installs may set
  `nameOverride: rdpanel` to get `rdpanel` in resource names.

## Why a StatefulSet with exactly one replica

`replicaCount` accepts only `0` or `1` (enforced by `values.schema.json`):

- hbbs keeps peer registrations, punch-hole state and relay settings **in memory**.
  Two pods would be two unrelated ID servers. Clients would only find peers that
  registered on the same pod.
- The panel uses **SQLite** (one writer) on a ReadWriteOnce volume.
- The server key pair `id_ed25519` identifies the server to clients and must be unique.

Also:

- `replicaCount: 0` stops the server without uninstalling the release. The legacy
  migration Job uses it.
- The StatefulSet gives a stable PVC (`data-<fullname>-0`) and ordered, non-overlapping
  restarts, so two pods never write the same SQLite file.

## Development / validation

```sh
helm lint --strict deploy/helm/rdpanel
for f in deploy/helm/rdpanel/ci/*.yaml; do helm lint --strict deploy/helm/rdpanel -f $f; done
helm template rd deploy/helm/rdpanel | kubeconform -strict -summary -kubernetes-version 1.30.0 \
  -schema-location default \
  -schema-location 'https://raw.githubusercontent.com/datreeio/CRDs-catalog/main/{{.Group}}/{{.ResourceKind}}_{{.ResourceAPIVersion}}.json' -
```

CI: `.github/workflows/helm.yml` runs the same checks on PRs that touch `deploy/helm/`.

## Publishing the chart (release procedure)

The chart is published as an OCI artifact to `ghcr.io/bropines/charts`:

```sh
# 1. Bump Chart.yaml `version` (chart) and `appVersion` (image tag), commit, tag chart-v<version>.
helm package deploy/helm/rdpanel -d dist/
echo "$GITHUB_TOKEN" | helm registry login ghcr.io -u bropines --password-stdin
helm push dist/rdpanel-<version>.tgz oci://ghcr.io/bropines/charts
# 2. Make the package public once: GitHub → Packages → charts/rdpanel → settings.
# 3. Users install with:
helm install rustdesk oci://ghcr.io/bropines/charts/rdpanel --version <version>
```

In GitHub Actions the same steps work with `permissions: packages: write` and
`secrets.GITHUB_TOKEN`. Optionally sign with `cosign sign ghcr.io/bropines/charts/rdpanel:<version>`.
