# Runbook — dedicated etcd SSD on the control-plane node

**Status:** executed successfully on 2026-10-18 (see "Execution result")
**Date:** 2026-10-17 (executed 2026-10-18)
**Audience:** an infrastructure agent/operator with `talosctl` and `kubectl` access to the homelab cluster.
**Scope:** move etcd's data directory on `talos-cp-01` from the 7200 rpm HDD to a dedicated 64 GB SSD, with no Talos reinstall and no Longhorn data migration.

---

## Execution result (2026-10-18)

- SSD detected as `/dev/nvme0n1` (62 GB, FORESEE E2M2 NVMe). It was **not empty**: it
  carried a SteamOS A/B layout (ESP, efi-A/B, rootfs-A/B, var-A/B, 50 GB `home`). It was
  wiped with `talosctl wipe disk nvme0n1` after the data was confirmed disposable.
- The `machine.disks` patch was applied staged, the node rebooted, and `/var/lib/etcd`
  came up on `/dev/nvme0n1p1` (XFS). The install disk stayed `/dev/sda`.
- etcd was restored from the pre-migration snapshot and the API returned.
- WAL fsync p99 dropped from **178 ms to ~13-14 ms** (avg ~4 ms, 97% <= 8 ms); backend
  commit p99 dropped from **187 ms to ~8-15 ms**. The remaining tail is the consumer
  SSD's fsync latency, not contention with Longhorn.
- One Longhorn volume (`jellyfin-media`) ended up `degraded`, queued behind the
  serialized rebuild limit; it heals in the background.
- A worker kube-proxy regression (pods could not reach `10.96.0.1:443`) blocked
  Longhorn's volume status updates. Restarting kube-proxy on the worker fixed it. See §8.

---

## 0. Read this first

The cluster is a 2-node Talos/Kubernetes homelab:

| Node | Role | IP | Boot/OS disk | Longhorn data |
|---|---|---|---|---|
| `talos-cp-01` | control plane (single etcd member) | 192.168.1.5 | `/dev/sda` — HGST 500 GB 7200 rpm HDD | `/var/lib/longhorn` on the same HDD |
| `talos-worker-01` | worker | 192.168.1.6 | `/dev/sda` — same model HDD | `/var/lib/longhorn` on the same HDD |

Measured problem (after the 2026-10-17 incident):

```
etcd_disk_wal_fsync p99       = 0.977 s   (healthy target: < 10 ms)
etcd_disk_backend_commit p99  = 1.334 s
apiserver p99                 = ~22 s during rebuilds
kube-scheduler / kube-controller-manager = 95 / 91 restarts before the lease fix
```

The root cause is that etcd's WAL fsync and Longhorn's rebuild/replication traffic share the same HDD. The cluster is now stabilized by `leader-elect=false` and serialized Longhorn rebuilds, but the fsync latency remains ~100× the recommended value.

**This runbook chooses the lowest-risk fix: use the 64 GB SSD as a dedicated etcd data volume on the control-plane node.** It does not reinstall Talos and does not touch Longhorn's data path.

A secondary option (install Talos itself on the SSD) is documented in [Appendix A](#appendix-a--optional-install-talos-on-the-ssd) for completeness. Do not use it unless the primary plan is rejected by the validation in §4.

### Why not the worker?

The worker has no etcd. An SSD there would speed up Prometheus/Loki/images locally but leave the API server and scheduler waiting on the CP's HDD. That does not fix the cluster-wide instability. Put the SSD in the CP.

### Why not put Longhorn data on the 64 GB SSD?

Current per-node usage on `/var` is ~20% of ~497 GB (~102 GB): ~63 GB is Longhorn replica data and ~39 GB is Talos + containerd images + logs. A 64 GB SSD cannot hold the OS and the Longhorn replicas. Longhorn must stay on the HDD.

---

## 1. Decision summary

- **Target node:** `talos-cp-01` (192.168.1.5).
- **SSD use:** dedicated XFS partition mounted at `/var/lib/etcd`.
- **Expected result:** etcd WAL fsync and backend commit drop to SSD latency; the control plane stops being IO-bound.
- **Not changed:** Talos install disk (still `/dev/sda`), Longhorn disk path, worker, workloads.
- **Downtime:** one CP reboot (~2–5 min) for the staged config, then the etcd restore (a few minutes). The Kubernetes API is unavailable during the reboot and restore. The worker keeps running pods.
- **Reversibility:** high. The old etcd data on the HDD is shadowed, not deleted. Removing the disk config and rebooting returns to the pre-migration state (at the snapshot point).

---

## 2. Pre-flight (do not skip)

Run from a shell in the repo with:

```bash
export TALOSCONFIG=/mnt/c/Users/dani/Documents/repos/homelab/talos/talosconfig
export KUBECONFIG=/mnt/c/Users/dani/Documents/repos/homelab/talos/kubeconfig
```

### 2.1 Verify cluster health

```bash
kubectl get nodes
flux get kustomizations
kubectl -n longhorn-system get volumes.longhorn.io \
  -o custom-columns='NAME:.metadata.name,ROBUST:.status.robustness'
kubectl -n kube-system get pods | grep -E 'kube-scheduler|kube-controller-manager|kube-apiserver'
```

Proceed only if:

- both nodes are `Ready`;
- all Flux Kustomizations are `True`;
- **all Longhorn volumes are `healthy`** (no `degraded`, `faulting`, or active rebuild);
- the control-plane pods are `Running` and not restarting.

If Longhorn is rebuilding, wait. Do not start this during a rebuild.

### 2.2 Take an etcd snapshot

```bash
mkdir -p ~/etcd-backups
SNAP=~/etcd-backups/etcd-$(date -u +%Y%m%dT%H%M%SZ).db
talosctl -n 192.168.1.5 etcd snapshot "$SNAP"
ls -lh "$SNAP"
```

Expected: `etcd snapshot saved to ...` plus a hash, revision and size. **Copy this file off the machine** (another host, NAS, Bitwarden attachment, etc.). It is the recovery point for the entire cluster state.

Do not proceed without a snapshot that reports a valid hash and a non-zero size.

### 2.3 Back up the machine configuration

```bash
talosctl -n 192.168.1.5 get mc -o yaml > /tmp/mc-cp-before.yaml
talosctl -n 192.168.1.6 get mc -o yaml > /tmp/mc-worker-before.yaml
```

These contain cluster secrets. Keep them private; do not commit them. Note the current `machine.install.disk` (it should be `/dev/sda`).

### 2.4 Back up Longhorn metadata and critical data

Even though this plan does not touch Longhorn, take a cheap safety net:

```bash
kubectl get pvc -A -o yaml > /tmp/pvc-before.yaml
kubectl get pv -o yaml > /tmp/pv-before.yaml
kubectl -n longhorn-system get volumes.longhorn.io -o yaml > /tmp/lh-volumes-before.yaml
kubectl -n longhorn-system get nodes.longhorn.io -o yaml > /tmp/lh-nodes-before.yaml
```

If a Longhorn backup target is configured, trigger a backup of at least `jellyfin-config`, `jellyfin-media`, and `opengym-data`. If not, accept that the worker's replica is the only other copy during the migration; the worker must stay up.

### 2.5 Confirm the etcd mountpoint is free

```bash
talosctl -n 192.168.1.5 get mountstatus | grep -E 'ETCD|/var/lib/etcd'
```

Expected: the `ETCD` pseudo-mount and `/var/lib/etcd` under `/var` (the HDD). After the change, `/var/lib/etcd` must appear as a separate filesystem.

### 2.6 Hardware check

The HP ProDesk 600 G2 Desktop Mini has:

- one **M.2 2280 PCIe x4** slot (use an **NVMe** SSD; a SATA M.2 may not be detected by that slot);
- one **2.5" bay**, currently occupied by the HDD.

The 64 GB SSD must be an **M.2 NVMe** drive for the M.2 slot. If it is a 2.5" SATA or an M.2 SATA drive, stop and re-evaluate: the 2.5" bay is in use, and a USB-attached SSD is not recommended for etcd.

Before touching the cluster, install the SSD and verify it is visible:

```bash
talosctl -n 192.168.1.5 get disks
```

Expected: a new disk (typically `nvme0n1`, 64 GB) alongside `sda` (500 GB HGST). **Verify the model/size**; never assume the device name. If only `sda` appears, the slot/drive is not detected — stop and fix the hardware.

Do this **before** taking the etcd snapshot if possible, so a hardware problem does not consume the maintenance window. Installing the SSD requires a power-off; the cluster can be up while the node is off? No — the CP is the only control plane. Plan the physical install and the migration in the same window, or install the SSD, boot back into the existing HDD install, verify `nvme0n1`, and only then schedule the migration.

> Practical order: install the SSD first, boot the existing system from the HDD, confirm `talosctl get disks` shows `nvme0n1`. Then do §2.1–§2.5. This splits the hardware risk from the etcd risk.

---

## 3. Apply the disk config (staged)

Create `/mnt/c/Users/dani/Documents/repos/homelab/talos/config-etcd-ssd-patch.yaml`:

```yaml
# Dedicated etcd data volume on the control-plane node.
# Talos creates a partition on the SSD and mounts it at /var/lib/etcd.
# The existing /var/lib/etcd on the HDD remains untouched (shadowed).
machine:
  disks:
    - device: /dev/nvme0n1
      partitions:
        - mountpoint: /var/lib/etcd
```

Before applying, replace `/dev/nvme0n1` if §2.6 showed a different device. Do not add a `size`; omit it to use the full disk.

> **v1.12 caveat:** `machine.disks` is deprecated and hidden from the v1.12 config
> reference (`docgen:nodoc`; "Deprecated: Use UserVolumeConfig instead"). It is still
> processed by the legacy `UserDiskConfigController` in v1.12-v1.14 and worked for this
> migration. The built-in `ETCD` volume is a *directory* volume, so the legacy partition
> shadows it. This is not a supported configuration: verify §4 after every reboot. The
> supported alternative (`VolumeConfig` named `ETCD` with a `diskSelector`) only exists
> from v1.14 and is only honored at provisioning time, so it cannot be applied to an
> already-installed node without reinstalling.

Stage it (do not reboot yet):

```bash
talosctl -n 192.168.1.5 patch mc --mode=staged \
  --patch @/mnt/c/Users/dani/Documents/repos/homelab/talos/config-etcd-ssd-patch.yaml
```

Verify the staged config contains the disk entry:

```bash
talosctl -n 192.168.1.5 get mc persistent -o yaml | grep -A6 'disks:'
```

Expected: a `disks:` block with `/dev/nvme0n1` and `mountpoint: /var/lib/etcd`. If it is absent or wrong, **do not reboot**; remove the staged config or correct it.

Risks at this point:

- `machine.disks` **formats** the target disk. The SSD is new/empty; if it contains data, back it up first.
- The device name must be the SSD, not `sda`. Formatting `sda` would destroy the OS and Longhorn data. Triple-check the model/size.

---

## 4. Reboot and validate the mount BEFORE touching etcd

```bash
talosctl -n 192.168.1.5 reboot
```

Wait for the Talos API (not necessarily the Kubernetes API). Then:

```bash
talosctl -n 192.168.1.5 get mountstatus | grep -E 'ETCD|/var/lib/etcd'
```

Expected: `/var/lib/etcd` with `SOURCE` on the SSD (e.g. `/dev/nvme0n1p1`) and a filesystem (XFS), **not** the HDD `/dev/sda4`.

Also check:

```bash
talosctl -n 192.168.1.5 get disks | grep -E 'sda|nvme'
talosctl -n 192.168.1.5 service etcd
```

- If the mount is **present**: proceed to §5. The etcd data directory is now empty; the Kubernetes API will be down until the restore.
- If the mount is **absent** (the config was accepted but the volume did not mount): **do not attempt a restore.** The etcd service is still using the old HDD data, so the cluster should recover on its own. Remove the staged disk config (see §7) and reboot. Report the failure; do not continue.

### Decision gate

| Observation | Action |
|---|---|
| `/var/lib/etcd` mounted from SSD, etcd `Preparing`/`Waiting` | Continue to §5 (restore) |
| `/var/lib/etcd` not mounted, etcd `Running` on HDD | Abort, roll back (§7), investigate |
| `/var/lib/etcd` mounted, etcd `Running` and API up | Unexpected: the empty SSD etcd may have started a new cluster. Abort/roll back, investigate |
| Node does not come back | Physical/BIOS issue; use the HDD boot entry, then roll back |

---

## 5. Restore etcd from the snapshot

Confirm the etcd service is waiting for a bootstrap (empty data dir):

```bash
talosctl -n 192.168.1.5 service etcd
```

Expected state: `Preparing` (or `Waiting`). Then restore:

```bash
talosctl -n 192.168.1.5 bootstrap \
  --recover-from="$SNAP"
```

Where `$SNAP` is the snapshot from §2.2 (or its copied path). Expected output includes the hash/revision and `recovering from snapshot`.

> If `bootstrap` returns `FailedPrecondition: bootstrap is not available yet`, the node is
> still booting. Wait until `talosctl get machinestatus` leaves `booting` and
> `talosctl service etcd` reports `Preparing`, then retry.

Wait for the Kubernetes API:

```bash
for i in $(seq 1 60); do
  kubectl get --raw /readyz && break
  sleep 5
done
kubectl get nodes
```

Then watch the control plane settle:

```bash
kubectl -n kube-system get pods -o wide | grep -E 'kube-apiserver|kube-scheduler|kube-controller-manager'
kubectl -n kube-system get pods -o custom-columns='NAME:.metadata.name,RESTARTS:.status.containerStatuses[0].restartCount'
```

The worker should re-register. Flux will reconcile automatically (GitRepository interval 1m). Do not force reconciles until the API is stable.

If the bootstrap fails:

- `etcd already bootstrapped` → the etcd service started with an empty data dir instead of waiting. Abort/roll back (§7); do not attempt to wipe the HDD.
- `hash mismatch` / corrupt snapshot → the snapshot is bad. Roll back and retake a snapshot while the cluster is healthy; do not proceed.
- API never returns → check `talosctl -n 192.168.1.5 service etcd` and the Talos logs. Roll back if not resolved quickly.

---

## 6. Post-migration verification

### 6.1 etcd is on the SSD

```bash
talosctl -n 192.168.1.5 get mountstatus | grep /var/lib/etcd
```

### 6.2 etcd latency dropped

Prometheus is scraped via `kube-etcd`; query it directly (port-forward if needed):

```bash
kubectl -n monitoring port-forward svc/kube-prometheus-stack-prometheus 9090:9090 &
# in another shell:
curl -s 'http://localhost:9090/api/v1/query?query=histogram_quantile(0.99,rate(etcd_disk_wal_fsync_duration_seconds_bucket%5B5m%5D))'
curl -s 'http://localhost:9090/api/v1/query?query=histogram_quantile(0.99,rate(etcd_disk_backend_commit_duration_seconds_bucket%5B5m%5D))'
```

Success criteria:

- WAL fsync p99 **< 10 ms** (ideally < 5 ms);
- backend commit p99 **< 25 ms**;
- `apiserver_request_duration_seconds` p99 (excluding WATCH) well below 5 s;
- no new `kube-scheduler` / `kube-controller-manager` restarts.

The `EtcdFsyncSlow` / `EtcdApplySlow` alerts should resolve. If they still fire, the mount or the disk is not what we think it is — re-check §4.

> Measured on 2026-10-18: WAL fsync p99 ~13-14 ms (avg ~4 ms, 97% <= 8 ms), backend
> commit p99 ~8-15 ms. The residual tail is the consumer SSD's fsync latency; getting
> below 10 ms p99 would need a DRAM-equipped NVMe, not more configuration.

### 6.3 Cluster health

```bash
kubectl get nodes
flux get kustomizations
kubectl -n longhorn-system get volumes.longhorn.io \
  -o custom-columns='NAME:.metadata.name,ROBUST:.status.robustness'
kubectl -n telegram-bot get pods
kubectl -n monitoring get pods | grep -E 'alertmanager|prometheus'
```

Expected: nodes Ready, Flux True, Longhorn `healthy` (this plan does not touch it), bot/Alertmanager running.

### 6.4 Persist the change

The machine config patch is applied to the live node, but it is **not** in Git (Talos machine configs are gitignored). For reproducibility:

- `talos/config-etcd-ssd-patch.yaml` is tracked in Git;
- the applied device (`/dev/nvme0n1`) and mountpoint (`/var/lib/etcd`) are recorded there;
- pre/post-migration snapshots are in `~/etcd-backups/` — copy them off-host; they contain cluster secrets.

---

## 7. Rollback

The old etcd data is still on the HDD at `/var/lib/etcd` (shadowed while the SSD is mounted). Rollback is a config revert plus a reboot.

1. Remove the `machine.disks` entry from the machine config. The preferred method is `talosctl edit mc` (staged), delete the `machine.disks` block, save, and reboot. If an automated patch is required, apply the pre-change machine config **without** an install-disk change (do not use `apply-config` with a config that could trigger a reinstall; prefer a patch that removes the disk entry).

2. Reboot:

   ```bash
   talosctl -n 192.168.1.5 reboot
   ```

3. After the reboot, `/var/lib/etcd` resolves to the HDD again; etcd starts from the pre-migration data (at the point of the snapshot). The SSD stays attached but unused (or is removed).

4. Verify the API and cluster state. Any writes made after the snapshot are lost. If the snapshot was taken immediately before the migration, the loss is limited to the migration window.

If the node will not boot from the HDD, use the BIOS boot menu (F9/F10 on the HP) to select the HDD entry; the old Talos install is intact.

**Do not** run `talosctl reset`, wipe the HDD, or reformat `/dev/sda` during a rollback.

---

## 8. Troubleshooting

| Symptom | Check | Action |
|---|---|---|
| SSD not visible | `talosctl get disks` | Reseat the M.2; verify NVMe (not SATA M.2); BIOS detects it |
| Staged config not applied | `talosctl get mc persistent -o yaml \| grep -A6 disks` | Re-apply the patch; ensure `--mode=staged` and a reboot |
| `/var/lib/etcd` still on `sda4` | `talosctl get mountstatus` | The volume did not mount. Roll back; check the device/mountpoint and Talos logs |
| etcd will not start | `talosctl service etcd`, `talosctl logs etcd` | Check the mount; if the data dir is empty and it refuses, restore from the snapshot |
| Bootstrap says already bootstrapped | `talosctl service etcd`, `etcd members` | The empty etcd initialized. Roll back; the old data is on the HDD |
| API up but slow | etcd fsync query | Confirm the mount is the SSD; check `iostat`/node-exporter for `sda` vs `nvme0n1` |
| Old HDD health | Longhorn node health data | The HDD has 64 reallocated sectors and ~40k power-on hours; plan replacement |
| Pods cannot reach `10.96.0.1:443`; Longhorn status updates fail; kube-state-metrics crash-loops | `kubectl -n longhorn-system exec <longhorn-manager> -- curl -sk -m 4 https://10.96.0.1:443/version` | Restart kube-proxy on the affected node (`kubectl -n kube-system delete pod <kube-proxy-pod>`); re-test. This happened on the worker after the CP reboot. |
| Longhorn volume stuck `detaching`/`faulted`; replica `desire=stopped` but `state=running` | Longhorn manager logs; HTTP errors to `10.96.0.1:443` | Fix pod -> ClusterIP first; then restart the instance-manager (and manager if needed). Do not delete the replica holding the newest data. |

---

## 9. What this does NOT fix

- **OS/containerd/images/kubelet logs/audit logs remain on the HDD.** The etcd fsync path is fixed, but container image pulls, log writes and the apiserver audit log still contend with Longhorn. If the API is still slow after §6, the next step is [Appendix A](#appendix-a--optional-install-talos-on-the-ssd).
- **Longhorn rebuilds remain slow** (HDD). They are now serialized and no longer share a disk with etcd.
- **Single control-plane / single etcd member** remains a single point of failure. A second CP/etcd member (or a 3-node cluster) is a separate project.
- **The 500 GB HDDs are aging** (64 reallocated sectors, 39,968 power-on hours). A 500 GB SSD per node is the long-term fix; the 64 GB SSD is a targeted etcd fix.

---

## Appendix A — Optional: install Talos on the SSD

Use this only if the dedicated-etcd plan is rejected (or if the SSD cannot be mounted at `/var/lib/etcd`) and the team accepts a CP reinstall.

**Additional risks:** the CP reinstall wipes the OS/etcd on the HDD; a snapshot + `bootstrap --recover-from` is mandatory; 64 GB is tight for OS + images (~39 GB used, ~25 GB free); Longhorn must stay on the HDD and its data path must be preserved or rebuilt.

### A.1 Snapshot everything

- etcd snapshot (as in §2.2) and copy it off-host.
- Machine config backup (as in §2.3).
- Longhorn backups of `jellyfin-config`, `jellyfin-media`, `opengym-data`, or accept a rebuild from the worker replica.
- Record the Longhorn node/disk paths and the `config-longhorn-patch.yaml` mount.

### A.2 Install on the SSD

1. Verify the SSD device (`nvme0n1`).
2. Take the existing `talos/controlplane.yaml` (gitignored) and change only:
   ```yaml
   machine:
     install:
       disk: /dev/nvme0n1
   ```
   Keep the same cluster secrets/tokens/certs so the node can restore the existing cluster.
3. Install:
   - Preferred: `talosctl apply-config --nodes 192.168.1.5 --file controlplane-ssd.yaml --mode=reboot` (the installer writes the SSD and reboots). Verify the node boots from the SSD; if the BIOS still picks the HDD, set the SSD first in the boot order.
   - Fallback: boot the Talos ISO (v1.12.6/factory) and apply the config in maintenance mode: `talosctl apply-config --insecure --nodes 192.168.1.5 --file controlplane-ssd.yaml`.
4. Never format `/dev/sda`; the HDD holds the Longhorn replicas and the old data.

### A.3 Restore etcd

After the SSD boot, the etcd data dir is empty:

```bash
talosctl -n 192.168.1.5 service etcd      # Preparing/Waiting
talosctl -n 192.168.1.5 bootstrap --recover-from="<snapshot>"
```

### A.4 Longhorn data on the HDD

Do **not** leave the Longhorn disk path at `/var/lib/longhorn` on the 64 GB SSD; the replicas (~63 GB) will not fit.

Two choices:

1. **Preserve the old replicas (advanced).** Use an `ExistingVolumeConfig` to mount the old HDD EPHEMERAL partition (label `EPHEMERAL`) under `/var/mnt/longhorn`:

   ```yaml
   apiVersion: v1alpha1
   kind: ExistingVolumeConfig
   name: longhorn-old
   discovery:
     volumeSelector:
       match: volume.partition_label == "EPHEMERAL"
   ```

   This mounts the old partition read-write at `/var/mnt/longhorn`; the Longhorn data is at `/var/mnt/longhorn/lib/longhorn`. Then patch the Longhorn Node CR disk path from `/var/lib/longhorn/` to `/var/mnt/longhorn/lib/longhorn` (disable scheduling/evict first, patch, re-enable) and verify the replicas are adopted. Review the Longhorn chart hostPath/`config-longhorn-patch.yaml` to ensure the new path is propagated.
2. **Rebuild (simpler, slower).** Format a fresh partition on the HDD as a user volume at `/var/mnt/longhorn`, set the Longhorn disk path/default-data-path to it, and let Longhorn rebuild the CP replicas from the worker. This copies ~63 GB over the HDD; expect hours. The worker replica is the source; do not lose the worker.

**Do not** let Longhorn write replicas to the 64 GB SSD.

### A.5 Validation

Same as §6, plus:

```bash
talosctl -n 192.168.1.5 get mountstatus | grep -E 'EPHEMERAL|/var/lib/etcd|/var/mnt/longhorn'
kubectl -n longhorn-system get nodes.longhorn.io talos-cp-01 -o yaml | grep -A4 'path:'
```

The `EPHEMERAL` volume must be on `nvme0n1`; Longhorn's path must be on the HDD (`/var/mnt/longhorn...`), not the SSD.

---

## Appendix B — Commands and facts

```bash
export TALOSCONFIG=/mnt/c/Users/dani/Documents/repos/homelab/talos/talosconfig
export KUBECONFIG=/mnt/c/Users/dani/Documents/repos/homelab/talos/kubeconfig

# inventory
talosctl -n 192.168.1.5 get disks
talosctl -n 192.168.1.5 get discoveredvolumes -o yaml
talosctl -n 192.168.1.5 get mountstatus
talosctl -n 192.168.1.5 get mc -o yaml

# snapshot / restore
talosctl -n 192.168.1.5 etcd snapshot /path/etcd.db
talosctl -n 192.168.1.5 service etcd
talosctl -n 192.168.1.5 bootstrap --recover-from=/path/etcd.db

# health
kubectl get nodes
flux get kustomizations
kubectl -n longhorn-system get volumes.longhorn.io -o custom-columns='NAME:.metadata.name,ROBUST:.status.robustness'
```

Known facts for this cluster:

- Talos server `v1.12.6`, client currently `v1.14.0`; version-skew warnings are expected. Do not upgrade Talos as part of this migration.
- The CP is the only etcd member and the only control plane. The API is unavailable during the reboot/restore.
- Longhorn disk path: `/var/lib/longhorn/`; per-node data ~63 GB; the HDD is `HGST HTS725050A7E630`, 500 GB, 64 reallocated sectors, ~39,968 power-on hours.
- `machine.mounts`/`machine.mountpoints` do not exist in Talos v1.12. `machine.disks` still exists but is deprecated and undocumented; it is processed by the legacy `UserDiskConfigController`. The supported v1.14+ way to dedicate a system volume is a `VolumeConfig` document (`ETCD`, `CRI`, `KUBELET`, `LOG`), but it is only honored at provisioning time.
- `machine.disks` **formats** the target partition. The SSD must be new/empty. Never point it at `sda`.
- `bootstrap --recover-from` is a one-shot operation for an unbootstrapped etcd. The etcd service must be waiting/preparing.

## Appendix C — Post-migration hardening (separate tasks)

- Add a recurring etcd snapshot CronJob/backup (Bitwarden/NAS) so a future migration does not depend on a manual snapshot.
- Add a PrometheusRule for `etcd_disk_wal_fsync` p99 (already present in `homelab-failure-modes`) and keep the alert route.
- Replace the aging HDDs with 500 GB SSDs when budget allows; with 500 GB per node, Longhorn can move to the SSD and the HDD can become media/backup storage.
- Consider a second control-plane node (3-node cluster) for real etcd HA. `leader-elect=false` is safe only while there is a single CP.

---

## Appendix D — Follow-ups from the 2026-10-18 execution

- Watch `jellyfin-media` (`pvc-1275139b…`) rebuild. It is `degraded` and queued behind the
  serialized rebuild limit (`rebuildConcurrentSyncLimit=1`); once its turn comes it rebuilds
  from the worker replica. No manual action unless it stays stopped for hours.
- Investigate the worker kube-proxy regression: after the CP reboot, DNAT for `10.96.0.1`
  was stale on the worker while other ClusterIPs worked. Restarting kube-proxy fixed it.
  Add a post-reboot check for pod -> `10.96.0.1:443` in this runbook's verification.
- Jellyfin liveness probe was relaxed in `k8s/services/jellyfin/values.yaml`
  (`initialDelaySeconds: 120`, `failureThreshold: 60`) because Jellyfin takes ~75 s to
  start and the old probe killed it at ~60 s. This also makes post-reboot recovery safe.
- If fsync p99 below 10 ms is required, replace the DRAM-less 64 GB NVMe with a
  DRAM-equipped drive; the current result is a ~13× improvement but still has a 13-14 ms
  p99 tail.
