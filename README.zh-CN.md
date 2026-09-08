# cpa-key-quota

给 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 用的「按客户端 Key 限额」插件。

它**不签发** Key，也不替代 Plus 鉴权。绑定的是已经在 Plus `api-keys` 里的 Key。**美元金额按 CPA-Manager-Plus 的价目表记账**（本插件自己没有价表）。绑定且启用的策略超过日/周 USD 或 RPM 时返回 HTTP 429。未绑定或已停用的策略零动作（Plus 照常放行）。

| | |
|---|---|
| **插件 ID** | `cpa-key-quota` |
| **许可证** | MIT |
| **English** | [README.md](./README.md) |

## 环境

- 能 `dlopen` c-shared `.so` 的 CLIProxyAPI v7 宿主。用 glibc 镜像，例如 `eceasy/cli-proxy-api`。Alpine / `CGO_ENABLED=0` 的宿主加载不了本插件。
- Linux `amd64` 或 `arm64`，且与 CPA 宿主架构一致（`CGO_ENABLED=1`）。
- **USD 限额依赖 CPA-Manager-Plus**。插件读取 Plus `GET /v0/management/model-prices`，按该表记账。没有 Plus 时 RPM 仍然有效。

**不要**声明 `frontend_auth_provider`。超限必须 `Terminate` 429，不能靠 `Authenticated: false`。

## 部署

插件是 CPA `dlopen` 的 `.so`。常见布局：CPA 与 CPA-Manager-Plus 在同一 Docker 网络，`.so` bind-mount 进 CPA 的 `plugins/`。

### 1. 构建 `.so`

在与 CPA 同架构的 Linux 上：

```bash
make build-linux-amd64   # 或：make build-linux-arm64
```

产物：`dist/cpa-key-quota_linux_<arch>.so`。

本机是 macOS（或架构与 CPA 不同）时用 Docker 交叉编译。CPA 是 x86_64 就必须编 `linux/amd64`，即使笔记本是 ARM：

```bash
docker build --platform linux/amd64 -f e2e/Dockerfile.plugin -t cpa-key-quota-so:amd64 .
cid=$(docker create --platform linux/amd64 cpa-key-quota-so:amd64)
mkdir -p dist
docker cp "$cid":/cpa-key-quota.so dist/cpa-key-quota_linux_amd64.so
docker rm "$cid"
file dist/cpa-key-quota_linux_amd64.so   # 必须是：ELF 64-bit LSB shared object, x86-64
```

ARM 的 CPA 用 `--platform linux/arm64` 和 `cpa-key-quota_linux_arm64.so`。不要把 ARM 的 `.so` 拷到 amd64 宿主（反之亦然），CPA 会加载失败。

### 2. 安装文件

拷到 CPA 会加载的目录（`config.yaml` 里的 `plugins`，常见挂载为 `/CLIProxyAPI/plugins`）：

```text
plugins/linux/amd64/cpa-key-quota.so
```

ARM 宿主用 `plugins/linux/arm64/cpa-key-quota.so`。

### 3. 指向 CPA-Manager-Plus

把 [配置](#配置) 里的片段写入 CPA 的 `config.yaml`。`plus_base_url` 必须是 **CPA 容器里能访问到的 Plus 地址**（Docker DNS，不要填公网域名），例如 `http://cpa-manager-plus:18317`。`plus_management_key` 是 **Plus 的 admin key**（`CPA_MANAGER_ADMIN_KEY`），用来读价表，以及 **同步** 时读 Plus `api-keys`。它不是 CPA 的 `remote-management.secret-key`。

这两项配好、且 Plus 里已有模型价格之后，USD 限额才会生效。见 [价目表](#价目表)。

### 4. 重启 CPA

`plugins/` 经常是只读挂载。只覆盖 `.so` 不够，必须重启 CPA 进程才会 `dlopen` 新文件：

```bash
docker restart cli-proxy-api
```

只更新插件时，不必重启 Plus 或反向代理。

### 5. 验收

用 CPA 的 management key：

```http
GET /v0/management/plugins
GET /v0/management/plugins/cpa-key-quota/status
```

期望 `cpa-key-quota` 为 `registered: true`、`effective_enabled: true`；Plus 返回价表后 `prices_available: true`。CPA 日志应有 `plugin loaded plugin_id=cpa-key-quota path=plugins/linux/<arch>/cpa-key-quota.so`。

管理界面嵌在 `/v0/resource/plugins/cpa-key-quota/index.html`（Plus 面板菜单 **Key Quota**）。

以后发新版本：重新构建、覆盖同一路径的 `.so`、只重启 CPA。

## 价目表

**USD 记账依赖 CPA-Manager-Plus。** 本插件不自带、也不提供编辑价表的界面。改价去 Plus，插件只读。

| | |
|---|---|
| **依赖服务** | CPA-Manager-Plus（compose 里常见 `seakee/cpa-manager-plus`） |
| **接口** | `GET {plus_base_url}/v0/management/model-prices`，Bearer `plus_management_key` |
| **回退** | 该路径 HTTP 404 时再试 `GET …/v0/management/billing/model-prices`（Home / 旧版 Plus） |
| **没有 Plus** | `plus_base_url` 为空，或还没有任何价表：USD 门关闭。RPM 仍有效。 |

价表缓存 30 秒。之后拉取失败会保留上一次成功的表，避免 USD 限额被打掉。没有按模型家族写死费率（例如不会特判 GPT-5/6）：Plus 列表里出现的名字就会按该表记账。

## 配置

完整示例见 [`config.example.yaml`](config.example.yaml)。最少片段：

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-quota:
      enabled: true
      priority: 10
      state_file: "cpa-key-quota-state.json"
      plus_base_url: "http://cpa-manager-plus:18317"
      plus_management_key: "your-plus-admin-key"
```

| 字段 | 作用 |
|---|---|
| `enabled` | 不卸载插件的情况下暂停限额 |
| `state_file` | 绑定策略和用量的 JSON（不存在会创建，权限 600） |
| `plus_base_url` | CPA-Manager-Plus 地址（建议 Docker 内网）。见 [价目表](#价目表) |
| `plus_management_key` | Plus admin 的 Bearer，用于价表以及 **同步**（`GET /v0/management/api-keys`） |
| `keys` | 可选种子。状态文件一旦存在，以文件为准 |

用量在内存里累计，大约每 15 秒原子写入状态文件（临时文件 + rename）。

## 绑定与同步

**绑定**（界面 **新增**，或 API）：把已有 Plus Key 的**明文提交一次**。插件只存 `sha256:`、截断 preview、CPA `caller_scope`。明文不落盘，列表/status JSON 也不返回明文。

```http
POST /v0/management/plugins/cpa-key-quota/keys
Authorization: Bearer <CPA remote-management secret-key>
Content-Type: application/json

{"id":"k-team","name":"team","key":"sk-...","daily_limit_usd":0.5,"weekly_limit_usd":0,"rpm":0}
```

限额填 `0` 表示不限。同一路径 PATCH 可改限额、改名、启用/停用，或再贴明文换绑（立刻哈希）。

**从 Plus 同步**（**同步**）：`POST /v0/management/plugins/cpa-key-quota/keys/sync` 拉取 Plus `GET /v0/management/api-keys`，把尚未绑定的 Key 加进来。已有名称/限额/启用状态不覆盖。新 Key 默认启用且不限额。Plus 目前只返回明文字符串、**没有显示名**，同步后的名字是 preview，可在 **编辑** 里改。

停用（`enabled: false`）只暂停限额，Plus 侧 Key 仍可用。解绑只删插件策略。从 Plus `api-keys` 删掉才会 401。

## 滚动窗口

日/周 USD 都是**滚动窗口**，不是自然日/自然周：

- 日：该 Key 本窗口第一笔计费起 24 小时，到期整窗清零
- 周：第一笔计费起 7×24 小时，到期整窗清零

卡片上显示下次重置时间。尚未计费显示「首次计费后起算」。列表按最后一次请求排序，最近用过的在最前。

## 客户端错误

HTTP **429**，OpenAI 形状的 `error`：

| 门控 | `error.type` / `error.code` | 说明 |
|---|---|---|
| 日或周 USD | `insufficient_quota` | message 里区分 daily / weekly |
| RPM | `rate_limit_exceeded` | `Retry-After: 60` |

未绑定、已停用的 Key 不会被本插件 429。

## 请求路径

```
客户端 Key
  → Plus api-keys 鉴权
  → request.intercept_before
       ├ 未绑定 / 策略停用 → 零动作
       └ 已绑定且启用
            ├ 超 RPM / 日 / 周 USD → Terminate 429
            └ 否则放行；usage.handle 按 Plus 价表记账
```

## 隐私

- **每把 Key 落盘：** 密钥的 `sha256:`、截断 preview、`caller_scope`、限额、用量。没有明文。
- **管理界面会话：** CPA management key 只放在内存（刷新页面需重新登录）。若在官方面板 iframe 里且勾了「记住密码」，可能复用面板已有的 `localStorage`；本插件自己不会写入该 key。
- **Plus 同步/价表：** `plus_management_key` 能列出明文 `api-keys`，按宿主密钥保管。插件在内存里哈希，不把这些字符串写入状态文件。`plus_base_url` 建议用 HTTPS 或 Docker 内网（compose 里常见 `http://` 回环）。公网明文 URL 会把 admin key 和全部 api-keys 打到网上。
- **状态文件：** CPA 进程能写到的路径（由运营配置），权限 600，不要提交 git。
- **管理 API：** `/v0/management/plugins/cpa-key-quota/*` 的鉴权在 **CPA 宿主**，插件自己不再验一次。没有 secret-key 不要把这些路径暴露出去。
- **界面嵌入：** 资源页带 `Content-Security-Policy: frame-ancestors 'self'`；只有父页面同源时才会复用面板「记住密码」的 key。
- **价表失败：** Plus 拉取失败时保留上一次成功的价表，避免 USD 限额被打掉。还没有任何价表时 USD 门关闭，RPM 仍有效。

已知且接受的边界：Plus 自己的 `GET /v0/management/api-keys` 会返回明文。本插件改不了这个接口。

## 测试

```bash
go test ./internal/policy/ ./internal/plugin/
cd web && npm test
```

Docker：`make e2e-docker`（Home 价表）、`make e2e-plus`（CPA-Manager-Plus）。说明见 [e2e/README.md](e2e/README.md)。
