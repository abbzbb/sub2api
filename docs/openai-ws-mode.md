# OpenAI Responses WebSocket 模式（WS mode）

WS mode 控制 OpenAI Responses 请求在网关与上游之间如何传输。它同时涉及两件不同的事：

1. **客户端 WS ingress**：客户端用 WebSocket 连接 `/v1/responses`，网关再把 turn 转发到上游。
2. **HTTP 流式 WS 加速**：客户端仍用 HTTP/SSE，但网关用上游 WebSocket 连接池承载请求，提前发出 `response.created` 等前置事件。

第 2 项只对普通 OAuth 账号的首轮流式请求生效，单独由 `openai_oauth_ws_sse_acceleration` 控制，详见 [OpenAI OAuth HTTP 流式 WS 加速](openai-ws-sse-acceleration.md)。本文主要说明第 1 项的模式、开关、连接池和排障。

## 开关层级

WS mode 的最终行为由全局配置和账号字段共同决定。全局开关关闭或未开启 v2 路由时，账号级设置不会按预期生效。

### 全局配置

| 配置 | 默认 | 作用 |
| --- | --- | --- |
| `gateway.openai_ws.enabled` | `true` | WS 总开关；关闭后所有请求保持 HTTP/SSE |
| `gateway.openai_ws.oauth_enabled` | `true` | 是否允许 OAuth 账号使用 WS |
| `gateway.openai_ws.apikey_enabled` | `true` | 是否允许 API Key 账号使用 WS |
| `gateway.openai_ws.responses_websockets_v2` | `true` | v2 协议开关；v2 优先于 v1 `responses_websockets` |
| `gateway.openai_ws.force_http` | `false` | 紧急回滚：强制上游使用 HTTP/SSE |
| `gateway.openai_ws.mode_router_v2_enabled` | `false` | **账号级 WS mode 的总开关** |
| `gateway.openai_ws.ingress_mode_default` | `ctx_pool` | 新会话默认模式，仅在 v2 路由开启时生效 |

`mode_router_v2_enabled=false` 时，账号级 WS mode（包括 `http_bridge`）被忽略，统一按 legacy `ctx_pool` 行为工作。对应环境变量为 `GATEWAY_OPENAI_WS_MODE_ROUTER_V2_ENABLED=true`；修改全局配置或环境变量后需要重启网关。

### 账号字段

OAuth 与 API Key 使用不同字段，不能混用：

| 账号类型 | 启用字段 | 模式字段 |
| --- | --- | --- |
| OAuth / setup-token | `accounts.extra.openai_oauth_responses_websockets_v2_enabled` | `accounts.extra.openai_oauth_responses_websockets_v2_mode` |
| API Key | `accounts.extra.openai_apikey_responses_websockets_v2_enabled` | `accounts.extra.openai_apikey_responses_websockets_v2_mode` |

兼容字段（在分类型字段缺失时回退）：

- `accounts.extra.responses_websockets_v2_enabled`
- `accounts.extra.openai_ws_enabled`

解析优先级：分类型模式字符串 → 分类型布尔值 → 兼容布尔值 → 全局默认。布尔值 `true` 等价于 `ctx_pool`，`false` 等价于 `off`。历史值 `shared` / `dedicated` 会归并为 `ctx_pool`。

账号级强制 HTTP 字段为 `accounts.extra.openai_ws_force_http`；为 `true` 时该账号不使用上游 WebSocket。

### 后台选项

账号编辑页的「WS mode」下拉框写入分类型模式字段：

| 选项 | 值 | 行为 |
| --- | --- | --- |
| 关闭 | `off` | 禁用该账号的 WS |
| 上下文池 | `ctx_pool` | 复用上游 WS 连接池，推荐用于 OAuth |
| 透传 | `passthrough` | 直接把客户端 WS 会话转发到上游 |
| HTTP 桥接 | `http_bridge` | WS ingress 转 HTTP 上游，下游仍按 WS 返回 |

## 四种模式

### `ctx_pool`（上下文池）

- 网关从上游 WS 连接池取连接并复用；客户端会话在整个生命周期内持有一条上游连接。
- 适合 OAuth 账号、需要低开销多轮会话的客户端。
- 同时是「HTTP 流式 WS 加速」的推荐模式：HTTP 客户端也能使用该连接池，提前收到前置事件。
- 连接数、空闲裁剪和并发系数见下文「连接池与 1013」。

### `passthrough`（透传）

- 客户端 WS 帧直接透传到上游，网关不按 `ctx_pool` 的会话池语义重新排队。
- 客户端需要自己处理 WS 会话、重叠 `response.create` 和取消语义；重叠请求会被拒绝。
- 适合明确要求原生 WS 语义、且上游 WS 可用的客户端。

### `http_bridge`（HTTP 桥接）

- 客户端仍用 WS ingress，但网关把 turn 转成 HTTP 请求发往上游，再把 SSE 事件包装成 WS 事件返回。
- 支持 `previous_response_id` 等 HTTP 侧语义，不依赖上游 WS 是否可用。
- 适用于：上游 WS 返回 1013 / “no available account”、上游 WS 不增量流式、或大输出请求。
- 它不是原生 WS 加速；上游仍走 HTTP/SSE，延迟特性以 HTTP 路径为准。

### `off`（关闭）

- 该账号不使用 WS；客户端 WS 请求会回退或按现有 HTTP 路径处理，具体取决于客户端和网关的回退逻辑。

## OAuth 账号推荐配置

### 只用 WS ingress

1. 全局：`enabled=true`、`oauth_enabled=true`、`responses_websockets_v2=true`、`force_http=false`、`mode_router_v2_enabled=true`。
2. 账号：WS mode 选择「上下文池」，保存后对应
   `openai_oauth_responses_websockets_v2_enabled=true`、
   `openai_oauth_responses_websockets_v2_mode=ctx_pool`。
3. 确认账号不是 `error`、`schedulable=false`，且没有开启 SIWC、Copilot SDK 或 Excel BPS all-models。

### 同时启用 HTTP 流式 WS 加速

在第 1、2 步基础上：

1. 勾选账号的「HTTP 流式 WS 加速」，即
   `openai_oauth_ws_sse_acceleration=true`。
2. 关闭「自动透传」；该加速路径要求 `IsOpenAIPassthroughEnabled()==false`。
3. 只对 HTTP 客户端、`POST .../responses`、`stream=true`、且**没有** `previous_response_id` 的首轮请求生效；非流式、`/responses/compact`、Messages 兼容桥接和 chained turn 继续走原传输。

「WS mode」下拉框和「HTTP 流式 WS 加速」是两个独立开关：前者控制客户端 WS ingress / 上游传输，后者只控制 OAuth 的 HTTP 首轮流式加速。

## 会静默关闭 WS 的条件

### WS mode 不生效

以下情况即使下拉框选了 `ctx_pool`，WS 也不会按账号配置生效：

- `mode_router_v2_enabled=false`：账号级模式被忽略，统一 legacy `ctx_pool`。
- 全局 `gateway.openai_ws.force_http=true`，或账号 `openai_ws_force_http=true`。
- OAuth 账号开启 SIWC（`openai_siwc=true`）。
- 账号开启 Copilot SDK（`openai_copilot_sdk=true`）。
- Excel BPS 已开启且没有配置 `openai_excel_bps_models` 模型范围（all-models 模式）。
- 账号 `status=error`、`schedulable=false`，或没有可用并发槽位。

### HTTP 流式 WS 加速额外不生效

该项只适用于普通 OAuth 账号，且要求：

- 客户端使用 HTTP Responses，`POST .../responses`，`stream=true`；
- 请求没有 `previous_response_id`（只覆盖首轮）；
- 不是非流式、`/responses/compact` 或 Messages 兼容桥接；
- 自动透传关闭（`IsOpenAIPassthroughEnabled()==false`）；
- 没有插件把该 OAuth 账号路由到自己的传输链；
- 账号不是影子账号、Agent Identity、Personal Access Token 或 BPS 协议账号。

这些条件只影响“HTTP 流式 WS 加速”，不一定会关闭客户端 WS ingress；WS ingress 的可用性按账号类型、全局开关和上游能力判断。

## 连接池与 1013

`ctx_pool` 的关键配置：

| 配置 | 默认 | 说明 |
| --- | --- | --- |
| `max_conns_per_account` | `128` | 每个账号的上游 WS 连接上限 |
| `min_idle_per_account` | `4` | 每账号保留的空闲连接 |
| `max_idle_per_account` | `12` | 空闲连接裁剪上限 |
| `dynamic_max_conns_by_account_concurrency_enabled` | `true` | 按账号并发动态计算连接上限 |
| `oauth_max_conns_factor` / `apikey_max_conns_factor` | `5.0` | 动态上限系数 |
| `pool_target_utilization` | `0.7` | 目标利用率 |
| `queue_limit_per_conn` | `64` | 单连接排队上限 |
| `max_ingress_connections_per_api_key` | `512` | 单个 API Key 的活跃 WS ingress 会话上限 |

`ctx_pool` 下，一个客户端会话在生命周期内持有一条上游连接。活跃会话数接近 `max_conns_per_account`（或动态上限）时，新会话可能收到：

```text
1013 upstream websocket is busy
```

这是**本地连接池**耗尽，不是上游拒绝。处理方式：提高 `max_conns_per_account` / 动态系数，降低单 Key 活跃会话数，或调整 `ingress_inter_turn_idle_timeout_seconds` 与空闲连接裁剪（`min_idle_per_account` / `max_idle_per_account`）后观察 `queue_wait_ms`。

上游也可能返回 1013，例如：

```text
close frame: status = StatusTryAgainLater
reason = "no available account"
```

这是**上游供应商的 WS 账号池**没有可用账号，与本地连接池无关。典型表现：WebSocket 握手返回 `101`，但第一条 `response.create` 后立即收到 close frame。处理方式：

1. 找上游确认该 key 是否开通 WS、WS 账号池是否有容量；
2. 暂时把该账号改成 `http_bridge`，让 WS ingress 走上游 HTTP；
3. 或把该账号从 WS 调度中排除，改用有 WS 容量的上游。

握手 `101` 只能证明端点接受 Upgrade，不能证明该 turn 能成功；以后续 close frame / 首事件为准。

## 排障

| 现象 | 先检查 | 处理 |
| --- | --- | --- |
| 下拉框选了 `ctx_pool`/`http_bridge` 但行为没变 | `mode_router_v2_enabled` 是否为 `true` | 打开全局开关并重启；否则账号级模式被忽略 |
| 面板显示「流式」而不是「WS」 | 客户端是否真的用 WS ingress；`usage_logs.openai_ws_mode` | 客户端用 HTTP 时不会出现 WS；WS 失败后也可能回退 HTTP |
| 有 WS 连接但 0 成功 | `conn_reader_loop_closed` 的 close status/reason | 若为上游 1013 `no available account`，换 `http_bridge` 或换供应商 |
| 本地报 `1013 upstream websocket is busy` | `max_conns_per_account`、动态系数、活跃会话数 | 扩池或减少会话；确认 `queue_wait_ms` 是否升高 |
| 大输出请求首 token 很高 | 是否 WS ingress、输出 token 数、上游是否增量流式 | 大输出优先走 HTTP 或 `http_bridge`；不要只看首 token 指标 |
| 账号明明 active 但仍不走 WS | SIWC / Copilot SDK / Excel BPS all-models / `openai_ws_force_http` / 账号状态 | 关闭冲突开关或换账号 |
| HTTP 流式加速不生效 | 是否首轮、是否 HTTP、`stream=true`、是否有 `previous_response_id`、自动透传是否关闭 | 该开关只覆盖首轮流式请求 |

常用观测：

- `usage_logs.openai_ws_mode`：客户端是否使用 WS ingress；它**不表示**上游最终协议，`http_bridge` 时上游是 HTTP。
- `ingress_ws_protocol_confirm`：`account_type`、`transport`、`ws_host`、`ws_path`、`ws_mode`。
- `ingress_ws_upstream_connected`：`conn_reused`、`conn_idle_ms`、`queue_wait_ms`、`preferred_conn_id`。
- `conn_reader_loop_closed`：上游 close status / reason，用于区分本地池和上游池。
- `ingress_ws_turn_chain` / `ingress_ws_next_turn_chain`：多轮会话的 turn 编号和 `previous_response_id` 链。

## 验证边界

本文说明的是当前代码中的开关、默认值和日志字段，不构成生产性能结论。WS 连接是否成功应以实际 turn 的首事件和 close frame 为准；握手成功不代表请求成功。排障时使用脱敏的账号、连接 ID 和 close reason，不要记录请求正文、凭据或真实用户数据。

## 相关文档

- [OpenAI OAuth HTTP 流式 WS 加速](openai-ws-sse-acceleration.md)
- [OpenAI SIWC](openai-siwc.md)
- [Copilot SDK Codex](copilot-sdk-codex.md)
- [OpenAI HTTP 阶段超时](openai-http-phase-deadlines.md)
