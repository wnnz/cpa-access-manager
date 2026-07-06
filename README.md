# CPA Toolkit

CPA Toolkit 是一个 CLIProxyAPI/CPA 增强插件，用来给 CPA 增加更细的下游 API Key 管理、账号/供应商授权、额度控制、用量统计和账号计费能力。

当前插件显示名是 `CPA Toolkit`，内部插件 ID、配置 key、管理 API 路径和构建产物名统一为 `cpa-toolkit`，仓库地址为 `https://github.com/wnnz/cpa-toolkit`。

客户端仍然调用 CPA 原来的 OpenAI 兼容接口，例如 `/v1/chat/completions`、`/v1/responses`，只是把 `Authorization` 换成本插件生成的 `cam_...` Key。插件不会把生成的 Key 写入 CPA 原生 `api-keys`。

## 当前功能

- **API Key 管理**：创建、启用/停用、删除和轮换下游 `cam_...` Key。
- **脱敏展示**：列表只展示开头和结尾，中间使用星号，例如 `cam_abcd******WXYZ`；新建 Key 后 UI 不展示明文。
- **资源绑定**：Key 可直接关联 CPA 认证文件或具体 AI 提供商实例。
- **供应商库存同步**：可从 CPA 管理接口同步认证文件、Codex/Gemini/Claude/Vertex API Key 配置和 OpenAI Compatible 配置。
- **严格调度限制**：`scheduler.pick` 只会选择 Key 允许的账号或供应商实例，没有可用候选时拒绝请求，不回退到未授权账号。
- **模型路由辅助**：`model.route` 会根据 Key 的授权范围选择可用 provider。
- **额度控制**：支持滚动 5 小时额度、滚动 7 天额度和总额度，单位为 USD。
- **用量账本**：通过 CPA `usage.handle` 接收真实 token 用量，记录请求数、输入 token、输出 token、缓存 token、推理 token、模型、供应商、账号和时间。
- **账号计费基础**：支持按 `provider + model` 配置输入、输出和 cache-read 单价，计算 USD 并写入用量账本。
- **今日用量**：API Key 列表中展示当天请求数、Token 和账号计费金额。
- **CPA 登录复用**：插件 UI 从 CPA 管理后台同源 iframe 中复用管理会话，也支持在设置弹框中临时填写 Management Key。

## 当前界面

当前内嵌 UI 的功能菜单显示为：

```text
API Key管理
```

页面聚焦 API Key 管理：列表、筛选、搜索、创建/编辑抽屉、资源绑定、额度展示、今日用量和启停/删除操作。

库存、价格和分组目前主要作为后端管理能力保留，不再作为独立页面展示。后续如果 CPA Toolkit 扩展到完整用量统计、账号计费规则、供应商管理等功能，可以继续按功能增加菜单项。

## 工作方式

插件组合使用 CPA 插件协议的多种能力：

- `frontend_auth`：识别插件生成的 Key，检查启用状态、额度和绑定。
- `model.route`：根据 Key 授权范围选择 provider。
- `scheduler.pick`：在 CPA 候选账号中筛选出授权账号。
- `usage.handle`：请求完成后记录真实 token 用量并计算费用。
- `management_api`：提供管理 API 和内嵌 Web UI。

默认使用 SQLite 保存数据：

```text
/CLIProxyAPI/plugins/cpa-toolkit.db
```

## 安装方式

### 1. 构建插件

插件需要在 Linux 环境构建 `.so`：

```bash
git clone https://github.com/wnnz/cpa-toolkit.git
cd cpa-toolkit

CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build -buildmode=c-shared \
  -o dist/cpa-toolkit.so ./cmd/cpa-toolkit
```

如果系统没有 Go 或 gcc：

```bash
apt update
apt install -y golang build-essential
```

也可以使用 Docker 构建：

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.24-bookworm \
  bash -lc 'go test ./... && mkdir -p dist && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildmode=c-shared -o dist/cpa-toolkit.so ./cmd/cpa-toolkit'
```

### 2. 放入 CPA 插件目录

Linux amd64 推荐放到：

```bash
install -m 0755 -D dist/cpa-toolkit.so \
  /opt/cli-proxy-api/plugins/linux/amd64/cpa-toolkit.so
```

如果是从旧版 `cpa-access-manager` 升级，建议只保留新的 `cpa-toolkit.so`，避免 CPA 同时加载两个插件。

如果 CPA 跑在 Docker 容器中，确保宿主机目录挂载到容器内：

```yaml
volumes:
  - ./plugins:/CLIProxyAPI/plugins
```

### 3. 修改 CPA 配置

Docker 场景下，推荐使用容器内路径：

```yaml
plugins:
  enabled: true
  dir: "/CLIProxyAPI/plugins"
  configs:
    cpa-toolkit:
      enabled: true
      priority: 1
      db_path: "/CLIProxyAPI/plugins/cpa-toolkit.db"
      allow_unpriced: false
```

非 Docker 场景可以使用本机路径：

```yaml
plugins:
  enabled: true
  dir: "/opt/cli-proxy-api/plugins"
  configs:
    cpa-toolkit:
      enabled: true
      priority: 1
      db_path: "/opt/cli-proxy-api/plugins/cpa-toolkit.db"
      allow_unpriced: false
```

从旧版 `cpa-access-manager` 迁移时，需要同步改这三处：

```bash
cp /opt/cli-proxy-api/plugins/cpa-access-manager.db \
  /opt/cli-proxy-api/plugins/cpa-toolkit.db
```

```yaml
plugins:
  configs:
    cpa-toolkit:
      db_path: "/opt/cli-proxy-api/plugins/cpa-toolkit.db"
```

同时删除或移走旧的 `/opt/cli-proxy-api/plugins/linux/amd64/cpa-access-manager.so`。

### 4. 重启 CPA

```bash
cd /opt/cli-proxy-api
docker compose restart cli-proxy-api
```

查看日志确认插件加载：

```bash
docker compose logs --tail=100 cli-proxy-api | grep cpa-toolkit
```

插件资源入口：

```text
/v0/resource/plugins/cpa-toolkit/index.html
```

## 管理后台会话复用

UI 参考 `cpa-plugin-key-policy` 的方式复用 CPA 管理后台登录：

- 插件页由 CPA 管理后台同源 iframe 嵌入打开时，会读取管理后台保存的 `cli-proxy-auth`。
- 直接打开插件资源页时不会扫描 CPA 管理后台的登录缓存。
- 兼容 CPA 管理后台新版 `enc::v1::` 本地混淆格式，也兼容未混淆的 `cli-proxy-auth`。
- 如果管理后台只是临时登录、没有保存会话，插件页会提示从 CPA 管理后台重新打开。
- UI 不展示 CPA Base URL；Management Key 只保存在当前页面会话中，也可以通过设置弹框临时填写或修改。
- 加载数据前会先请求 `/status` 验证管理会话，验证失败时不会并发请求多个管理接口，避免更容易触发 CPA 的 IP 封禁。

## 管理 API

管理 API 使用 CPA 原来的 management key：

```text
GET/POST/PATCH/DELETE /v0/management/plugins/cpa-toolkit/keys
POST                  /v0/management/plugins/cpa-toolkit/keys/rotate
GET/POST/PATCH/DELETE /v0/management/plugins/cpa-toolkit/groups
GET/POST              /v0/management/plugins/cpa-toolkit/inventory
GET/PUT               /v0/management/plugins/cpa-toolkit/prices
GET                   /v0/management/plugins/cpa-toolkit/usage
GET                   /v0/management/plugins/cpa-toolkit/status
```

注意：CPA 插件管理路由是精确路径匹配，`id` 使用 query string 或 JSON body 传递。

## 数据模型

### Key 绑定

Key 可以绑定多个目标，结果取并集：

- `auth_id`：CPA 认证文件稳定 ID。
- `provider_instance`：具体 AI 提供商实例。
- `group`：资源分组，后端 API 保留。

当前 UI 直接选择认证文件和 AI 提供商实例，并要求所选资源属于同一 AI 类型，避免一个 Key 混用不同 provider。

### 资源库存

库存项分为：

- `auth_file`：CPA 认证文件。
- `provider_instance`：AI 提供商实例，例如 CPA 配置中的 `codex-api-key` 或 OpenAI Compatible provider。

刷新库存时，插件会读取 CPA 管理接口并同步展示快照。API Key 秘钥只做脱敏快照，不保存完整明文。

### 额度

- 额度单位是 USD。
- 字段为 `five_hour_limit_usd`、`weekly_limit_usd`、`total_limit_usd`。
- 5 小时额度是滚动 5 小时窗口。
- 周额度是滚动 7 天窗口。
- 总额度不自动重置。
- 请求开始前只检查已记录用量是否达到额度；请求完成后按真实 token 用量记账，所以单次请求可能略微打超额度。

### 计费与用量

用量账本保存：

- API Key ID
- 认证文件 / 账号 ID
- AI provider
- provider instance ID
- model / alias
- input tokens
- output tokens
- reasoning tokens
- cached tokens
- cache-read tokens
- cache-creation tokens
- total tokens
- USD
- 请求完成时间

当前价格规则按 `provider + model` 配置：

- `input_usd_per_million`
- `output_usd_per_million`
- `cache_read_usd_per_million`

计算方式：

```text
USD =
  (input_tokens - cache_read_tokens) * input_usd_per_million / 1_000_000
+ output_tokens * output_usd_per_million / 1_000_000
+ cache_read_tokens * cache_read_usd_per_million / 1_000_000
```

没有价格规则时，插件仍会记录请求数和 token，USD 记为 0。对于启用 USD 额度的 Key，请求开始前仍会要求对应模型有价格规则，以避免绕过额度控制。

## API 示例

刷新库存：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-toolkit/inventory" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"refresh":true}'
```

写入价格规则：

```bash
curl -X PUT "$CPA/v0/management/plugins/cpa-toolkit/prices" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"rules":[{"provider":"codex","model":"gpt-test","input_usd_per_million":1,"output_usd_per_million":4,"cache_read_usd_per_million":0.25}]}'
```

创建 Key：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-toolkit/keys" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"name":"team-a","enabled":true,"weekly_limit_usd":10,"bindings":[{"target_type":"provider_instance","target_id":"codex:codex:apikey:xxxx"}]}'
```

更新 Key 绑定或额度：

```bash
curl -X PATCH "$CPA/v0/management/plugins/cpa-toolkit/keys?id=key_xxx" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"five_hour_limit_usd":1,"weekly_limit_usd":10,"total_limit_usd":100,"bindings":[{"target_type":"auth_id","target_id":"codex-user.json"}]}'
```

轮换 Key：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-toolkit/keys/rotate?id=key_xxx" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY"
```

查询用量：

```bash
curl "$CPA/v0/management/plugins/cpa-toolkit/usage?key_id=key_xxx&limit=50" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY"
```

## 生产注意

- 如果仍保留 CPA 原生下游 Key，它们不受 CPA Toolkit 的额度和授权控制；生产建议使用插件独占鉴权或移除原生下游 Key。
- `scheduler.pick` 没有授权候选账号时会返回错误，让请求失败，不会回退到 CPA 默认调度。
- `usage.handle` 是请求完成后的记账入口，历史上未写入账本的请求无法自动补回。
- 客户端只应拿到 `cam_...` 明文 Key；管理 API 返回的 key id 不能作为 Bearer token 使用。
- 从旧版 `cpa-access-manager` 升级时，要同步迁移插件文件名、CPA 配置 key、管理 API 路径和 SQLite 数据库路径。

## 开发验证

```bash
go test ./...
go build ./cmd/cpa-toolkit
```

Windows 本地普通 `go build` 只用于验证非 cgo stub。正式 `.so` 必须用 Linux + `CGO_ENABLED=1` 构建。
