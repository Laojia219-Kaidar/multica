# Authority loopback bridge (staging only)

The bridge is non-root, read-only, and host-networked. The canonical, packaged
operator assets live under `ops/dgx-staging-package`; the scripts here are only
source-tree entrypoints. The resolver binds the existing backend container to
one exact Compose bridge network, project, service, network ID and gateway. It
rejects loopback, wildcard, LAN, Tailnet, stale and non-Docker-bridge addresses.

The bridge uses the exact candidate backend image and its
`/app/authority-loopback-bridge` binary; no third image is built at apply time.
It binds only `<gateway>:3151` and forwards to `127.0.0.1:3150`. Apply waits for
sidecar health and an HTTP 200 before starting the backend. Rollback removes the
unique label-bound sidecar and proves zero container and listener residue. No
secret, volume or Docker socket is mounted or logged.
