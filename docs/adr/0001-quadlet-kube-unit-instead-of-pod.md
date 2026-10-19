# Quadlet `.kube` unit instead of `.pod`

The brief specified a Quadlet `maket.pod` for running Caddy and the API together, but `.pod` units need Podman 5.0+, and the development VPS runs Podman 4.9 while the production server isn't chosen yet. We use a Quadlet `.kube` unit (a Kubernetes pod YAML holding both containers) instead, because it gives the same one-pod, systemd-managed shape and runs on Podman 4.4 and newer, so the full deploy setup can be tested on the dev VPS before production exists.
