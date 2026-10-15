#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=v3.65.0-bitqiu.1
frontend=3.65.0
commit=$(git rev-parse --short=12 HEAD)
built_at=$(date -u '+%Y-%m-%d %H:%M:%S +0000')
mkdir -p build
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -tags=jsoniter \
  -ldflags="-s -w -X 'github.com/alist-org/alist/v3/internal/conf.Version=${version}' -X 'github.com/alist-org/alist/v3/internal/conf.WebVersion=${frontend}' -X 'github.com/alist-org/alist/v3/internal/conf.GitCommit=${commit}' -X 'github.com/alist-org/alist/v3/internal/conf.GitAuthor=FV4004' -X 'github.com/alist-org/alist/v3/internal/conf.BuiltAt=${built_at}'" \
  -o build/alist .
