基于 xhofe/alist v3.65.0 的 linux/amd64 镜像，迁入 BitQiu Windows 4.1.0.1 的客户端登录、type=5 新设备短信验证及原文件下载协议。

镜像：ghcr.io/fv4004/alist_bitqiu:latest

也可下载 alist-bitqiu-linux-amd64-image.tar.gz，解压后使用 docker load 导入。

操作说明见仓库 README_BITQIU.md。客户端模式当前提供文件浏览和下载；原网页模式可选。已完成离线协议测试和容器启动检查；未使用真实 BitQiu 账号进行联网验收。网络侧接口或账号权限变化可能需要进一步适配。
