# Flux Kustomizations Pattern

This pattern uses two root-level Flux Kustomizations to manage all infrastructure and services. Flux auto-discovers and deploys everything with proper dependency handling.

## Structure

```
k8s/
├── flux-operators-kustomizations.yaml           # Stage 1: All operators/charts/CRDs
├── flux-resources-kustomizations.yaml           # Stage 2: Custom resources (depend on Stage 1)
│
├── infrastructure/
│   ├── cert-manager/                            # Simple component
│   ├── external-dns/                            # Simple component
│   ├── intel-device-plugin/                     # Simple component
│   ├── metrics-server/                          # Simple component
│   ├── monitoring/                              # Simple component
│   ├── node-feature-discovering/                # Simple component
│   │
│   ├── metallb/                                 # Two-stage pattern
│   │   ├── operators/
│   │   │   ├── kustomization.yaml
│   │   │   ├── namespace.yaml
│   │   │   ├── release.yaml
│   │   │   └── repository.yaml
│   │   └── resources/
│   │       ├── kustomization.yaml
│   │       ├── ip-address-pool.yaml
│   │       └── l2-advertisement.yaml
│   │
│   ├── longhorn/                                # Two-stage pattern
│   │   ├── operators/
│   │   └── resources/
│   │
│   └── envoy-gateway/                           # Two-stage pattern
│       ├── operators/
│       └── resources/
│
└── services/
    ├── telegram-bot/                            # Simple service
```

## Component Types

### Simple Components

Deployed in `flux-operators-kustomizations.yaml` with single `kustomization.yaml`:
- cert-manager
- external-dns
- intel-device-plugin
- metrics-server
- monitoring
- node-feature-discovering
- telegram-bot (service)

### Two-Stage Components

Deployed across `flux-operators-kustomizations.yaml` and `flux-resources-kustomizations.yaml`:
- metallb (with `operators/` and `resources/` subdirectories)
- longhorn (with `operators/` and `resources/` subdirectories)
- envoy-gateway (with `operators/` and `resources/` subdirectories)

## How It Works

### Stage 1: flux-operators-kustomizations.yaml

All operators, charts, and CRDs are deployed in parallel:

```yaml
cert-manager-operators              → ./k8s/infrastructure/cert-manager
external-dns-operators             → ./k8s/infrastructure/external-dns
intel-device-plugin-operators       → ./k8s/infrastructure/intel-device-plugin
metrics-server-operators           → ./k8s/infrastructure/metrics-server
monitoring-operators               → ./k8s/infrastructure/monitoring
node-feature-discovering-operators → ./k8s/infrastructure/node-feature-discovering

metallb-operators                  → ./k8s/infrastructure/metallb/operators
longhorn-operators                 → ./k8s/infrastructure/longhorn/operators
envoy-gateway-operators            → ./k8s/infrastructure/envoy-gateway/operators

telegram-bot                       → ./k8s/services/telegram-bot
```

### Stage 2: flux-resources-kustomizations.yaml

Custom resources are deployed only after their operators are ready:

```yaml
metallb-resources (dependsOn: metallb-operators)
  → ./k8s/infrastructure/metallb/resources

longhorn-resources (dependsOn: longhorn-operators)
  → ./k8s/infrastructure/longhorn/resources

envoy-gateway-resources (dependsOn: envoy-gateway-operators)
  → ./k8s/infrastructure/envoy-gateway/resources
```

## Adding a Simple Component

### Step 1: Create component folder

```
infrastructure/mycomponent/
├── kustomization.yaml
├── namespace.yaml
├── release.yaml
└── repository.yaml
```

### Step 2: Create kustomization.yaml

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
metadata:
  namespace: mycomponent-system
resources:
  - namespace.yaml
  - release.yaml
  - repository.yaml
```

### Step 3: Add to flux-operators-kustomizations.yaml

```yaml
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: mycomponent-operators
  namespace: flux-system
spec:
  interval: 10m0s
  path: ./k8s/infrastructure/mycomponent
  prune: true
  sourceRef:
    kind: GitRepository
    name: flux-system
```

## Adding a Two-Stage Component

### Step 1: Create directory structure

```
infrastructure/mycomponent/
├── operators/
│   ├── kustomization.yaml
│   ├── namespace.yaml
│   ├── release.yaml
│   └── repository.yaml
└── resources/
    ├── kustomization.yaml
    └── custom-resource.yaml
```

### Step 2: Create operators/kustomization.yaml

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
metadata:
  namespace: mycomponent-system
resources:
  - namespace.yaml
  - release.yaml
  - repository.yaml
```

### Step 3: Create resources/kustomization.yaml

```yaml
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
metadata:
  namespace: mycomponent-system
resources:
  - custom-resource.yaml
```

### Step 4: Add to flux-operators-kustomizations.yaml

```yaml
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: mycomponent-operators
  namespace: flux-system
spec:
  interval: 10m0s
  path: ./k8s/infrastructure/mycomponent/operators
  prune: true
  sourceRef:
    kind: GitRepository
    name: flux-system
```

### Step 5: Add to flux-resources-kustomizations.yaml

```yaml
---
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: mycomponent-resources
  namespace: flux-system
spec:
  interval: 10m0s
  path: ./k8s/infrastructure/mycomponent/resources
  prune: true
  sourceRef:
    kind: GitRepository
    name: flux-system
  dependsOn:
    - name: mycomponent-operators
```

## Benefits

✓ **Zero kustomization.yaml files in k8s root** — Everything managed by Flux Kustomizations  
✓ **Automatic discovery** — Flux discovers and deploys all kustomizations  
✓ **Clean structure** — Simple vs two-stage components clearly separated  
✓ **No JSON patches** — Pure declarative structure  
✓ **Eliminates race conditions** — Dependencies explicitly defined  
✓ **Scalable** — Same pattern works for all components  
✓ **Easy to understand** — Clear separation of operators and resources  
