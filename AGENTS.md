# AGENTS.md

GitOps homelab: a bare-metal Talos Kubernetes cluster fully managed by Flux CD. Git (`main` branch) is the source of truth — there is no local `kubectl apply`/`helm install`; changes take effect only by committing to `main` and letting Flux reconcile.

## Layout

- `k8s/` — Flux GitOps root. `k8s/flux-system/gotk-sync.yaml` sets the Flux `path: ./k8s`. Each component dir holds its release-stage manifests (`namespace.yaml`, `repository.yaml`, `release.yaml`) plus a `resources/` subdir for custom resources.
- `opentofu/` — one-time Flux bootstrap (github + flux providers). Not used after initial setup.
- `talos/` — Talos node config; holds `talosconfig`/`kubeconfig` (gitignored) plus a README.
- `telegram/` — standalone Go service (Telegram notification bot), separate module from the rest of the repo.
- `.github/scripts/flux-diff/` — Go CLI used by CI to render Helm diffs on PRs.

## Flux wiring (easy to get wrong)

The real entrypoint is `k8s/kustomization.yaml`. It references these Kustomizations:

- `k8s/infra-kustomizations/flux-releases.yaml` — stage 1 (namespaces/HelmReleases/CRDs), every entry has `wait: true`
- `k8s/infra-kustomizations/flux-resources.yaml` — stage 2 (custom resources, `dependsOn` stage 1)
- `k8s/services-kustomizations/flux-releases.yaml` — services (jellyfin, telegram-bot)

To add a component: create `k8s/infrastructure/<name>/` with its release manifests, add a `resources/` subdir if it creates custom resources, then register a release-stage `Kustomization` in `flux-releases.yaml` (and a `dependsOn` resources-stage entry in `flux-resources.yaml` when needed).

`wait: true` on stage 1 is required for `dependsOn` to gate on HelmRelease/CRD readiness (without it, `dependsOn` only waits for manifests to be applied).

Current service state: jellyfin and telegram-bot are **enabled**; `project-zomboid` has been removed.

`k8s/flux-system/gotk-sync.yaml` and `gotk-components.yaml` are Flux-generated — don't hand-edit them; re-run bootstrap if needed.

## Secrets

Secrets are delivered by **External Secrets Operator** (`k8s/infrastructure/external-secrets/`) backed by **Bitwarden Secrets Manager**. `k8s/infrastructure/external-secrets/resources/` defines the `ClusterSecretStore bitwarden` and the `ExternalSecret`s for AdGuard, Jellyfin, the Telegram bot and Grafana.

- Set up in Bitwarden: one secret per key, named `<namespace>/<KEY>` (see `resources/externalsecrets.yaml` for the full list), plus the org/project IDs in `resources/clustersecretstore.yaml`.
- **One manual bootstrap** (not GitOps-managed): the Bitwarden machine-account token Secret `external-secrets/bitwarden-access-token` (key `token`).
- Legacy `*.secret.yaml` files are gitignored and no longer consumed; the remaining ones exist locally only.

## TLS

`cert-manager` issues a self-signed bootstrap issuer → internal CA (`ClusterIssuer homelab-ca`) → wildcard `*.homelab.home.arpa` cert used by the `homelab-gateway` HTTPS listener. The Bitwarden SDK server also gets a cert from `homelab-ca`.

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
