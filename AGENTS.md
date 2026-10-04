# AGENTS.md

GitOps homelab: a bare-metal Talos Kubernetes cluster fully managed by Flux CD. Git (`main` branch) is the source of truth — there is no local `kubectl apply`/`helm install`; changes take effect only by committing to `main` and letting Flux reconcile.

## Layout

- `k8s/` — Flux GitOps root. `k8s/flux-system/gotk-sync.yaml` sets the Flux `path: ./k8s`.
- `opentofu/` — one-time Flux bootstrap (github + flux providers). Not used after initial setup.
- `talos/` — Talos node config; holds `talosconfig`/`kubeconfig` (gitignored) plus a README.
- `telegram/` — standalone Go service (Telegram notification bot), separate module from the rest of the repo.
- `.github/scripts/flux-diff/` — Go CLI used by CI to render Helm diffs on PRs.

## Flux wiring (easy to get wrong)

The real entrypoint is `k8s/kustomization.yaml`. It references these Kustomizations:

- `k8s/infra-kustomizations/flux-operators-kustomizations.yaml` — stage 1 (HelmReleases/CRDs)
- `k8s/infra-kustomizations/flux-resources-kustomizations.yaml` — stage 2 (custom resources, `dependsOn` stage 1)
- `k8s/services-kustomizations/flux-operators.yaml` — services (jellyfin, telegram-bot)

`k8s/README-FLUX-KUSTOMIZATIONS.md` is **stale**: it describes `flux-operators-kustomizations.yaml`/`flux-resources-kustomizations.yaml` living at the `k8s/` root and a `k8s/infrastructure/*` flat layout. The current files live under `k8s/infra-kustomizations/` and `k8s/services-kustomizations/`. Trust the YAML, not that README.

Two-stage components (metallb, longhorn, envoy-gateway) split `operators/` and `resources/` subdirs. To add a component, create its directory and register a `Kustomization` entry in the matching `*-kustomizations.yaml` — there are no per-directory kustomization files in the `k8s/` root.

Current service state (do not trust inline comments): jellyfin and telegram-bot are **enabled** via `services-kustomizations/flux-operators.yaml`; `project-zomboid` is **disabled** (`services-kustomizations/flux-patches.yaml` is commented out in the root kustomization and `zomboid-operators` does not exist).

`k8s/flux-system/gotk-sync.yaml` and `gotk-components.yaml` are Flux-generated — don't hand-edit them; re-run bootstrap if needed.

## Secrets

`*.secret.yaml` / `*secret.yaml` files are **gitignored and not tracked**, but exist locally with real credentials (Telegram token, AdGuard password, etc.). Never commit them; fresh clones won't have them. Examples: `k8s/infrastructure/external-dns/adguard-secret.yaml`, `k8s/services/telegram-bot/telegram.secret.yaml`.

## Commands

```bash
# Cluster access (or `direnv allow`; .envrc is gitignored)
export TALOSCONFIG=talos/talosconfig
export KUBECONFIG=talos/kubeconfig

# Flux bootstrap (one-time; needs opentofu/terraform.tfvars — gitignored)
cd opentofu && tofu init && tofu apply

# Flux status / force reconcile
flux get kustomization
flux reconcile kustomization flux-system --with-source

# Validate manifests locally
kustomize build k8s

# Go modules (two separate modules)
go build ./...          # in telegram/
go build ./...          # in .github/scripts/flux-diff/
```

There is no test suite or lint/typecheck config in this repo.

## CI

`.github/workflows/flux-diff.yaml` runs on PRs touching `k8s/**`. It builds the Go `flux-diff` tool (`.github/scripts/flux-diff/main.go`) and posts a rendered Helm manifest diff as a PR comment. The tool shells out to `helm` and `diff` and resolves `HelmRelease` values from inline `spec.values` plus `valuesFrom` ConfigMap files. The compiled binary is gitignored (`.github/scripts/flux-diff/flux-diff`).

## Renovate

`renovate.json` disables docker image bumps inside `k8s/flux-system/gotk-components.yaml` (Flux manages those holistically) and scopes flux/kubernetes managers to `k8s/**`.

## Telegram bot

Go 1.26 module (`github.com/dani/homelab/telegram`), built `CGO_ENABLED=0` into a `scratch` image, published as `danifuu/telegram-bot:latest`. Runtime env: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` (required), `WEBHOOK_SECRET` (optional). Exposes `POST /notify` and `GET /healthz`. Deployed by `k8s/services/telegram-bot/deployment.yaml` with `envFrom` secret `telegram-bot-secret`.

## Network context

Cluster nodes `192.168.1.5` (control plane) and `192.168.1.6` (worker); services are published under `*.home.arpa` via ExternalDNS → AdGuard Home (Raspberry Pi, `192.168.1.2`). GitHub org is `danifuuu`.
