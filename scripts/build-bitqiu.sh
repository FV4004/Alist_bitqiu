#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
version=v3.65.0-bitqiu.24-cpu-compat-preview
frontend=3.65.0
commit=$(git rev-parse --short=12 HEAD)
built_at=$(date -u '+%Y-%m-%d %H:%M:%S +0000')
mkdir -p build
: "${CC:=musl-gcc}"
command -v "$CC" >/dev/null || { echo "Install musl-tools (musl-gcc) before building" >&2; exit 1; }
# The C compiler and prebuilt musl must also target baseline x86-64. Release
# runners are pinned to Ubuntu 24.04; Ubuntu 26.04's musl is amd64v3. The
# post-build QEMU gate validates the linked executable, not just Go metadata.
CGO_ENABLED=1 CC="$CC" GOOS=linux GOARCH=amd64 GOAMD64=v1 \
  CGO_CFLAGS="-O2 -g -march=x86-64 -mtune=generic" \
  CGO_CXXFLAGS="-O2 -g -march=x86-64 -mtune=generic" go build -trimpath -tags=jsoniter \
  -ldflags="-s -w -linkmode external -extldflags '-static' -X 'github.com/alist-org/alist/v3/internal/conf.Version=${version}' -X 'github.com/alist-org/alist/v3/internal/conf.WebVersion=${frontend}' -X 'github.com/alist-org/alist/v3/internal/conf.GitCommit=${commit}' -X 'github.com/alist-org/alist/v3/internal/conf.GitAuthor=FV4004' -X 'github.com/alist-org/alist/v3/internal/conf.BuiltAt=${built_at}'" \
  -o build/alist .
