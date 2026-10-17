# How to boot your first node
Similar steps for the rest of CP nodes and worker nodes. For more information, see [this](https://docs.siderolabs.com/talos/v1.12/getting-started/getting-started).

## Generate configuration for lider (first) node
``` bash
talosctl gen config homelab https://<IP>:6443
```

## Configure node endpoint
``` bash
talosctl config endpoint <IP>
```

## Apply configuration to first node (CP)
``` bash
talosctl apply-config --insecure --nodes <IP> --file controlplane.yaml
```

## Bootstrap
``` bash
talosctl bootstrap --nodes <IP>
```

## Stability patch (control plane)
etcd runs on a slow HDD and can stall long enough for kube-scheduler and
kube-controller-manager to lose their leader lease and exit. Apply the patch to
the control-plane node only (a second CP would break its assumptions):
```bash
talosctl -n <CP_IP> patch mc --patch @talos/config-stability-patch.yaml
```
This restarts kube-scheduler, kube-controller-manager and etcd, so expect a
brief API outage. etcd metrics are then available at `<node-ip>:2381` and are
scraped by the monitoring stack.

# Upgrading talos image
## Generate your new talos image
Go here: https://factory.talos.dev/ and generate your custom image.

## Update your instance:
```bash
talosctl upgrade --image <factory-url> --nodes <IP>
```