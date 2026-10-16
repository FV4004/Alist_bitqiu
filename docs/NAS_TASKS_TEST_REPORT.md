# NAS 下载任务优化：实现与验收

本次基于 AList v3.65.0-bitqiu.11 的原 NAS 页面、复制队列和存储接口实现，不新增另一套并发队列。版本为 v3.65.0-bitqiu.12。

## 需求对应

| 需求 | 实现 |
| --- | --- |
| REQ-01/02 | 全选当前目录跨越所有分页，取消全选和单项勾选同步；全部/部分/未选分别显示勾选、半选、未选。 |
| REQ-03 | 全部开始处理等待、暂停及可重试失败任务；需丢弃进度的任务要求单独确认。 |
| REQ-04 | 全部暂停先锁定并暂停等待项，再终止活动传输；保留临时文件与进度。 |
| REQ-05 | 全部取消有二次确认；后端取消请求并等待执行退出，清理本任务临时文件。 |
| REQ-06 | 每项独立开始/暂停/取消，按状态启用按钮。 |
| REQ-07 | 选中项和全部作用域明确分开，批量只操作选中 ID，返回成功/失败/跳过数量；复用复制队列并发上限。 |
| REQ-08 | 选择变化实时更新数量、已知容量和未知大小文件数；真实空文件为已知 0，缺少大小不会算作准确 0。 |
| REQ-09 | 后端递归统计文件夹，按源对象身份去重，仅累加叶子文件大小；统计错误明确提示。 |
| REQ-10 | 后端维护等待、下载中、暂停中、已暂停、已完成、失败、取消中、已取消；页面读取统一快照，持久化独立于浏览器。 |

开始和暂停适用状态按需求表执行；过渡状态不接受重复开始。全部暂停额外暂停等待队列（用户已确认）；单项/选中项暂停跳过等待项。取消失败任务会将其历史结果更新为用户取消。HTTP 超时和其他失败保留可用的错误原因，允许开始重试。

恢复重新取得源链接，续传校验 Range 偏移、总大小和可用的实体标识；服务端忽略 Range、返回不一致的 Content-Range 或实体变化时暂停并要求单独确认从头下载，不静默丢弃进度。最终文件采用不覆盖发布，路径通过 os.Root 限制在原目标挂载内。

## 测试矩阵

| 测试 | 自动验证 |
| --- | --- |
| TEST-01 | 浏览器全选 237 项、三页、可见 100 项全显示勾选，再取消全选。 |
| TEST-02 | 取消一个文件验证半选，重新选中恢复全选；任务勾选也验证半选。 |
| TEST-03 | 浏览器单任务暂停不改变其他任务；真实 HTTP 暂停后核对临时字节。 |
| TEST-04 | 真实 HTTP Range 从临时文件偏移续传，最终文件逐字节比对；无 Range 明确确认后从头下载。 |
| TEST-05 | 选中项批量开始作用域和跳过状态；原单工作线程队列恢复多个任务，验证传输并发。 |
| TEST-06 | 全部暂停先冻结等待项，核对等待任务传输字节仍为 0。 |
| TEST-07 | 浏览器取消确认拒绝不发送请求、确认后取消；后端拒绝无确认请求。 |
| TEST-08 | 真实正在传输任务取消后，源 HTTP 活动结束，临时及最终文件均不存在，其他任务文件不变。 |
| TEST-09 | 已知容量变化、未知大小提示；协议对象测试缺失/null/非数字/0/正数字节。 |
| TEST-10 | 递归文件夹和空文件夹下载、去重统计；浏览器显示包含文件数及未知容量。 |
| TEST-11 | 浏览器刷新保持后端状态；数据库记录加载恢复历史，等待/下载中恢复为暂停，保留进度。 |
| TEST-12 | HTTP 503 失败显示且可重试；无 Range、错误偏移、实体/大小变化防止错误拼接；历史超时原因展示。 |

本地结果：NAS 页面浏览器验收已通过根路径和 `/alist` 路径前缀，覆盖桌面和 390px 手机布局；无页面异常及横向溢出。`internal/task` race 测试及导航 Node 测试通过。当前云环境无法下载部分 Go 依赖，完整后端测试已在 [GitHub Actions](https://github.com/FV4004/Alist_bitqiu/actions/runs/38766799016) 通过：BitQiu 协议、NAS 集成、真实 HTTP 传输/续传/取消/并发、任务记录恢复、大小元数据和全部相关 race 检查均通过。官方登录和 NAS 浏览器回归本地已通过；同样列入发布门禁。工作流在这些检查全部通过后才构建并执行镜像启动检查，随后双仓库发布。最终对应提交的完整构建结果以 [Actions](https://github.com/FV4004/Alist_bitqiu/actions/workflows/bitqiu-amd64.yml) 为准。

验证命令与 CI 保持一致：

```sh
go test -race ./drivers/bitqiu
go test -race ./internal/op -run '^TestPendingLoginCanBeCreatedAndEdited$'
go test -race ./server/handles -run '^Test(BitQiuNASIntegration|NASTaskInfo)$'
go test -race ./internal/fs -run '^TestNAS'
go test -race ./internal/task ./server/nasui
node --test scripts/nas-ui.test.cjs
python scripts/test-alist-login.py
python scripts/test-nas-browser.py
```

真实下载测试使用本地 HTTP 服务和临时 NAS 目录，不含真实用户账号；网页测试使用模拟接口。没有以真实 BitQiu 账号、真实极空间系统或磁盘写满现场做联网验收，部署后仍需核对服务端下载权限与 NAS 挂载写入权限。写入/同步/连接错误均进入可重试失败状态，但未通过实际填满磁盘验证 ENOSPC。

## 修改文件清单

- 前端：`server/nasui/web/index.html`、`app.js`、`style.css`。
- 后端队列和传输：`internal/fs/nas_tasks.go`、`nas_transfer.go`、`copy.go`，`internal/bootstrap/task.go`。
- 统计、任务 API 和兼容适配：`server/handles/bitqiu_nas_tasks.go`、`bitqiu_nas.go`、`task.go`、`fsread.go`，`server/router.go`。
- 大小元数据：`internal/model/obj.go`、`drivers/bitqiu/client.go`、`util.go`。
- 自动测试：`internal/fs/nas_tasks_test.go`、`server/handles/bitqiu_nas_test.go`、`drivers/bitqiu/client_test.go`、`scripts/test-nas-browser.py`、`test-alist-login.py`。
- 版本、CI 和文档：`Dockerfile.bitqiu`、`scripts/build-bitqiu.sh`、`.github/workflows/bitqiu-amd64.yml`、`README.md`、`README_BITQIU.md`、`docs/RELEASE.md`、本报告。

## 数据和部署兼容

使用已有 `task_items` 表的 `bitqiu_nas_v1` 项，不改数据库结构、不加配置、不改变最终文件名和 NAS 路径选择。文件创建沿用 Local 写入的 0666 与容器 umask 组合，保留 NAS 的正常读取规则。新记录不包含密码、Token 或临时下载 URL。容器升级保留原数据和 `/downloads` 挂载；不要删除任务临时文件。

旧式任务继续由原 AList 任务管理查看、重试和取消，不自动迁移到新状态模型。建议完成旧任务后升级。重启后增强任务暂停，手动开始继续；数据库记录损坏时保护原记录并暂停增强任务写入，主 AList 仍可启动。原 HTTP API 保留，新增管理员 `/api/admin/bitqiu/nas/stats`、`tasks`、`control` 接口；普通复制和原并发设置继续工作。


## v3.65.0-bitqiu.13 增量验收

- 新增 `internal/fs/nas_concurrency.go`：NAS 1–5 入队名额、真实开始顺序、上限持久化和最新优先暂停；继续使用原复制管理器，不创建额外工作线程池。
- `internal/fs/nas_tasks_test.go` 增加 8 个共享工作线程下的最多 5 并发、并发提交等待项、5 调为 2 时只暂停最后 3 项、源请求结束、保留字节、提高上限不自动恢复暂停项、恢复后逐字节核对以及重启后保留上限测试。
- NAS 任务快照增加 `concurrency`，新增管理员 POST `/api/admin/bitqiu/nas/concurrency`，拒绝 0、负数、6 及非整数。页面验证保存、刷新恢复、缩小上限和保存失败后恢复旧值。
- 增加 `scripts/bitqiu-entrypoint.sh`、`check-downloads.awk`、`test-downloads-mount.py`。挂载解析覆盖无挂载、仅父挂载、含空格路径、NAS 绑定、只读、匿名及命名 Docker 本地卷；镜像 smoke 使用正常启动入口验证拒绝错误配置、拒绝权限不足并在有效绑定下启动，保留登录和 NAS 资产检查。
- Dockerfile 和 Compose 声明 /downloads，Compose 强制提供已有路径；升级需要新增 NAS 绑定挂载。已有数据和最终文件命名不变，新增设置占用现有表一项，无表结构迁移。
- 移动端只做可行性评估和 320/360/390/430/768px 视口验证，未修改原 CSS 布局；详见 NAS_PORTRAIT_ASSESSMENT.md。模拟测试不替代真实极空间/iOS/Android 验收。

本地浏览器增量测试、挂载解析及复用上一版二进制的真实启动入口测试已通过。完整 Go/race、5 调到 2 的真实传输测试及浏览器回归已在 GitHub Actions 通过；使用新二进制的最终镜像启动检查继续作为发布门禁。对应提交的完整 Actions 成功结果确认最终验收状态。

构建上下文 `.dockerignore` 仅额外放行两个启动检查文件，保留原二进制白名单；不打包工作区的其它脚本、安装包或配置。


## v3.65.0-bitqiu.14 前端增量验收

- 重复提交弹窗、纯文本渲染、失败保留勾选及目标、Escape/关闭焦点恢复；批量部分失败展示成功/失败/跳过数量与原因。
- 历史超时任务查看详情，手动/定时刷新不主动重复弹窗。
- 根路径及 /alist 前缀，320/360/390/430px 手机和 768px 布局无横向溢出；手机按钮至少 44px、导航定位、横竖切换保留选择、400px 高度长错误可滚动和关闭。
- 原 TEST-01～TEST-12 对应的前端回归继续执行；真实下载/断点/取消由现有 Go 测试和发布 CI 覆盖，本次未改后台。
- 无数据库或配置迁移；实体 iOS/Android、软键盘与极空间内嵌浏览器尚未实机验收。
