#!/bin/sh
set -eu

# Keep the official version command available without mounting user data.
if [ "${1:-}" = version ]; then
    exec /entrypoint.sh "$@"
fi

if [ ! -d /downloads ] || [ -L /downloads ]; then
    echo '配置错误：请将实际 NAS 文件夹读写挂载到 /downloads，并保留原 /opt/alist/data 挂载。' >&2
    exit 78
fi
awk -f /check-downloads.awk /proc/self/mountinfo
# Verify the download user's permissions without chowning the NAS tree or
# touching any existing files. mktemp makes the probe exclusive.
if ! su-exec "${PUID:-0}:${PGID:-0}" sh -c '
    probe=$(mktemp /downloads/.alist-write-check.XXXXXX) || exit 1
    if ! printf "%s" check > "$probe"; then rm -f "$probe"; exit 1; fi
    rm -f "$probe"
'; then
    echo '配置错误：容器下载用户无法写入 /downloads，请检查剩余空间、NAS 文件夹权限以及 PUID/PGID。' >&2
    exit 78
fi
exec /entrypoint.sh "$@"
