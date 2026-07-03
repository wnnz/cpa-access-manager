# cpa-access-manager

`cpa-access-manager` 是一个独立的 CLIProxyAPI/CPA 插件，用来给 CPA 增加轻量级下游 Key 管理、账号/供应商授权、分组和 USD 额度控制能力。

它不改 CPA 内核，也不把插件生成的 Key 写入 CPA 原生 `api-keys`。客户端仍然调用 CPA 原来的 OpenAI 兼容接口，例如 `/v1/chat/completions`、`/v1/responses`，只是把 `Authorization` 换成本插件生成的 `cam_...` Key。

## 主要功能

- **下游 Key 管理**：创建、禁用、删除、轮换 `cam_...` Key。
- **账号绑定**：Key 可以绑定 CPA auth id、具体 provider instance，或绑定资源分组。
- **分组管理**：把认证文件和 API 供应商实例放进统一资源分组，方便按团队或用途授权。
- **严格调度**：`scheduler.pick` 只会选择 Key 被授权的账号，没有可用账号时直接拒绝请求，不会回退到 CPA 默认调度。
- **模型路由**：`model.route` 会尽量把请求路由到 Key 被授权的 provider。
- **USD 额度**：支持滚动 5 小时额度、滚动 7 天额度和总额度。
- **真实用量记账**：请求完成后从 CPA `usage.handle` 接收真实 token 用量，按价格规则计算 USD 并写入 SQLite 账本。
- **管理 UI**：内嵌 Web UI，可管理 Key、分组、库存、价格和用量。

## 工作方式

插件使用 CPA 的插件能力组合实现访问控制：

- `frontend_auth`：识别插件生成的 Key，检查启用状态、额度和基础绑定。
- `model.route`：根据 Key 授权范围选择 provider。
- `scheduler.pick`：在 CPA 候选账号中筛选出授权账号。
- `usage.handle`：请求结束后计算 USD 并写入账本。
- `management_api`：提供管理 API 和内嵌页面。

默认使用 SQLite 保存数据，建议放在 CPA 数据目录或插件目录下，例如：

```text
/CLIProxyAPI/plugins/cpa-access-manager.db
```

## 安装方式

### 1. 构建插件

插件需要在 Linux 环境构建 `.so`：

```bash
git clone https://github.com/wnnz/cpa-access-manager.git
cd cpa-access-manager

CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
  go build -buildmode=c-shared \
  -o dist/cpa-access-manager.so ./cmd/cpa-access-manager
```

如果系统没有 Go 或 gcc：

```bash
apt update
apt install -y golang build-essential
```

### 2. 放入 CPA 插件目录

CPA 会扫描插件目录下的系统/架构子目录。Linux amd64 推荐放到：

```bash
install -m 0755 -D dist/cpa-access-manager.so \
  /opt/cli-proxy-api/plugins/linux/amd64/cpa-access-manager.so
```

如果 CPA 跑在 Docker 容器中，确保宿主机目录挂载到了容器内：

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
    cpa-access-manager:
      enabled: true
      priority: 1
      db_path: "/CLIProxyAPI/plugins/cpa-access-manager.db"
      allow_unpriced: false
```

非 Docker 场景可以把 `dir` 和 `db_path` 改成本机实际路径，例如：

```yaml
plugins:
  enabled: true
  dir: "/opt/cli-proxy-api/plugins"
  configs:
    cpa-access-manager:
      enabled: true
      priority: 1
      db_path: "/opt/cli-proxy-api/plugins/cpa-access-manager.db"
      allow_unpriced: false
```

### 4. 重启 CPA

```bash
cd /opt/cli-proxy-api
docker compose restart cli-proxy-api
```

查看日志确认插件加载：

```bash
docker compose logs --tail=100 cli-proxy-api | grep cpa-access-manager
```

UI 入口：

```text
/v0/resource/plugins/cpa-access-manager/index.html
```

管理 API 使用 CPA 原来的 management key：

```text
GET/POST/PATCH/DELETE /v0/management/plugins/cpa-access-manager/keys
POST                  /v0/management/plugins/cpa-access-manager/keys/rotate
GET/POST/PATCH/DELETE /v0/management/plugins/cpa-access-manager/groups
GET/POST              /v0/management/plugins/cpa-access-manager/inventory
GET/PUT               /v0/management/plugins/cpa-access-manager/prices
GET                   /v0/management/plugins/cpa-access-manager/usage
```

注意：CPA 插件管理路由是精确路径匹配，`id` 使用 query string 或 JSON body 传递。

## 数据模型与额度

- Key 不写入 CPA 原生 `api-keys`，客户端继续调用 CPA 原来的 `/v1/chat/completions`、`/v1/responses` 等接口，`Authorization` 使用本插件生成的 `cam_...`。
- Key 可绑定多个目标，结果取并集：
  - `group`：资源分组。
  - `auth_id`：CPA 认证文件稳定 ID。
  - `provider_instance`：具体供应商实例，ID 形如 `provider:auth_id`。
- 分组成员可以是：
  - `auth_file`
  - `provider_instance`
- 额度单位是 USD，字段是 `five_hour_limit_usd`、`weekly_limit_usd`、`total_limit_usd`。
- 5 小时额度是滚动 5 小时窗口，周额度是滚动 7 天窗口，总额度不自动重置。
- 请求开始前只检查已记录用量是否达到额度；请求完成后按真实 token 用量记账，所以单次请求可能略微打超额度。
- 对启用 USD 额度的 Key，模型缺少价格规则时默认拒绝，避免绕过额度。

## 管理 API 示例

刷新库存：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-manager/inventory" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"refresh":true}'
```

手工写入一个 provider instance：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-manager/inventory" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"items":[{"id":"codex:auth-a","type":"provider_instance","provider":"codex","auth_id":"auth-a","name":"Codex account A"}]}'
```

创建分组：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-manager/groups" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"id":"team-a-codex","name":"Team A Codex","members":[{"member_type":"provider_instance","member_id":"codex:auth-a"}]}'
```

写入价格规则：

```bash
curl -X PUT "$CPA/v0/management/plugins/cpa-access-manager/prices" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"rules":[{"provider":"codex","model":"gpt-test","input_usd_per_million":1,"output_usd_per_million":4,"cache_read_usd_per_million":0.25}]}'
```

创建 Key：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-manager/keys" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"name":"team-a","enabled":true,"weekly_limit_usd":10,"bindings":[{"target_type":"group","target_id":"team-a-codex"}]}'
```

更新 Key 绑定或额度：

```bash
curl -X PATCH "$CPA/v0/management/plugins/cpa-access-manager/keys?id=key_xxx" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY" \
  -H "content-type: application/json" \
  -d '{"five_hour_limit_usd":1,"weekly_limit_usd":10,"total_limit_usd":100,"bindings":[{"target_type":"provider_instance","target_id":"codex:auth-a"}]}'
```

轮换 Key：

```bash
curl -X POST "$CPA/v0/management/plugins/cpa-access-manager/keys/rotate?id=key_xxx" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY"
```

查询用量：

```bash
curl "$CPA/v0/management/plugins/cpa-access-manager/usage?key_id=key_xxx&limit=50" \
  -H "Authorization: Bearer $CPA_MANAGEMENT_KEY"
```

## 生产注意

- 如果仍保留 CPA 原生下游 Key，它们不受本插件额度控制；生产建议使用插件独占鉴权或移除原生下游 Key。
- `scheduler.pick` 没有授权候选账号时会返回错误，让请求失败，不会回退到 CPA 默认调度。
- `usage.handle` 依赖 CPA 将 frontend auth 的 principal 写入 usage record 的 `APIKey` 字段；当前 CPA 插件协议支持该链路。
- 只启用额度但不配置价格规则时，请求会被拒绝。这是为了避免模型未定价导致免费绕过额度。
- 客户端只应拿到 `cam_...` 明文 Key；管理 API 返回的 key id 不能作为 Bearer token 使用。

## 开发验证

```bash
go test ./...
go build ./cmd/cpa-access-manager
```

Windows 本地普通 `go build` 只用于验证非 cgo stub。正式 `.so` 必须用 Linux + `CGO_ENABLED=1` 构建。
