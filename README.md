# scale-to-0

A Kubernetes scale-to-zero example: idle applications scale down to no running pods, and
scale back up when a request arrives. Built on Istio for the request path, KEDA for the
scaling decision, and Prometheus for the traffic signal that drives it. Runs locally on
kind.

## How it fits together

Traffic reaches the application through the Istio ingress gateway. Each application pod
runs an Envoy sidecar that reports per-service request counters, which Prometheus scrapes.
KEDA reads those counters and scales the deployment: down to zero when a service has been
idle, and back up when traffic returns.

Prometheus runs under the Prometheus Operator, so scrape targets are declared as
`ServiceMonitor` and `PodMonitor` resources rather than static scrape config. Alongside
the application and Istio metrics it collects cAdvisor and kubelet metrics, Kubernetes
object state, and node metrics. metrics-server serves the resource metrics API that HPA
and VPA read from.

## Components

| Component | Version | Namespace | Notes |
|---|---|---|---|
| kind | cluster `scale-to-zero` | — | single node, NodePorts mapped to localhost |
| Istio | 1.30.4 | `istio-system` | base + istiod + ingress gateway, `enablePrometheusMerge` on |
| KEDA | 2.20.2 | `keda` | operator, metrics apiserver, admission webhooks |
| kube-prometheus-stack | chart 88.6.2 | `monitoring` | operator, Prometheus, kube-state-metrics, node-exporter; 2h retention, emptyDir |
| metrics-server | chart 3.14.0 | `kube-system` | resource metrics API for HPA/VPA |
| webapp (podinfo) | 6.9.2 | `demo` | sidecar injected, routed through the gateway |

Grafana, Alertmanager and the default alerting rules are turned off. So are the scrape
targets for the controller-manager, scheduler, etcd and kube-proxy — on kind those bind
to localhost, so their targets would never come up.

Everything is sized to run on a single laptop-scale node: one replica per component, no
autoscaling, and no persistent volumes.

## Requirements

`kind`, `kubectl`, `helm`, and a running Docker daemon. No `istioctl` needed — Istio is
installed from its Helm charts.

## Usage

```bash
make up      # create the cluster and install everything
make status  # pods per namespace + local URLs
make smoke   # curl the app and Prometheus
make down    # delete the cluster
```

Individual components can be (re)installed on their own: `make istio`, `make keda`,
`make prometheus`, `make metrics-server`, `make webapp`, `make monitoring`. Each is a `helm upgrade --install` or `kubectl apply`,
so re-running is safe. `make lint` renders every chart and manifest without touching a
cluster. `make help` lists all targets.

To change an upstream chart version, edit the dependency in the relevant
`deploy/k8s/charts/*/Chart.yaml` and re-run `make deps`.

## URLs

| URL | What |
|---|---|
| http://localhost:8080/ | webapp, through the Istio ingress gateway |
| http://localhost:9090/ | Prometheus UI |

Both go through kind `extraPortMappings` onto NodePorts (30080 and 30090), so no
`kubectl port-forward` is needed.

## Layout

```
Makefile                        all deploy/teardown targets
deploy/k8s/kind/cluster.yaml    kind cluster + port mappings
deploy/k8s/charts/              one wrapper chart per release
  istio-base/                     Chart.yaml + values.yaml
  istiod/
  istio-gateway/
  keda/
  prometheus/                     also ships the Istio sidecar PodMonitor
  metrics-server/
deploy/k8s/webapp/              demo app: namespace, deployment, service, Gateway/VirtualService,
                                ServiceMonitor
```

Each directory under `deploy/k8s/charts/` is a small chart that declares its upstream
chart as a dependency in `Chart.yaml` — repository URL and pinned version live there, not
in the `Makefile` — and carries the overrides in `values.yaml`. `make deps` runs
`helm dependency update` for each, and the install targets point at the local chart
directory.

## Scrape targets

| Monitor | Defined in | Scrapes |
|---|---|---|
| `webapp` ServiceMonitor | `deploy/k8s/webapp/` | the application's own `/metrics` |
| `istio-proxy` PodMonitor | the Prometheus chart | Envoy sidecar stats in every namespace, including `istio_requests_total` |
| stack defaults | the Prometheus chart | cAdvisor and kubelet, kube-state-metrics, node-exporter, API server, CoreDNS |

## The scaling signal

The Envoy sidecar in each application pod exposes per-service request counters, collected
by the `istio-proxy` PodMonitor. After sending some traffic, the counter that drives the
scaling decision is available:

```bash
curl -s http://localhost:8080/ >/dev/null
curl -sG http://localhost:9090/api/v1/query \
  --data-urlencode 'query=sum by (destination_service_name) (istio_requests_total{destination_service_name="webapp"})'
```
