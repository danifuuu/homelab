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

# Upgrading talos image
## Generate your new talos image
Go here: https://factory.talos.dev/ and generate your custom image.

## Update your instance:
```bash
talosctl upgrade --image <factory-url> --nodes <IP>
```