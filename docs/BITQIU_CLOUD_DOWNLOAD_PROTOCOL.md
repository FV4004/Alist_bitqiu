# Windows 客户端云下载静态证据

采集日期：2026-10-28。仅在 `bitqiu-cloud-download` 开发，不合并主分支、不发布镜像。本记录区分客户端可见行为和仍待验证的服务端契约；路径常量出现不等于该接口可直接调用。

## 来源与复查

官方下载页为 `https://pan.bitqiu.com/download#/`。该页的 `download.5321045b88a6c6c23859.js` 定义 PC 类型为 `3`，点击下载调用 `getVersionUrl({type:3})`；`common.4b5acb5e48a24a41fbed.js` 将其映射到 `POST /ver/getVersionUrl`。实际返回版本 `4.1.0.1`，大小 `158.07MB`，日期 `2025-03-12`。

安装包实际为 165744624 字节，使用 7-Zip 26.05 静态解包成功，未执行任何 Windows 文件。安装包及解出的 `BitqiuPan.exe` 哈希记录在 `BITQIU_CLOUD_DOWNLOAD_EVIDENCE.json`；不要将签名下载 URL、安装包或第三方前端完整代码提交到仓库。

`BitqiuPan.exe` 的 ASCII 字符串包含 `https://pan-pc.bitqiu.com/` 和原生 API 主机 `https://pan-api.bitqiu.com`。前者返回的页面加载 `_nuxt/acc9b6e.js`、`11dddf0.js`、`4a33481.js`、`0dd941b.js`。这些是采集当天的远程前端，不能声称是 2025 年随安装包固定发布的代码。

可用 `strings`、`objdump` 复查原生文件；用 JavaScript AST 解析上述前端的 webpack 模块数组，不执行前端代码。以下模块编号均指该哈希对应的文件，不能跨前端版本复用编号。

## 已观察到的请求构造

`0dd941b.js` 模块 219 的 ACTION 常量和模块 960 的链接提交模型共同确认：

| 行为 | 页面路径 | 证据与限制 |
| --- | --- | --- |
| 链接提交 | `/api/cloudDownload/addTasks` | 模块 960，字段 `dir_id`、`download_urls`；模块 16 的 ajax 默认 `method:"post"` |
| 任务列表 | `/api/cloudDownload/getUserTaskList` | API 包装函数显式 POST；默认 `page:1,limit:1,status:0` |
| 单任务查询 | `/api/cloudDownload/queryTask` | 仅见路径常量，尚未确认参数或调用点，不能使用 |
| BT 上传提交 | `/api/cloudDownload/addTask` | 模块 957 的上传流程，不能当成普通链接提交接口 |

页面路径中的 `/api` 是否由前端网关剥离后转发到原生 API 主机，尚未证实。禁止照搬已有文件列表接口的规律去猜测直连路径。

链接提交模型按行拆分、去空白、去重，再逐项执行 `encodeURIComponent`。模块 191 的请求拦截器在签名前将字段值调用 `.toString()`，并在签名桥返回后进行表单编码。单元素数组因此变为该元素的字符串，多元素数组变为逗号分隔字符串；不能直接使用 Go 的重复表单键替代。当前签名辅助函数只接受已经构造好的单值字段，拒绝数组；链接规范化和传输尚未接入。

模块 219 的 `DOWNLOAD_LINK` 正则接受 `http://`、`https://`、`ftp://`、`ed2k://`、`thunder://`、`magnet:?`。这只证明前端接受这些形式，不证明服务端对每一种都可用；首版不能据此宣称全部支持。

## 鉴权桥

模块 191 将请求交给 `window.$bridge.getSignParams`，POST 表单以 `application/x-www-form-urlencoded` 编码，并从 `window.clientPars` 提供头字段。模块 1662 默认使用原生 `bitQiu.sign`；pay、discount、loginServer、coupons 路径才选择 `nsign`。云下载走 `sign` 桥，不能套用现有 `authenticatedClientRequest`：后者会在认证过期时尝试密码登录，不符合本功能约束。

原生文件含 `bitQiu.sign` 和 `bitQiu.nsign` 的 JS 扩展声明。`objdump -d -M intel` 可在 VA `0x14004bd9a`、`0x14004be81` 复查 `sign` 分派引用，在 `0x14004c4d7`、`0x14004c5c5` 复查 `nsign` 引用。

进一步追踪确认：

| 原生位置 | 行为 |
| --- | --- |
| `0x140032590` | `sign_response` 处理函数调用 `0x140020c20` |
| `0x140020c20` | 检查现有会话，并构造签名字段和返回表单 |
| `0x140020d20`、`0x140020daa`、`0x140020ebb` | 从会话对象 `+0x40`、`+0x60`、`+0x80` 读取 `access_token`、`open_id`、`user_id` |
| `0x140020def` | 从对象 `+0x160` 读取既有 `platform`；此函数不生成新设备身份 |
| `0x140020d65` | 加入现有驱动同一 `clientAppID` 常量 |
| `0x140020c74`、`0x140020e47` | 取得时间戳；辅助函数 `0x1401d2b00` 调用 `0x1401d10c0`，将 Windows FILETIME 转为 Unix 毫秒 |
| `0x140020fd3` | 空的调用方字段不进入签名用的字段对象；返回对象仍保留这些字段 |
| `0x1401d2560` | 用有序映射将字段拼为 `key=value&key=value`；`0x1401d2887`、`0x1401d28b7` 处分别引用 `&`、`=`，不做 URL 转义 |
| `0x1400210e2` | 直接追加与已有驱动相同的 `clientSigningKey` 常量 |
| `0x1400213ef` | 调用 `0x1401cbd90`，后者经 `0x1401cbc70` 使用 Windows CryptoAPI 的 `CALG_MD5` (`0x8003`)，输出小写十六进制 |
| `0x14002141b` | 将摘要加入返回表单 `sign` |

桥自动加入的字段为 `access_token`、`app_id`、`open_id`、`platform`、`timestamp`、`user_id`；不会自动加入 `org_channel`。返回表单保留空调用方字段，签名时忽略它们，所以不能直接用所有返回字段重算签名。现有登录签名函数可用于已过滤的字段集合，但登录请求构造流程不能直接复用。

`drivers/bitqiu/cloud_download_form.go` 据此实现纯表单构造，读取当前驱动会话、绑定及设备标识；缺少会话或绑定发生变化时拒绝构造，不初始化 HTTP、不登录、不发短信、不生成设备、不保存或清空会话。它不发送请求，也不开放链接提交。测试向量为独立计算的合成数据，不冒充抓包结果。

## 列表字段与状态

`4a33481.js` 模块 1396 的查询参数为 `page`、`limit`、`status`、`task`。读取响应 `data.data` 为任务数组，另有 `totalCount`、`totalPageCount`、`activeCount`。首次和后续轮询的参数默认不同；不能用页面默认一条记录代替完整分页恢复。

`0dd941b.js` 模块 1614 明确映射：

| status | 客户端显示 |
| --- | --- |
| 0 | 等待下载 |
| 1 | 下载中 |
| 2 | 已暂停 |
| 3 | 下载失败 |
| 4 | 下载完成 |
| 5 | 部分完成 |

同模块使用 `progress` 作为 0–100 的百分比，显示时钳制越界值；字段还包括 `taskId`、`taskName`、`dirId`、`downloadUrl`、`size`、`fileSize`、`total`、`fileTotal`、`type`、`errorCode`。模块 1611 的 Vue 列表 key 则使用 `id`。**不能把 `id` 和 `taskId` 假定为同一身份。** `dirId` 被用于打开结果目录，也不能未经验证就将其等同于提交时的 `dir_id`。

未知状态必须保留为未知，部分完成不能当成全部成功。暂停状态的存在不授权开放暂停或恢复操作。模块 1614 的移除流程标题为移除记录，另带可选删除源文件选项；不能将其解释为已验证的停止云端下载操作。

`internal/offline_download/bitqiu/protocol.go` 提供隔离的列表解析准备层：保留身份字段原始 JSON，不把 `id` 回退成 `taskId`；整数身份不经过 float64。缺失状态保持未知，缺失进度保持未知，显示钳制不改变状态。缺失/空值任务数组与显式空数组分开处理，格式变化时拒绝整页，不据此认定远端任务消失。解析器没有网络入口，也尚未参与创建、恢复或权限检查。合成测试只验证这些本地约束，不证明服务端响应字段类型。

## 已观察到的错误处理

模块 219 常量：成功 `10200`，会话过期 `20003`，空间不足 `20103`，任务已存在 `00609`，部分成功 `00600`，无云下载权限 `00605`，超次数限制 `00606`。模块 960 另处理 `10833` 验证挑战，以及错误响应中的 `data.success`、`data.error`。不得自动重试挑战、超时或部分成功的提交，也不得通过新增登录绕过挑战。

## 尚不能开放的原因

1. 创建成功处理只转发响应并刷新页面，没有读取远端任务身份。`data.success` 元素结构、创建响应身份与列表 `taskId`/`id` 的对应关系均未确认。
2. 原生 `sign` 桥的会话、时间戳、设备字段和签名算法已核对并实现离线构造；页面 `/api` 网关与原生 API 的对应关系、额外头字段及实际传输尚未验证。不得猜测去掉 `/api` 的直连路径。
3. 前端接受的链接类型、服务端保存目标及任务结果关联尚未形成完整的可验证契约。

已请求现成脱敏响应，用户确认没有。不索取账号密码、验证码、登录令牌、Cookie 或签名；不因缺少材料主动登录或创建真实云任务。后续可继续审计原生桥或服务端公开契约；在缺口消除前，现有 journal 继续作为隔离的提交安全层，不注册工具、不开放业务入口、不自动下载到 NAS。
