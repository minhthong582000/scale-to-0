# Scale-to-zero lab — local kind cluster with Istio, KEDA, Prometheus and a demo app.

SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

CLUSTER_NAME ?= scale-to-zero
K8S_DIR      := deploy/k8s
CHART_DIR    := $(K8S_DIR)/charts
KIND_CONFIG  := $(K8S_DIR)/kind/cluster.yaml

ISTIO_NS := istio-system
KEDA_NS  := keda
PROM_NS  := monitoring
APP_NS   := demo
SYSTEM_NS := kube-system

CHARTS := istio-base istiod istio-gateway keda prometheus metrics-server

WAIT_TIMEOUT ?= 300s

# $(call helm_install,<release>,<chart dir>,<namespace>)
# Each chart's values.yaml is also passed with -f: Helm only honours `null` as
# "delete this key" for values supplied on the command line, so without it the
# upstream CPU limits we drop would merge back in.
define helm_install
helm upgrade --install $(1) $(CHART_DIR)/$(2) -n $(3) --create-namespace \
		-f $(CHART_DIR)/$(2)/values.yaml --wait --timeout $(WAIT_TIMEOUT)
endef

.PHONY: help
help: ## Show available targets
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

## --------------------------------------------------------------------------
## Full stack
## --------------------------------------------------------------------------

.PHONY: up
up: cluster deps istio keda metrics-server prometheus webapp status ## Create the cluster and deploy everything

.PHONY: down
down: cluster-delete ## Delete the whole cluster

## --------------------------------------------------------------------------
## Cluster
## --------------------------------------------------------------------------

.PHONY: cluster
cluster: ## Create the kind cluster (no-op if it already exists)
	@if kind get clusters 2>/dev/null | grep -qx '$(CLUSTER_NAME)'; then \
		echo ">> kind cluster '$(CLUSTER_NAME)' already exists"; \
	else \
		echo ">> creating kind cluster '$(CLUSTER_NAME)'"; \
		kind create cluster --config $(KIND_CONFIG) --wait 120s; \
	fi
	@kubectl cluster-info --context kind-$(CLUSTER_NAME)

.PHONY: cluster-delete
cluster-delete: ## Delete the kind cluster
	kind delete cluster --name $(CLUSTER_NAME)

## --------------------------------------------------------------------------
## Charts
## --------------------------------------------------------------------------

.PHONY: deps
deps: ## Fetch upstream chart dependencies declared in each Chart.yaml
	@for c in $(CHARTS); do \
		echo ">> helm dependency update $(CHART_DIR)/$$c"; \
		helm dependency update $(CHART_DIR)/$$c; \
	done

## --------------------------------------------------------------------------
## Components
## --------------------------------------------------------------------------

.PHONY: istio
istio: ## Install Istio (base + istiod + ingress gateway)
	$(call helm_install,istio-base,istio-base,$(ISTIO_NS))
	$(call helm_install,istiod,istiod,$(ISTIO_NS))
	$(call helm_install,istio-ingressgateway,istio-gateway,$(ISTIO_NS))

.PHONY: keda
keda: ## Install KEDA
	$(call helm_install,keda,keda,$(KEDA_NS))

.PHONY: prometheus
prometheus: ## Install the Prometheus Operator stack
	$(call helm_install,prometheus,prometheus,$(PROM_NS))

.PHONY: metrics-server
metrics-server: ## Install metrics-server (resource metrics API for HPA)
	$(call helm_install,metrics-server,metrics-server,$(SYSTEM_NS))

.PHONY: webapp
webapp: ## Deploy the demo app and its Istio routing
	@# The namespace goes first: `kubectl apply -f <dir>` orders files alphabetically.
	kubectl apply -f $(K8S_DIR)/webapp/namespace.yaml
	kubectl apply -f $(K8S_DIR)/webapp/
	kubectl rollout status deployment/webapp -n $(APP_NS) --timeout=$(WAIT_TIMEOUT)

## --------------------------------------------------------------------------
## Teardown of individual components (keeps the cluster)
## --------------------------------------------------------------------------

.PHONY: webapp-delete
webapp-delete: ## Remove the demo app
	kubectl delete -f $(K8S_DIR)/webapp/ --ignore-not-found

.PHONY: prometheus-delete
prometheus-delete: ## Uninstall Prometheus
	helm uninstall prometheus -n $(PROM_NS) --ignore-not-found

.PHONY: metrics-server-delete
metrics-server-delete: ## Uninstall metrics-server
	helm uninstall metrics-server -n $(SYSTEM_NS) --ignore-not-found

.PHONY: keda-delete
keda-delete: ## Uninstall KEDA
	helm uninstall keda -n $(KEDA_NS) --ignore-not-found

.PHONY: istio-delete
istio-delete: ## Uninstall Istio
	helm uninstall istio-ingressgateway -n $(ISTIO_NS) --ignore-not-found
	helm uninstall istiod -n $(ISTIO_NS) --ignore-not-found
	helm uninstall istio-base -n $(ISTIO_NS) --ignore-not-found

## --------------------------------------------------------------------------
## Inspection
## --------------------------------------------------------------------------

.PHONY: status
status: ## Show pods across all lab namespaces and the local URLs
	@for ns in $(ISTIO_NS) $(KEDA_NS) $(PROM_NS) $(APP_NS); do \
		echo "== $$ns =="; \
		kubectl get pods -n $$ns -o wide 2>/dev/null || true; \
		echo; \
	done
	@$(MAKE) --no-print-directory urls

.PHONY: urls
urls: ## Print the URLs exposed on localhost
	@echo "webapp     -> http://localhost:8080/"
	@echo "prometheus -> http://localhost:9090/"

.PHONY: smoke
smoke: ## Curl the demo app through the Istio ingress gateway
	curl -sS -o /dev/null -w 'webapp http %{http_code}\n' http://localhost:8080/
	curl -sS -o /dev/null -w 'prometheus http %{http_code}\n' http://localhost:9090/-/ready

.PHONY: lint
lint: ## Render every chart and manifest locally without touching a cluster
	@for c in $(CHARTS); do \
		echo ">> helm template $(CHART_DIR)/$$c"; \
		helm template $$c $(CHART_DIR)/$$c -f $(CHART_DIR)/$$c/values.yaml >/dev/null || exit 1; \
	done
	@# Server-side dry-run needs a live cluster; fall back to a YAML syntax check.
	@if kubectl cluster-info >/dev/null 2>&1; then \
		kubectl apply --dry-run=server -f $(K8S_DIR)/webapp/ >/dev/null; \
	else \
		echo ">> no cluster reachable: checking manifest YAML syntax only"; \
		python3 -c "import sys,yaml; [list(yaml.safe_load_all(open(f))) for f in sys.argv[1:]]" \
			$(K8S_DIR)/webapp/*.yaml; \
	fi
	@echo ">> all charts and manifests render"
