# Check the exact mount, not a directory in the container writable layer.
$5 == "/downloads" {
    found = 1
    writable = ("," $6 "," ~ /,rw,/)
    # Standard Docker local volumes (including implicit anonymous volumes) are
    # not a NAS folder chosen by the administrator. Require a host bind mount.
    count = split($4, root, "/")
    managed = count > 2 && root[count] == "_data" && root[count-2] == "volumes"
}
END {
    if (!found) {
        print "配置错误：必须将 NAS 文件夹读写挂载到 /downloads；容器内创建同名目录不算挂载。" > "/dev/stderr"
        exit 78
    }
    if (managed) {
        print "配置错误：/downloads 使用了 Docker 管理卷；请在极空间 Docker 界面改为实际 NAS 文件夹的绑定挂载。" > "/dev/stderr"
        exit 78
    }
    if (!writable) {
        print "配置错误：/downloads 是只读挂载，请改为读写。" > "/dev/stderr"
        exit 78
    }
}
