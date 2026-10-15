#!/usr/bin/env bash
set -euo pipefail
image=${1:?image required}
docker run --rm --network none --entrypoint /opt/alist/alist "$image" version
name="alist-bitqiu-smoke-${RANDOM}"
trap 'docker rm -f "$name" >/dev/null 2>&1 || true' EXIT
docker run -d --network none --name "$name" --entrypoint /opt/alist/alist "$image" server --data /tmp/alist-bitqiu-smoke --no-prefix >/dev/null
for attempt in $(seq 1 30); do
  if docker exec "$name" sh -c 'wget -qO /tmp/alist-ping http://127.0.0.1:5244/ping && test "$(cat /tmp/alist-ping)" = pong'; then
    docker exec "$name" sh -c 'wget -qO /tmp/alist-ui http://127.0.0.1:5244/ && grep -qi "<html" /tmp/alist-ui'
    docker exec "$name" sh -c 'wget -qO /tmp/alist-nav http://127.0.0.1:5244/bitqiu-assets/nav.js && test -s /tmp/alist-nav && grep -q "bitqiu-assets/nav.js" /tmp/alist-ui'
    docker exec "$name" sh -c 'wget -qO /tmp/alist-nas http://127.0.0.1:5244/@nas && grep -q "target-folders" /tmp/alist-nas'
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
