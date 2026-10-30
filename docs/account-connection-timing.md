# 账号连接测试耗时设计（实现中，远端验收记录随 PR 更新）

## 1. 范围与基线
基于 production 77455cf755fa0f119367b911704f11df88e7917e，保留 API-key/OAuth 边界修复。仅增强账号管理测试连接事件和 UI；不修改 hi 请求语义、计费、普通代理路由。独立工作树 /data/development/projects/sub2api-account-connection-timing。

## 2. 调用链与复用
已核实两处入口 components/account/AccountTestModal.vue 与 components/admin/account/AccountTestModal.vue 均 POST /api/v1/admin/accounts/:id/test → admin.AccountHandler.Test → AccountTestService.TestAccountConnection → 平台分支。OpenAI 的 API-key 根据 Responses 支持标志走 Responses 或 Chat Completions；OAuth 走 Codex Responses，也存在 BPS/SIWC/Prism/Agent Identity、图片与 compact 分支。其他分支包含 Claude/Vertex/Bedrock、Gemini API-key/OAuth/service-account、Antigravity、Grok 多种媒体/Realtime、TypeSafe 与自适应协议。全部共用开始/最终事件；未接入 HTTP trace/语义解析的路径明确不可测。优先复用 internal/pkg/requesttiming 的 HTTP trace、首内容/输出阶段观察；禁止直接向 UI 序列化含账号/代理/路由信息的 Snapshot，使用安全字段白名单投影。

## 3. 测量口径
请求发起/完成：测试 handler 入口（后台直接调用则 service 入口）→ service 完成上游 body close 与 keepalive 清理；不包含后续 handler 限流恢复，RFC3339 带时区；UI 显示浏览器本地时区名称/偏移。总耗时：同一单调时钟的起止差。
连接：实际 HTTP trace 连接开始→获取连接（复用连接明确标识，不臆造 TCP/TLS 耗时）。等待响应头：请求写出→响应头可用；首内容：上游请求开始→首个非空语义内容，不认 SSE 注释/心跳/首字节为 token。流输出：首语义内容→流终止；非流首内容与流输出显示不适用。阶段均声明起止、与总耗时及其他阶段重叠关系，绝不相加推导模型计算时间。
本地处理只计显式可观察处理区间；无法隔离网络等待时显示 — 和原因，不使用总耗时减网络作为模型处理时间。无观察/未到阶段/连接复用/非流必须区分，缺失不可填 0。

## 4. 事件兼容与终态
增加可选 timing 事件/字段，保留既有事件。区分成功、错误、超时、取消以及不完整 EOF。覆盖 API-key/OAuth，流/非流，前置校验失败与中途失败。取消时如终态无法送达，UI 需诚实显示客户端取消与未获服务端最终指标，不伪造完成时间。

## 5. UI
紧凑摘要显示开始/完成、总耗时、首内容；详细阶段默认折叠，支持移动端换行。中英文文案、各指标起止及不可测原因；旧服务端没有 timing 时保留原体验并显示不可用，不误报 0。

## 6. 安全与不变项
不暴露凭据、请求头/正文、真实 IP/域名/代理/连接细节。无生产或付费账号调用，无生产数据。无 merge、release、deploy。

## 7. 验收
本机禁止 Go build/test/run/generate、Docker、前端动态测试/构建；只读、gofmt、diff 静态检查允许。动态全部专用远端 CI。隔离 synthetic HTTP/SSE 测试延迟响应头、心跳先行、真实首内容、错误/超时/取消；真实 handler→事件→UI 消费序列，组件不能仅注入 fabricated timing 字段。可控 clock 验证范围/单调性，避免靠紧 sleep 断言。CI 验证真实 service/handler 编译和前端定向测试，记录 exact SHA、run/job URL、raw 日志。

## 8. 交付与披露
尽早发布包含 reviewable 增量的 Draft PR，8 节正文按模板列出完成/未完成与真实验证。Codex 请求模型 gpt-6.1-sol / xhigh，必须由实际 session metadata 核实后披露；Hermes 协调模型 gpt-6.1-sol，推理强度工具未提供。此文件为早期设计而非通过证明。

实现进度：已提前发布 Draft #602。共用 timing SSE 开始/最终事件；HTTPUpstream 仅测试分支的适配器复用 StartAttempt/StartTransport；四种流解析器在非空白可见文本处调用已有 Output/Stage 与首内容 Mark。Gemini/Claude 的无终止标志 EOF 现在明确报错。两处真实弹窗复用摘要/折叠面板和共享 reader，支持浏览器本地 IANA 时区与 UTC 偏移，取消/换账号/重试/卸载时隔离旧事件并清理 reader。新回归先于相应实现写入；本机未运行任何动态命令。远端已完成首轮隔离基线 RED，GREEN 尚未完成。

实际 session 核实：session 01a18d6e-f0f1-72c1-8e3e-84c145366772 的 turn_context.model=gpt-6.1-sol、effort=xhigh，cwd 匹配本隔离树；未公开原始 session 内容。Hermes 协调 gpt-6.1-sol，推理强度不适用（工具未提供）。Codex 为唯一实现者，未使用子 Agent。

网络阶段只在单次 egress 上呈现；如真实 repository 提供单次物理 HTTP trace，则优先使用该 trace 的连接与响应头边界。多次请求/物理重试显示不可测，collector 达到上限不呈现不完整指标。首内容起点仍是单次上游调用开始，与连接/头等待重叠。非流响应的首内容和流输出不适用。未提供实时解析边界的 gateway/插件/媒体/Realtime 分支不从诊断 content 事件伪造首 token；仅呈现实际观测的网络与生命周期数据。

本地处理严格限定为测试 SSE 事件 JSON 编码，写出前逐段 Observe；时长为不连续片段之和，偏移是外包区间，不含写出/背压/上游等待/最终 timing 事件或未采集处理。它不是整体 CPU 或模型计算时间。

synthetic 验收链：专用 workflow 在 baseline archive 运行原 API 回归核实 RED；精确 head 使用真实 requesttiming/service/admin handler packages 与 repository HTTP transport，通道控制 headers/content/terminal 门；捕获 .sse artifact 后由两处真实 Vue 组件消费，覆盖英文/中文。单独可控 clock 验证具体毫秒与范围，不靠窄 sleep。取消/超时通过可控 context 与真实 HTTP 验证上游和 handler 清理。当前远端结果仍待核查，尚非通过证明。
