# Authority loopback bridge (staging only)

The bridge is non-root, read-only, and host-networked. Resolve the single gateway from the existing backend container with `authority-bridge-resolve.sh`; no subnet is created or changed. The bridge binds only `<gateway>:3151` and forwards to `127.0.0.1:3150`. The stop helper removes it during rollback. No secret is mounted or logged.
