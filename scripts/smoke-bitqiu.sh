#!/usr/bin/env bash
set -euo pipefail
image=${1:?image required}
docker run --rm --network none --entrypoint /opt/alist/alist "$image" version
name="alist-bitqiu-smoke-${RANDOM}"
smoke_dir=$(mktemp -d)
mkdir "$smoke_dir/data" "$smoke_dir/downloads"
trap 'docker rm -f "$name" "$name-reject" >/dev/null 2>&1 || true; rm -rf "$smoke_dir"' EXIT
reject_mount() {
  docker create --network none --name "$name-reject" "$@" "$image" >/dev/null
  timeout 20 docker start -a "$name-reject" >"$smoke_dir/reject.log" 2>&1 || true
  test "$(docker inspect -f '{{.State.Running}}' "$name-reject")" = false
  test "$(docker inspect -f '{{.State.ExitCode}}' "$name-reject")" = 78
  grep -q /downloads "$smoke_dir/reject.log"
  docker rm -v "$name-reject" >/dev/null
}
reject_mount
reject_mount --mount "type=bind,src=$smoke_dir/downloads,dst=/downloads,readonly"
reject_mount -e PUID=65534 -e PGID=65534 --mount "type=bind,src=$smoke_dir/downloads,dst=/downloads"
docker run -d --network none --name "$name" \
  -e "PUID=$(id -u)" -e "PGID=$(id -g)" \
  --mount "type=bind,src=$smoke_dir/data,dst=/opt/alist/data" \
  --mount "type=bind,src=$smoke_dir/downloads,dst=/downloads" "$image" >/dev/null
for attempt in $(seq 1 30); do
  if docker exec "$name" sh -c 'wget -Y off -qO /tmp/alist-ping http://127.0.0.1:5244/ping && test "$(cat /tmp/alist-ping)" = pong'; then
    docker exec "$name" sh -c 'wget -Y off -qO /tmp/alist-ui http://127.0.0.1:5244/ && grep -qi "<html" /tmp/alist-ui'
    docker exec "$name" sh -c 'wget -Y off -qO /tmp/alist-nav http://127.0.0.1:5244/bitqiu-assets/nav.js && test -s /tmp/alist-nav && grep -q "bitqiu-assets/nav.js" /tmp/alist-ui'
    docker exec "$name" sh -c 'wget -Y off -qO /tmp/alist-nas http://127.0.0.1:5244/@nas && grep -q "target-folders" /tmp/alist-nas'
    exit 0
  fi
  if [ "$(docker inspect -f '{{.State.Running}}' "$name")" != true ]; then
    docker logs "$name" 2>&1 | grep -viE "password|passwd|token|secret|admin" >&2 || true
    echo "AList exited during smoke test" >&2
    exit 1
  fi
  sleep 1
done
echo "AList did not become healthy" >&2
exit 1
