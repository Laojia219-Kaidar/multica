#!/usr/bin/env bash
set -Eeuo pipefail
root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd); tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
mkcand(){ d="$tmp/$1"; mkdir -p "$d"; printf 'name: multica-dgx-ultra\n' > "$d/compose.yaml"; c=$(sha256sum "$d/compose.yaml"|awk '{print $1}'); jq -n --arg c "$c" '{schema:"HiveCrewIntegrationIdentityV1",compose_project:"multica-dgx-ultra",final_revision:("a"*40),final_tree:("b"*40),source_archive_sha256:("c"*64),authority_overlay_sha256:("d"*64),compose_sha256:$c,backend_image:{id:"backend:candidate",digest:("sha256:"+ ("e"*64))},web_image:{id:"web:candidate",digest:("sha256:"+ ("f"*64))},rollback_predecessor:{backend_image:"backend:previous",backend_digest:("sha256:"+ ("1"*64)),web_image:"web:previous",web_digest:("sha256:"+ ("2"*64))}}' > "$d/INTEGRATION-IDENTITY.json"; echo "$d"; }
cat > "$tmp/docker" <<'D'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "${FAKE_LOG:?}"
case "$1" in ps) exit 0;; compose) exit 0;; inspect) [[ "${FAKE_INSPECT_FAIL:-}" != 1 ]] && { [[ "$5" == *backend* ]] && echo backend:candidate@sha256:$(printf e%.0s {1..64}) || echo web:candidate@sha256:$(printf f%.0s {1..64}); }; exit 0;; esac
D
cat > "$tmp/curl" <<'C'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in *health*) [[ "${FAKE_HEALTH_FAIL:-}" != 1 ]] && echo -n 200;; *) [[ "${FAKE_VERSION_FAIL:-}" != 1 ]] && echo -n aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa;; esac
C
chmod 755 "$tmp/docker" "$tmp/curl"; export FAKE_LOG="$tmp/log"; : > "$FAKE_LOG"
c=$(mkcand good); DOCKER_BIN="$tmp/docker" CURL_BIN="$tmp/curl" "$root/precheck.sh" "$c" >/dev/null
if DOCKER_BIN="$tmp/docker" CURL_BIN="$tmp/curl" "$root/apply-staging.sh" "$c" >/dev/null 2>&1; then :; else echo success_case_failed; fi
bad=$(mkcand bad); jq '.backend_image.id="evil;touch /tmp/pwned"' "$bad/INTEGRATION-IDENTITY.json" > "$bad/x"; mv "$bad/x" "$bad/INTEGRATION-IDENTITY.json"; : > "$FAKE_LOG"; if DOCKER_BIN="$tmp/docker" CURL_BIN="$tmp/curl" "$root/precheck.sh" "$bad" >/dev/null 2>&1; then echo injection_accepted; exit 1; fi; [[ ! -s "$FAKE_LOG" ]]
printf 'state_machine_precheck_and_injection=pass\n'
