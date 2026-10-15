# AList BitQiu Windows 协议版（amd64）

在 AList 的 BitQiu 存储中增加 Windows 客户端密码登录、设备短信验证和原文件下载。保留原网页驱动供选择。

## 基础版本

- 官方镜像：`xhofe/alist:latest`，本次核对为 v3.65.0，linux/amd64。
- 固定基础镜像摘要：`sha256:499313cdb00ec53f60d9f00167df4ea737ca7f94f0d822673330f55bff8c9bfc`。
- 对应源码：AlistGo/alist，`ea628f9a4fb2dcd1f400a4a3f91ccb59b3c1da69`。
- 前端：官方 3.65.0。
- 协议参考：用户提供的 Windows 客户端 4.1.0.1；本仓库不分发其 EXE/DLL。

## NAS 部署

先备份原有 AList 数据目录。使用 `compose.bitqiu.yml`，把数据挂载路径改成现有 AList 数据目录，然后运行：

```sh
docker compose -f compose.bitqiu.yml up -d
```

也可下载 GitHub Release 中的 `alist-bitqiu-linux-amd64-image.tar.gz` 导入：

```sh
gzip -dc alist-bitqiu-linux-amd64-image.tar.gz | docker load
docker compose -f compose.bitqiu.yml up -d
```

数据目录、端口和 PUID/PGID 规则与原镜像一致。镜像仅支持 x86_64/amd64 NAS。若 GHCR 包尚未设为 public，可在 GitHub 包设置修改可见性，或使用 Release 镜像文件导入。

## 登录与验证

1. 管理后台编辑/新增 `BitQiu` 存储，`login_mode` 选 `windows`，输入账号和原始密码。`client_platform` 留空自动生成，`client_channel` 默认 `pc`；根目录默认 `0`，成功后会从 `/user/get` 读取根目录 ID。
2. `login_action` 保持 `none` 并保存。未触发设备验证时直接完成；触发时状态提示“需要新设备短信验证”。此时存储已保存，显示待验证错误是预期行为。
3. **关闭并重新打开编辑页**，确保加载服务端刚保存的 `client_state` 和设备标识。选 `send_sms` 保存。
4. 收到验证码后，**重新打开编辑页**，填入 `sms_code`，选 `verify_sms` 保存。
5. 成功后状态为工作中。操作自动恢复 `none`，验证码会清空；临时凭据会删除，正式会话会保存，重启复用。

短信只在明确选择 `send_sms` 时发送；失败或重启不自动重发。发送请求在发出前就标记为已消费，网络超时后若需重试，请等待至少 60 秒再重新选择。验证会话本地最多保留 15 分钟，服务端可能更早使其失效。验证码错误会保留待验证会话，允许重新填写。

更换账号、密码、设备标识或渠道会使旧登录状态失效。`reset_login` 清空会话重新登录。若 API 会话过期，文件读取最多重新登录一次；如果再触发设备验证，则回到上述流程，不自动发送短信。

`client_state` 包含敏感会话凭据；不要贴到问题报告中。它与账号密码一样保存在 AList 存储数据库中。

## 功能范围

- Windows 模式：密码登录、新设备短信验证、列目录（原生 `sort` 游标分页）、原文件地址获取、AList HTTP/WebDAV 下载。
- 本次 Windows 模式不迁移上传、删除、改名、移动、复制和建目录，调用会返回“不支持”。避免将客户端 Token 误发到原网页写入接口。
- `login_mode=web` 保留上游原驱动及其写入操作，仍受原网页登录兼容性限制。
- Windows 应用身份与 TV 不同；不接受直接混用 TV Token。没有对服务端权限或会员限制做绕过。

## 构建和验证

需要 Go 1.25+、Docker，以及官方前端文件：

```sh
curl -fL https://github.com/AlistGo/alist-web/releases/download/3.65.0/dist.tar.gz -o /tmp/alist-dist.tar.gz
tar -xzf /tmp/alist-dist.tar.gz -C public
go test -race ./drivers/bitqiu
bash scripts/build-bitqiu.sh
docker build --platform linux/amd64 -f Dockerfile.bitqiu -t ghcr.io/fv4004/alist_bitqiu:latest .
bash scripts/smoke-bitqiu.sh ghcr.io/fv4004/alist_bitqiu:latest
```

GitHub Actions 自动测试、构建、发布 GHCR 镜像，以及提供可 `docker load` 的 Release 镜像文件。上游自动工作流存放于 `.github/workflows.disabled`，不触发其第三方集成。

离线测试覆盖：登录挑战持久化、短信发送一次性操作、跨重启限流、验证码消费与过期、账号变更清理、会话复用与失效、游标分页、下载地址选择、错误处理与凭据不外泄。离线 HTTP 测试不是 BitQiu 实际账号联网验收；首次使用仍需用户完成一次真实登录、短信和文件下载验证。

源码沿用上游 AGPL-3.0，见 LICENSE。
