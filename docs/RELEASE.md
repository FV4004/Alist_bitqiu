基于 xhofe/alist v3.65.0 的 linux/amd64 镜像，迁入 BitQiu Windows 4.1.0.1 的客户端登录、type=5 新设备短信验证及原文件下载协议。

镜像：ghcr.io/fv4004/alist_bitqiu:latest

也可下载 alist-bitqiu-linux-amd64-image.tar.gz，解压后使用 docker load 导入。

操作说明见仓库 README_BITQIU.md。客户端模式当前提供文件浏览和下载；原网页模式可选。已完成离线协议测试和容器启动检查；未使用真实 BitQiu 账号进行联网验收。网络侧接口或账号权限变化可能需要进一步适配。


本版修复 SMS 流程：新建 BitQiu 存储不要求验证码；先保存并启动 Driver，遇到新设备验证后自动发送首条短信。等待验证正常保存并在存储列表显示状态。收到短信后重新打开编辑页，填写 sms_code 保存即可。重启、重复保存和发送失败不会自动重发；手动 send_sms 保留 60 秒间隔。
