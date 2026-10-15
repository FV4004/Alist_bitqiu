#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=v3.65.0-bitqiu.10
frontend=3.65.0
commit=$(git rev-parse --short=12 HEAD)
built_at=$(date -u '+%Y-%m-%d %H:%M:%S +0000')
mkdir -p build
: "${CC:=musl-gcc}"
command -v "$CC" >/dev/null || { echo "Install musl-tools (musl-gcc) before building" >&2; exit 1; }
CGO_ENABLED=1 CC="$CC" GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter \
  -ldflags="-s -w -linkmode external -extldflags '-static' -X 'github.com/alist-org/alist/v3/internal/conf.Version=${version}' -X 'github.com/alist-org/alist/v3/internal/conf.WebVersion=${frontend}' -X 'github.com/alist-org/alist/v3/internal/conf.GitCommit=${commit}' -X 'github.com/alist-org/alist/v3/internal/conf.GitAuthor=FV4004' -X 'github.com/alist-org/alist/v3/internal/conf.BuiltAt=${built_at}'" \
  -o build/alist .
