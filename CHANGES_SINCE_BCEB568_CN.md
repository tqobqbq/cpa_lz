# 相对基线 bceb568 的修改记录与用意说明

本文档详细记录当前工作区(含已提交 `e52aabf` 与未提交改动)相对基线提交 `bceb568c5e06a1917e7537604b0b496315f84f2f` 的全部修改及其用意。

## 总览

| 层次 | 文件数 | 增/删行 |
| --- | --- | --- |
| 全部(基线 → 工作区) | 309 | +15987 / −42796 |
| 已提交层(`bceb568..e52aabf`,"Record CPA runtime changes") | 300 | +13193 / −42320 |
| 未提交层(工作区增量) | 67 | +3518 / −1200 |

改动可归纳为两条主线:

1. **功能主线**:围绕"上游失败的精细化控制与可观测性"展开——新增输出过滤(output-filter)、错误控制(error-control,轮次重试+指数退避)、供应商冷却(provider-cooldown)、并行探测请求、流式响应语义校验、更细维度的用量统计与请求明细、管理端配套 UI。
2. **发布清理主线**:该仓库准备上传到 `git@github.com:tqobqbq/cpa_lz.git`,为此删除了上游项目的 CI、文档、示例与全部测试文件,并移除源码内置的 Google OAuth 客户端凭据(改为环境变量),避免仓库携带可用密钥或被 GitHub push protection 拦截。

未提交层主要是最新一轮迭代:**供应商冷却(provider-cooldown)与并行请求调度**、Codex Responses SSE 元数据修复、请求事件明细展示增强等。

---

## 一、仓库级清理与发布准备

### 1. 删除的内容

| 类别 | 数量 | 说明 |
| --- | --- | --- |
| Go 测试文件(`*_test.go`) | 130 | 覆盖 executor、translator、watcher、auth、usage 等全部包 |
| 前端测试文件(`*.test.ts`) | 5 | providerTest、credentialStats、routingRules 等 |
| `.github/` CI 配置 | 8 + 1 | docker 构建、release、PR 守卫、issue 模板、FUNDING 等(另含 `web/management-center/.github` 的 release workflow) |
| `docs/` 文档 | 9 | SDK access/advanced/usage/watcher 中英文档、管理面板设计文档 |
| `examples/` 示例 | 3 | custom-provider、http-request、translator |
| `test/` 集成测试 | 4 | amp 管理、thinking 转换、usage logging 及 sentinel fixture |
| 其他 | 2 | `AGENTS.md`(agent 说明文件) |

**用意**:目标仓库是精简的私有分发仓库,只保留运行与构建所需的源码、配置模板与管理端前端;测试、CI、上游文档均不随仓库发布。`.gitignore` 同步新增 `**test.go`,保证后续新写的测试也不会被提交。

### 2. `.gitignore`

新增忽略:`config1.yaml`、`config.yaml2`(本地多份运行配置)、`**test.go`(测试文件)、`.superpowers/`(本地 Agent 工具状态)、`a.txt`、`oauth.txt`(本地草稿/调试残留)。

**用意**:防止真实运行配置、认证调试残留和本地工具状态进入仓库。

### 3. `.env.example`

新增 Google OAuth 客户端环境变量样例:

- `CPA_GEMINI_OAUTH_CLIENT_ID` / `CPA_GEMINI_OAUTH_CLIENT_SECRET`
- `CPA_ANTIGRAVITY_OAUTH_CLIENT_ID` / `CPA_ANTIGRAVITY_OAUTH_CLIENT_SECRET`

### 4. 新增 `REQUIREMENTS_CHANGES_CN.md`

记录本轮需求汇总、上传范围、验证结果与注意事项,作为需求层面的说明文档(本文档则偏重代码层面的修改记录)。

---

## 二、OAuth 内置凭据外部化

**文件**:`internal/auth/gemini/gemini_auth.go`、`internal/auth/antigravity/constants.go`、`internal/auth/antigravity/auth.go`、`internal/api/handlers/management/api_tools.go`、`internal/runtime/executor/gemini_cli_executor.go`、`internal/runtime/executor/antigravity_executor.go`

**修改**:删除源码中硬编码的 Google OAuth `ClientID`/`ClientSecret` 常量,改为 `OAuthClientID()` / `OAuthClientCredentials()` 从环境变量读取(`CPA_GEMINI_OAUTH_*`、`CPA_ANTIGRAVITY_OAUTH_*`);缺少环境变量时返回明确错误(如 `gemini oauth client credentials missing; set ...`)。管理端 OAuth 工具、auth file token 刷新流程、Gemini CLI executor、Antigravity executor 全部改走该路径。

**用意**:上传仓库不携带可用的 OAuth 密钥,避免密钥泄露和 GitHub push protection 拦截;同时保留完整登录/刷新能力,只需部署方自带凭据。

---

## 三、配置体系(internal/config、sdk/config)

### 1. 新增 `output-filter`(输出过滤)

`internal/config/config.go` 新增 `OutputFilterConfig` / `OutputFilterRule`:

- 全局规则:`enabled`、`max-length`、`keywords`(正则,忽略大小写,非法正则跳过)。
- `providers` 下可按 provider 覆盖(如仅对 claude 启用)。
- 提供 `GlobalRule()`、`HasActiveRules()` 辅助方法。

**用意**:短响应中命中关键字(如 "permission denied"、"overloaded"、占位或拒绝文本)时,将其判定为上游失败而非把无效内容透传给下游,进而触发重试/failover。

### 2. 新增 `error-control`(错误控制/轮次重试)

新增 `ErrorControlConfig`(default + providers 两级)与 `ErrorControlPolicy`:

- `retry-rounds`:完整候选集合的重试轮次。
- `round-backoff-base` / `round-backoff-exponent` / `round-backoff-max`:轮次间指数退避,公式 `wait = min(base * exponent^(round-1), max)`。
- 同时**移除旧的 `max-retry-credentials` 字段**及其所有关联逻辑(watcher diff、管理 UI 表单)。
- `SanitizeErrorControl()` 把非法值(rounds < 1、backoff ≤ 0)规范化到安全默认值。

**用意**:用"按轮次重试整个候选集合 + 指数退避"替代旧的"最多试 N 个凭据"模型,重试行为可全局、按 provider、按凭据三级配置。

### 3. 新增 `provider-cooldown`(供应商失败冷却,未提交层新功能)

新增 `ProviderCooldownConfig`:`start`(起始冷却计数)、`exponent`(增长指数)、`max`(上限),含 `SanitizeProviderCooldown()` 规范化。全局、Gemini/Claude/Codex/OpenAI-compat/Vertex-compat 各 key 均可配置 `cooldown` 覆盖。

**用意**:对连续失败的凭据施加"按失败次数指数增长的跳过计数"(而非按时间),失败越多被跳过的调度轮次越多,成功后重置,降低对故障上游的无效打击。

### 4. 路由策略重构 + 并行请求

`RoutingConfig`:

- `strategy` 取值由 `round-robin`/`fill-first` 改为 **`random`(默认)/ `last-success`**。
- 新增 `parallel-requests-enabled`、`parallel-requests-min-round`、`parallel-requests-min-failures`:允许同优先级中满足条件(达到最小轮次、最小连续失败数)的凭据被并行分发。

**用意**:`last-success` 粘住最近成功的凭据;并行请求在多次失败后同时向多个候选发起探测,谁先成功用谁,缩短故障期的恢复时间。

### 5. Provider key 级新字段

- Claude key 新增 `auth-mode`:`auto`(默认兼容行为)/ `api-key`(强制 `x-api-key`)/ `bearer`(强制 `Authorization: Bearer`,用于 OAuth token)。
- Codex key 新增 `use-v1`(`*bool`):控制 API key base URL 是否追加 `/v1`,未设置时保持默认追加。
- 各类 key(Gemini/Claude/Codex/OpenAI-compat/Vertex-compat)统一新增 `error-control`、`cooldown` 覆盖字段。
- 全局新增 `codex-remove-empty-input-name` 布尔开关。

### 6. `config.example.yaml` 同步更新

新增 output-filter、error-control 示例段;删除 `max-retry-credentials`;routing 段改为 `random`/`last-success` 并加入 parallel-requests 三项;Claude key 示例加 `auth-mode` 注释。

### 7. `sdk/config/config.go`

导出 `OutputFilterRule`、`OutputFilterConfig`、`ErrorControlConfig`、`ErrorControlPolicy` 类型别名,供 SDK 使用方引用。

---

## 四、认证调度核心(sdk/cliproxy/auth)

本次最大的单文件改动:`conductor.go`(基线以来 +约 2700 行,其中未提交层 +1067)。

### 1. 重试候选链(retry candidate chain)

新增 `retryCandidate` / `retryCandidateChain` 及 `retryCandidates()`、`buildRetryCandidateChain()`、`roundCandidates()`:

- 每轮把可用凭据(auth 文件凭据 `isAuthFileRetryCandidate` 与配置型 API key 凭据 `isConfigProviderRetryCandidate`)组织为候选链,`shuffleRetryCandidates` 随机化。
- 候选按 provider/auth 策略决定各自可重复尝试的次数;`maxRetryRounds()` 取相关 provider 的最大轮次。
- 轮次之间按 `computeRoundBackoff(round, base, exponent, max)` 指数退避,`roundBackoffPolicy()` 汇总生效策略;`effectiveErrorControlPolicy(provider, auth)` 实现"全局默认 → provider → 凭据 metadata 覆盖"的三级合并。
- 候选链带 provider pool 版本号(`bumpProviderPoolVersionLocked` 等),配置变化时 `retryCandidateChainStale()` 判定链已过期需重建。

**用意**:把旧的"逐个凭据顺序重试"升级为"整集合按轮次重试 + 策略化退避",且策略可分层覆盖。

### 2. 供应商冷却状态机

新增 `providerCooldownState` / `providerCooldownPolicy` 与 `recordProviderCooldownFailureLocked`、`resetProviderCooldownLocked`、`applyProviderCooldownToCandidates`、`decrementProviderCooldownLocked`、`providerCooldownRawAfterFailure` 等:

- 凭据失败时按 `raw = raw * exponent`(起点 `start`)累积冷却计数,取整为需跳过的调度次数,封顶 `max`;每轮调度对冷却中的候选做计数递减并跳过;成功即重置。
- `shouldBypassCooldownForSelection` 处理模型选择时的冷却豁免;`pickNextMixedCooldownFallback` 在全部候选冷却时挑选最近可用的回退凭据。

### 3. 并行探测分发

新增 `parallelRequestConfig`、`annotateParallelEligibility`、`executeResponseCandidateBatch`、`executeStreamCandidateBatch`、`assignRoundDispatchIndexes`:

- 满足 `min-round`/`min-failures` 条件的候选按批并行执行,首个成功结果胜出;每个候选带 `RoundDispatchIndex` 记录批内分发序号(会写入用量明细,便于观察并行行为)。
- 凭据失败连击由 `incrementAuthFailureStreakLocked` / `resetAuthFailureStreakLocked` / `authFailureStreakLocked` 维护。

### 4. 运行时代数(RuntimeGeneration)与执行重置

- `types.go` 的 `Auth` 新增 `RuntimeGeneration uint64`;conductor 新增 `executionResetSignal`、`errExecutionReset`、`currentExecutionResetSignal()`、`executionResetChanged()`、`broadcastExecutionReset()`。
- auth 配置变化时递增 generation,in-flight 请求在执行间隙检测到变化即中止并按新配置重建候选链(`waitForCooldown` 等待也会被 reset 信号打断)。

**用意**:防止旧配置下发起的请求继续污染新配置的运行时状态(如把失败记到已被替换的凭据上)。

### 5. 运行时状态保留/重置

新增 `authConfigFingerprint`(配置指纹,含 metadata/attributes 规范化)、`shouldResetAuthRuntimeOnUpdate`、`preserveRuntimeStateOnSourceRefresh`、`preserveRuntimeMetadataOnSourceRefresh`、`resetAuthRuntimeState`、`cloneModelStates`、`authMarkedRemoved`:

- 普通的 source 刷新(文件重读等)尽量保留 quota、last error、model states、token metadata;
- 配置发生实质变化(指纹不同)才重置运行时状态;
- 删除 auth 时标记 `runtime_removed` 并清理 access/refresh token、credit balance 等 runtime metadata(`clearRuntimeErrorsForProviders`、management 端 `clearAuthRuntimeMetadata` 配合)。
- `persist_policy.go` 新增 `WithResetRuntimeState(ctx)` 上下文标记与 `isConfigDerivedAuth()`(识别 `source=config:*` 且 `auth_kind=apikey` 的配置派生凭据)。

### 6. 流式引导(stream bootstrap)校验

新增 `readStreamBootstrap`、`streamBootstrapPayloadAllowsStart`、`streamBootstrapSSEDataPayloads`、`streamBootstrapJSONPayloadHasText`、`shouldWaitForStreamBootstrapText`、`wrapStreamResult`:

- 在把流交给下游之前先读取并缓冲首批 chunk,确认出现真实语义输出(文本/工具调用/usage)后才"开播";空流或引导阶段错误直接作为该候选的失败,转入重试而不是给下游发一个空的 `[DONE]`。

### 7. 统一失败出口与错误清洗

- `newUpstreamExhaustedError()`:所有候选与轮次耗尽后统一返回 `upstream_exhausted`,`isRetryableUpstreamError()` 判定可重试错误。
- 配置型 provider 失败时 `applyConfigProviderModelFailureState` / `applyConfigProviderAuthFailureState` / `genericConfigProviderFailureMessage` 生成通用失败信息写入运行时状态。

**用意**:下游错误体不携带上游原始报错(可能含密钥、内部 URL 等敏感信息),排查依赖服务端日志与管理端运行时状态。

### 8. scheduler / selector / types 其他改动

- `scheduler.go`:移除 Codex websocket 凭据的跨优先级偏好(`highestReadyPriorityLocked` 不再接受 `preferWebsocket`),websocket 不再打破优先级顺序;空 meta 的默认优先级从 `group:10` 改为 `entry:0`。
- `types.go`:新增 `DisabledFromMetadata()` / `SyncDisabledMetadata()`(禁用状态与持久化 metadata 双向同步);新增 `RetryRoundsOverride`、`RoundBackoff{Base,Exponent,Max}Override`、`ProviderCooldown{Start,Exponent,Max}Override` 等凭据级 metadata 覆盖读取;新增 `parseFloatAny` 等解析辅助。
- `SetErrorControlConfig()` / `SetRetryConfig()`:由 `sdk/cliproxy/service.go`、`builder.go` 在启动与热重载时注入配置。

---

## 五、输出过滤与响应校验(executor 层)

### 1. 新增 `internal/downstreamtext/extract.go`

`Extract(format, output)` 从 OpenAI Chat / OpenAI Responses / Claude / Gemini 各响应格式中提取"助手可见文本"(`textCollector` 按格式遍历 content/parts/delta)。

**用意**:输出过滤只应作用于用户实际看到的文本,而非整个 JSON 载荷。

### 2. 新增 `internal/runtime/executor/helps/response_validation.go`

- `ResponseFormatError`:表示"上游返回 2xx 但载荷不可安全转发"的错误,对下游统一暴露 502,同时保留 `UsageStatusCode`(用量记录仍记原始状态码)、`ErrorReason`、`ErrorMessage`。
- `ValidateDownstreamNonStreamPayload(WithOutputFilter)`:非流式载荷校验(JSON 合法性、空响应)+ 输出过滤。
- `ValidateDownstreamStreamChunk(WithOutputFilterForProvider)`:流式 chunk 校验 + 过滤;provider 专属规则优先,同时保留全局规则(`outputFilterRuleForProvider`)。
- 预置错误构造:`newEmptyResponseError`(`upstream_empty_response`)、`newMalformedJSONError`、`newFilteredOutputError`(命中关键字)。

### 3. 各 executor 接入

Claude、Codex、Codex WebSocket、Gemini、Gemini CLI、Gemini Vertex、AI Studio、Antigravity、Kimi、OpenAI-compatible 全部接入输出过滤与空响应检测(多数 executor 是数行接线;重点改动见下)。

### 4. Claude executor(`claude_executor.go`)

- 新增 `claudeStreamSemanticState`:逐行观察 SSE,只有出现文本、thinking、tool 调用或 usage output tokens(`claudeStreamHasSemanticOutput` 等)后才认为响应有效;空流或只有 usage 无输出时返回 `upstream_empty_response`。
- EOF 处理:`shouldIgnoreClaudeStreamEOF`、`isUnexpectedEOF`、`normalizeClaudeReadError` 区分"已产出有效输出后的意外断流"与"从未产出输出的空流"。
- `auth-mode` 支持:`shouldUseClaudeAPIKeyHeader` / `normalizeClaudeAuthMode` 按配置强制 `x-api-key` 或 `Authorization: Bearer`。
- 规范 base URL 拼接,避免重复斜杠。
- 用量上报改为"先 `SetDetail`,响应确认有效后再 publish"。

### 5. Codex executor(`codex_executor.go`,未提交层 +417 行)

- `use-v1`:`codexUseV1(auth)` + `buildCodexResponsesURL()` 按开关构造 `/responses`、`/responses/compact` URL。
- `codex-remove-empty-input-name`:`normalizeCodexRequestBody()` 移除请求 `input[].name` 为空字符串的字段;`function_call` 且 name 为空的 input 项整个丢弃,避免上游校验失败。
- SSE 错误识别:`codexStreamErrorFromSSE`、`firstCodexStreamErrorText`、`codexStreamErrorStatusCode` 从流事件中提取错误文本与状态码;`codexEmptyDataEventError` 把空 data 事件判为错误。
- **Responses SSE 元数据修复**(未提交层):`codexResponseMetadataState` + `patchCodexResponseMetadata()` 对 `response.created` / `response.in_progress` / `response.completed` 事件补齐缺失的 `response` 对象、`response.id`(必要时生成 `resp_<uuid>`)、`object`、`created_at`、`model` 字段,并记录 `codex_upstream_response_metadata_repaired`。用意:某些兼容上游返回的 Responses SSE 元数据不完整,会导致下游客户端解析崩溃,由代理补齐。
- 流式与非流式均做空响应校验。

### 6. Antigravity executor

非流式转换后执行输出过滤;流式响应无任何行时返回空响应错误;terminal chunk 与 usage 发布顺序更明确;OAuth 凭据改环境变量(见第二节)。

---

## 六、用量统计与日志(internal/usage、sdk/cliproxy/usage)

### 1. `internal/usage/logger_plugin.go`(+约 800 行)

- `RequestDetail` 大幅扩展:`latency_ms`、`source`、`upstream_model`、`user_agent`、`input_chars`、`status_code`、`error_reason`、`error_message`、`request_count`、`retry_round`(第几轮重试)、`round_dispatch_index`(并行批内序号)、`parallel_eligible`、`provider_cooldown_remaining`、`provider_cooldown_generated_raw`、`failed`,以及 `TokenStats` 细分 `reasoning_tokens`、`cached_tokens`、`cache_creation_input_tokens`、`cache_read_input_tokens`。
- 新增聚合:`RequestOutcomeStats`(成功/失败计数)按 source 与 auth index 维度统计;`Usage20mSnapshot`(provider → bucket → identity → model 的 20 分钟桶 `UsageBucketStats`,含 token 细分与延迟 total/samples)。
- 存储保护:明细默认保留 72 小时;每模型明细上限 100、全局明细上限 100(`trimAndAppendDetail`、`trimTotalDetailsLocked`);模型数、user agent、source、auth index 数量及字符串长度均有上限,超限合并到 `(other)`(`boundedUsageModelKey`、`boundedUsageStatsKey`、`capUsageStoredString`)。

**用意**:让管理端能看到每次请求的完整链路信息(哪个凭据、第几轮、是否并行、冷却状态、错误原因),同时用硬上限防止统计内存无界增长。

### 2. 新增 `internal/usage/usage_queue.go`

环形队列(上限 4096 条)记录原始 usage JSON(`queuedUsageRecord`),`PopUsageQueue(count)` 按数量弹出最旧记录。配套管理 API `GetUsageQueue`。

**用意**:为外部采集器提供增量拉取 usage record 的接口,不依赖管理端统计视图。

### 3. `sdk/cliproxy/usage/manager.go`

`RequestMetadata` / `Record` 扩展与 logger 对应的全部新字段(RetryRound、RoundDispatchIndex、ParallelEligible、ProviderCooldown*、StatusCode、ErrorReason/Message、UpstreamModel、cache 细分 token);新增 `EnsureRequestContext(ctx)` 保证元数据随请求上下文传递;队列裁剪 `trimQueueForAppendLocked`。

### 4. `helps/usage_helpers.go`(UsageReporter 重构)

- 由"一次性发布"改为可分步:`SetDetail` → `SetUpstreamModel(FromPayload)` → `SetStatusCode` → `PublishCurrent` / `PublishFailureWithError`,便于 executor 在确认响应有效后再发布。
- `ExtractUpstreamModel(data)` 按 `upstreamModelJSONPaths` 从响应载荷提取上游真实模型名(处理模型别名/映射后的差异)。
- 失败元数据:`failureMetadataFromError` 从错误(含 `ResponseFormatError`)提取状态码、reason、message;`SetUpstreamStatusCode` / `UpstreamStatusCodeFromContext` 通过 context 透传上游状态码;`shouldTreatFailureAsSuccess` 处理边界(如已成功产出但尾部报错)。

---

## 七、管理 API(internal/api)

### 1. 新增配置端点(`internal/api/server.go` 注册)

- `GET/PUT/PATCH /v0/management/error-control`
- `GET/PUT/PATCH /v0/management/provider-cooldown`
- `GET/PUT/PATCH /v0/management/codex-remove-empty-input-name`

`config_basic.go` / `config_lists.go` 实现相应 handler,并带 `normalizeErrorControlPolicy`、`normalizeProviderCooldownConfig`、`normalizeClaudeAuthMode` 规范化;provider key 的 PATCH 支持 `error-control`、`cooldown`、Claude `auth-mode`、Codex `use-v1` 字段。`GET /management/config` 返回 provider key 列表时补充 `auth-index`,便于前端把配置项、用量与运行时状态关联。

### 2. Auth files API(`auth_files.go`,+242 行)

返回体补充运行时状态:`last_error`(`buildRuntimeErrorEntry`)、`quota`(`buildQuotaStateEntry`)、`model_states`(`buildModelStatesEntry`,含每模型冷却/错误)、推导的 `status_message`(`authRuntimeStatusMessage`、`retryAfterStatusMessage` 等)。

### 3. Usage API(`usage.go`)

- `GET /management/usage-statistics?include_details=true&details_limit=N` 返回受限明细(默认不带明细,防止载荷过大);export 时包含完整保留明细。
- 新增 usage queue 弹出接口(`GetUsageQueue` + `parseUsageQueueCount`)。

---

## 八、下游协议处理器(sdk/api/handlers)

- `openai/openai_responses_handlers.go`(+318):新增 `responsesSSEHasRealOutput` / `responsesSSEDataHasRealOutput` 判定首批 SSE 是否含真实输出;`responsesSSEStartErrorFromChunk`、`writeResponseStartError`、`isResponseStreamStartError` 把"流开始即错误/空流"转成结构化 HTTP 错误返回,而不是发送空流后静默 `[DONE]`。
- `claude/code_handlers.go`(+87/106):抽出 `forwardClaudeStream` 统一转发逻辑,首包为空/错误时返回错误。
- `handlers.go`:`BuildErrorResponseBody` 对 `upstream_exhausted` 使用固定、安全的错误 code/message(错误体清洗)。
- Gemini、OpenAI chat、images handler 做同方向的小幅适配。

**用意**:配合 conductor 的 stream bootstrap,保证"下游要么收到有效流,要么收到明确错误",不再出现假成功的空回复。

---

## 九、存储与热重载(internal/store、internal/watcher)

- **禁用状态持久化**:`gitstore.go`、`objectstore.go`、`postgresstore.go`、`sdk/auth/filestore.go` 保存时调用 `SyncDisabledMetadata(auth)` 把禁用状态写入 metadata;读取时用 `DisabledFromMetadata(metadata)` 还原 `Disabled` 与 `StatusDisabled`。用意:操作员在管理端禁用的凭据在重启/多实例间保持禁用。
- **watcher**(`config_reload.go`、`events.go`、`watcher.go`):`auth-dir` 变化时通过 `switchAuthDir()` 切换 fs watcher(此前只在启动时绑定);重试配置变化检测由 `max-retry-credentials` 改为深比较 `error-control`;auth dir 解析统一走 `resolveAuthDir`,并修正并发读写(`authDirWatched` 加锁)。
- **config diff**(`diff/config_diff.go`):变化明细记录 `error-control changed`、`claude[i].auth-mode` 变化;删除 `max-retry-credentials` 项。
- **synthesizer**(`synthesizer/config.go`):`buildBackoffMetadata` 扩展签名,把 error-control(`retry_rounds`、`round_backoff_*`)与 cooldown(`cooldown_start/exponent/max`)写入运行时 auth metadata(供 conductor 的凭据级覆盖读取);Claude `auth_mode`、Codex `use_v1` 写入 attributes;从 auth file metadata 恢复 disabled 状态。

---

## 十、管理端前端(web/management-center)

### 1. AI Providers 页面重构

- **编辑页改为弹窗**:`MainRoutes.tsx` 删除 `/ai-providers/*/new`、`/:index` 等顶层路由,新增 `AiProviderEditModal.tsx` 在弹窗内用 `useRoutes` 承载原编辑页(`utils/aiProviderEditModal.ts` 提供打开/关闭/导航事件),主列表不再整页跳转。
- **快速导入**:新增 `AiProviderQuickImportPanel.tsx` + `quickImport.ts`,从粘贴文本中用正则识别 `base_url`/`api_key`/`Authorization: Bearer` 等字段,快速追加 Codex/Claude provider 配置(含优先级、前缀校验与快捷键)。
- **测试请求生成**:新增 `providerTestRequest.ts`,为 Codex/Claude 生成贴近真实客户端的测试请求(如 Codex 使用 `codex-tui/...` User-Agent、按 `use-v1`/`auth-mode` 构造 URL 与 header)。
- **冷却配置组件**(未提交层):新增 `ProviderCooldownFields.tsx`(+ scss)与 `utils/providerCooldown.ts`(默认值 `start:1, exponent:1.2, max:10`,继承/自定义两种 scope),嵌入各 provider 编辑页与 Visual Config 全局区。
- 页面布局改为紧凑信息面板(`AiProvidersPage.tsx` +1017、scss +608):当前 provider 启用配置侧栏、长 key/模型/header 横向滚动、卡片统计(`useProviderStats`)。
- 各编辑页(Claude/Codex/Gemini/OpenAI/Vertex/Ampcode)接入 error-control、auth-mode、use-v1、cooldown、模型配置、禁用规则等新字段;draft store(`useClaudeEditDraftStore`、`useOpenAIEditDraftStore`)同步扩展。

### 2. Visual Config(`VisualConfigEditor.tsx` +677、`useVisualConfig.ts` +772)

- 新增 Output Filter 配置区,含本地测试框:`components/config/outputFilter.ts` 实现规则测试状态机(`disabled`/`too-long`/`matched`/`invalid-patterns` 等),可输入示例输出验证规则是否命中。
- 新增 Error Control 默认策略与 provider override 编辑、Provider Cooldown 全局配置、Codex remove empty input name 开关;移除 max retry credentials 表单。
- `services/api/config.ts`、`transformers.ts`、`types/visualConfig.ts` 增加对应请求与类型(含 `positive_integer`/`positive_number` 校验文案)。

### 3. Usage 页面

- `utils/usage.ts`(+1530,前端最大改动)+ 新增 `utils/usage/cost.ts`:适配后端新 usage schema——20 分钟桶(`Usage20mSnapshot` 汇总、`collectUsage20mSummaryEntries`)、token 细分桶(reasoning/cache 5m/1h write/read)、`ModelPrice` 扩展全部价格字段(`model_price_cache_write_5m/1h`、`cache_read` 等)、`resolveModelPrice` 模型名规范化匹配、成本计算 `calculateUsageBucketCost`/`calculateCost`、来源/凭据映射(`normalizeUsageSourceId`、`buildCandidateUsageSourceIds`、`normalizeAuthIndex`)、敏感值打码 `maskUsageSensitiveValue`。
- `ApiDetailsCard`(+230):API 详情改为可排序表格,展示输入/输出/reasoning/cache 读写/延迟/成功率/费用。
- `RequestEventsDetailsCard`(+482,未提交层继续增强)+ 新增 `requestEventsDisplay.ts`:请求事件明细支持错误展开、source tooltip、跳转编辑来源、状态码、成本显示,以及 `request_events_dispatch`(重试轮次/并行分发序号)与 `request_events_upstream_model` 展示。
- `PriceSettingsCard`(+183):费用设置支持全部价格字段,不再只有 prompt/completion/cache。
- `StatCards`、`TokenBreakdownChart`、`useSparklines`、`useUsageData`、`credentialStats`、`useUsageStatsStore`:同步适配新 schema 与 20m 快照。

### 4. Auth Files 页面

- `AuthFileCard` 显示 runtime quota/model states 状态;`AuthFileQuotaSection` 支持单文件刷新 quota;页面支持批量刷新全部 quota、批量启用/禁用(混合状态更新);`useAuthFilesData`/`useAuthFilesStats`、`pageControls` 相应扩展。
- `utils/sourceResolver.ts`(+277)、`types/sourceInfo.ts`:把 usage/auth index 反向映射回配置来源(哪个 provider 的第几个 key),支撑"从用量明细跳转到对应配置编辑"。

### 5. 其他

- i18n:en / zh-CN / zh-TW / ru 四语言各 +123 行,覆盖上述全部新功能(`output_filter`、`error_control`、`cooldown_*`、`parallel_requests_*`、`quick_import_*`、`claude_auth_mode_*`、`codex_use_v1_*`、`model_price_*`、`request_events_*`、`quota_refresh_single` 等)。
- `internal/managementasset/embed/management.html`:前端构建产物同步(与 `dist/index.html` 字节一致)。
- `MainLayout`、`SecondaryScreenShell`、`HeaderInputList`、`ModelInputList`、`DashboardPage`、`QuotaSection` 等小幅适配。

---

## 十一、其他零散改动

- `internal/registry/model_registry.go`:小幅精简(−9 行)。
- `internal/tui/dashboard.go`:适配 usage 结构变化的小改动。
- `internal/runtime/executor/helps/proxy_helpers.go`、`logging_helpers.go`、`utls_client.go`:配合上述重构的小调整。
- `sdk/cliproxy/service.go` / `builder.go`:启动与热重载时向 auth Manager 注入 error-control / retry 配置(`SetErrorControlConfig` 等),并接入 usage 队列与新统计。

---

## 附:新增文件清单

| 文件 | 作用 |
| --- | --- |
| `REQUIREMENTS_CHANGES_CN.md` | 需求与改动记录(需求视角) |
| `internal/downstreamtext/extract.go` | 从各响应格式提取下游可见文本 |
| `internal/runtime/executor/helps/response_validation.go` | 响应校验 + 输出过滤核心 |
| `internal/usage/usage_queue.go` | usage 原始记录队列(外部采集) |
| `web/.../config/outputFilter.ts` | 输出过滤规则本地测试逻辑 |
| `web/.../providers/AiProviderEditModal.tsx` | 弹窗承载 provider 编辑路由 |
| `web/.../providers/AiProviderQuickImportPanel.tsx` | 快速导入面板 |
| `web/.../providers/ProviderCooldownFields.tsx` (+ scss) | 冷却参数编辑组件 |
| `web/.../providers/providerTestRequest.ts` | 生成贴近真实客户端的测试请求 |
| `web/.../providers/quickImport.ts` | 粘贴文本解析(base_url/api_key) |
| `web/.../usage/requestEventsDisplay.ts` | 请求事件明细展示辅助 |
| `web/.../utils/aiProviderEditModal.ts` | 编辑弹窗事件总线 |
| `web/.../utils/providerCooldown.ts` | 冷却配置规范化与默认值 |
| `web/.../utils/usage/cost.ts` | 模型价格解析与成本计算 |

## 附:兼容性与行为变化提醒

- `max-retry-credentials` 配置字段已删除,旧配置中该字段将被忽略;重试行为改由 `error-control` 控制。
- `routing.strategy` 取值语义变化:`round-robin`/`fill-first` 不再支持,现为 `random`(默认)/`last-success`。
- Gemini / Antigravity OAuth 登录与刷新**必须**设置 `CPA_GEMINI_OAUTH_*` / `CPA_ANTIGRAVITY_OAUTH_*` 环境变量,否则报错。
- 输出过滤命中会把响应视为上游失败,可能触发 error-control 重试与 failover。
- `upstream_exhausted` 的下游错误体经过清洗;排查细节需看服务端日志与管理端运行时状态。
- 全部测试文件已从仓库移除且 `.gitignore` 新增 `**test.go`;`REQUIREMENTS_CHANGES_CN.md` 中记录的测试通过结论对应删除前的本地验证,当前仓库内已无法直接复跑这些测试。
