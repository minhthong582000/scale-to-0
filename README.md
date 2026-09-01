# scale-to-0 (WIP)

## How it works

```mermaid
flowchart TD
    req([request])
    ig["ingress"]
    wl["workload<br/>0 .. N replicas"]
    sc["scaler<br/>per workload: last seen, request rate"]
    au["autoscaler"]

    req --> ig
    ig --> wl
    ig -.->|traffic counters| sc
    wl -.->|traffic counters| sc
    sc -->|"active? how busy?"| au
    au -->|sets replica count| wl
```

```mermaid
sequenceDiagram
    participant caller
    participant ingress
    participant scaler
    participant autoscaler
    participant workload

    Note over workload: 0 replicas
    caller->>ingress: request
    ingress->>workload: attempt
    workload-->>ingress: rejected, no endpoint
    ingress->>scaler: request seen
    scaler->>autoscaler: workload is active
    autoscaler->>workload: scale 0 to 1
    Note over workload: starting
    loop until it answers or the deadline passes
        ingress->>workload: retry
    end
    workload-->>ingress: response
    ingress-->>caller: response
```

## Running it

```bash
make up      # create the cluster and install everything
make down    # delete the cluster
make help    # every target
make test    # Go tests
```
