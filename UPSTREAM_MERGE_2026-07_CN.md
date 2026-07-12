# 2026-07 上游合并记录(router-for-me v7)

本文档记录 2026-07 将上游最新代码合并进本 fork 的过程、决策与结果。基线关系见
`CHANGES_SINCE_BCEB568_CN.md`(fork 相对 bceb568 的功能层说明)。

## 合并对象

| 侧 | 仓库 | 提交 |
| --- | --- | --- |
| 后端上游 | router-for-me/CLIProxyAPI `dev` | `4a2a3b29`(模块路径 v6→v7) |
| 前端上游 | router-for-me/Cli-Proxy-API-Management-Center `main` | `4064b01a` |
| 本地基线 | 本仓库 | `9a546af9`(前端 vendor 基 `de97726`) |

合并提交:后端 `e7caeada`(双父),前端 `5204135b`(vendored,合并过程在临时仓库
完成三方合并后回同步)。

## 决策原则(经用户确认)

1. 合并目标为上游 `dev`;恢复上游测试、CI、docs。
2. **重叠功能上游优先**:同领域一律采用上游实现,本地机制仅在上游完全没有对应
   能力时保留。据此放弃:计数型冷却(provider-cooldown)、error-control 轮次重试、
   并行探测请求、default-test-models;采用上游的时间型持久化冷却、禁用持久化、
   usage token 细分。
3. **本地独有功能必须可用**:output-filter、claude auth-mode、codex use-v1、
   codex-remove-empty-input-name、Codex SSE 元数据修复、usage 统计与明细、
   auth-files-group、routing.rules、快捷导入、MANAGEMENT_STATIC_PATH 覆盖。
4. gemini-cli 跟随上游移除(改用官方插件)。

## 后端结果(e7caeada)

- 9 个执行器全部回植 output-filter 校验与空流检测(`upstream_empty_response`)。
- claude:`auth_mode` 属性(auto/api-key/bearer 决定 x-api-key vs Authorization)、
  流语义空流检测、EOF 容忍。
- codex:`use_v1` 属性(**缺省=false**,OAuth 官方端点;synthesizer 对 API-key 条目
  缺省显式写 true)、`buildCodexResponsesURL`、SSE 元数据修复
  (`codexResponseMetadataState`)、`codexStreamErrorFromSSE` 兜底错误提取、
  `codex-remove-empty-input-name`。
- conductor:`routing.rules`(按 User-Agent / 输入字符数选 provider),
  handlers 注入请求元数据(UA、utf8 字符数)。
- 管理端:`/v0/management/usage`、auth_files 运行时状态(last_error/quota/
  model_states)、`/v0/management/auth-files-group`、嵌入式管理页支持
  `MANAGEMENT_STATIC_PATH` 覆盖。
- go build / go vet / go test 全绿(61 个包)。

## 前端结果(5204135b)

采用上游 Workbench 重构为主体,本地功能重新植入:

**采纳上游**:Providers Workbench(Sheet 表单、连通性测试、模型发现)、插件商店
UI、分区可视化配置编辑器(字段搜索索引、simple/full 模式)、auth-files 状态过滤
(statusFilterMode 四态)、xai 配额、recentRequests 状态条、卡内配额刷新/重置、
bun 打包。

**本地保留/重植**:

| 功能 | 植入方式 |
| --- | --- |
| usage 统计页全套 | 整目录保留;config 解析补齐 `routing.rules` 回填;编辑来源跳转改为路由导航(编辑弹窗已随旧结构退役) |
| auth-files 全局组设置 | 面板移植到新 AuthFilesPage,走 `/v0/management/auth-files-group` |
| output-filter 可视化 | 新编辑器新增独立分区(07),含全局规则、按 provider 规则、正则试算;搜索索引与校验(positive_integer)接线 |
| codex-remove-empty-input-name | network 分区新增开关 + 搜索索引 |
| claude auth-mode / codex use-v1 | Workbench 表单按 descriptor 门控展示;use-v1 缺省勾选(=缺省 true 语义),取消勾选写 `use-v1: false`;模型发现请求尊重 auth-mode |
| 快捷导入 | 改造为 codex/claude 新建 Sheet 顶部"粘贴来源文本"框,自动提取 base_url/api key |
| auth-files 'modified' 排序 | 保留(uiState 与 pageControls) |

**随后端一并移除**:error-control、provider-cooldown、backoff、
default-test-models 的 UI、类型与 API 调用;usage 明细中的 retry_round/
dispatch/cooldown 字段解析保持可选(后端不再下发,展示自然为空)。

**验证**:`tsc --noEmit` 0 错误;`vite build` 单文件产物 2.8MB;
`dist/index.html` 与 `internal/managementasset/embed/management.html` 字节一致;
服务冒烟:`/management.html` 200,`/v0/management/{usage,config,
auth-files-group,routing/rules}` 契约正常。

## 注意事项

- 前端依赖改回含 `chart.js`/`react-chartjs-2`(usage 图表);上游 lockfile 为
  `bun.lock`,本地用 npm 构建亦可(package-lock.json 不入库)。
- `use-v1` 三态语义:配置缺省 = synthesizer 对 API-key 写 true;显式 false 才走
  官方端点。前端表单以"勾选=true(缺省)/取消=false"表达,不再写显式 true。
- usage 明细 schema 中 retry_round、round_dispatch_index、parallel_eligible、
  provider_cooldown_* 已随后端功能移除;前端类型保留为可选字段以兼容旧数据文件。

## 合并后回归修复(1983018b,2026-07-11)

合并后发现 usage 请求明细表三处回归,根因均为 v7 合并删除了 fork 独有的后端数据
链路(前端 UI 本身仍在):

| 回归 | 根因 | 修复 |
| --- | --- | --- |
| source 列悬停/点击编辑失效,且明细泄露裸 API key | synthesizer 不再给 config 合成 auth 写 `display_source` 属性 | 恢复 `applyConfigDisplayAttrs`;`resolveUsageSource` 优先 display_source;编辑跳转改 `/ai-providers?edit=<type>:<index>` 深链(Workbench Sheet),旧 `/ai-providers/:type/:index` 路由重定向兼容 |
| 上游真实模型列恒为 "-" | `Record.UpstreamModel` 与响应体提取链整体被删 | 恢复 `ExtractUpstreamModel` + `SetUpstreamModelFromPayload`(first-wins),接入全部执行器 usage 解析点、logger 与 redisqueue |
| dispatch 列消失 | 调度元数据随 error-control 引擎移除 | 以 context 方式重建 `RequestMetadata`(request_count / retry_round / round_dispatch_index),conductor 三层打点;parallel_eligible 与 provider_cooldown_* 维持移除(上游优先决策) |

注意:上一节"usage 明细 schema 中 retry_round、round_dispatch_index …已随后端
功能移除"自本次修复起不再成立 —— 这三个字段(加 request_count、auth_index、
upstream_model)已恢复下发;仅 parallel_eligible、provider_cooldown_* 仍为历史
字段。重试**行为**不变,仍是上游 v7 语义(request-retry × max-retry-credentials
× max-retry-interval + 时间型冷却),恢复的只是可观测性字段。

验证:go build/vet/test 61 包全绿;tsc 0 错误;dist 与 embed 字节一致;端到端
冒烟(假 SSE 上游)确认明细含 `source: codex#0`、`auth_index`、
`upstream_model`、`request_count`,source_stats 键不再是裸 key。

## 旧重试引擎整体回迁(2026-07-12)

用户决定推翻 1983018b 时"重叠功能上游优先"的重试决策,恢复旧 fork(9a546af9)
的完整 error-control 引擎,同时不丢失 v7 合并带来的路由能力。三点要求:

1. **上游轮询/重试遵照旧实现** —— 恢复轮次引擎:`retry-rounds` 全候选轮次、
   轮间指数退避(`round-backoff-base/exponent/max`,轮内耗时抵扣)、计数型
   `provider-cooldown`(start/exponent/max,成功清零)、并行探测调度
   (`routing.parallel-requests-*`,同优先级并发、首成功取消其余,输者记
   `parallel_request_aborted`)、执行重置信号(配置指纹变化 → RuntimeGeneration++
   → 外层循环重启)、providerPoolVersions 链失效重建。分层策略:
   default ← provider ← auth metadata(backoff_mode/retry_rounds/round_backoff_*/
   cooldown_*,synthesizer 从各 provider 配置块写入)。home 模式保留上游 v7
   单遍循环(旧树无 home)。
2. **下游错误为可重试错误** —— 轮次耗尽后统一 `upstream_exhausted:` 前缀错误
   (Retryable=true,保留最后状态码);`BuildErrorResponseBody`(OpenAI 格式)与
   claude handler 的 `claudeErrorDetailFromText` 均强制清洗为固定安全文案
   "Upstream providers exhausted after retries",不透传上游噪声;
   auth_not_found/auth_unavailable/provider_not_found/executor_not_found 原样透传。
3. **Unexpected EOF 修复** —— 合并时引入的 `AbortTerminalError` +
   `abortHTTPStream`(http.Hijacker 直接断 TCP)是 Claude Code
   "JSON Parse error: Unexpected EOF" 的元凶;恢复旧 `forwardClaudeStream`:
   终端错误一律写合法 SSE `event: error\ndata: {...}`,空流兜底 502
   "upstream returned an empty response";stream_forwarder.go 撤销 Abort 钩子,
   与旧树逐字节一致(仅 v6→v7 import)。

### 与 v7 路由特性的调和(关键差异,非纯回迁)

旧引擎的候选链完全绕过 selector 与 scheduler,而 v7 在其上层叠了若干路由特性。
本次移植做了如下调和(行为均有测试锁定):

| v7 特性 | 调和方式 |
| --- | --- |
| openai-compat 别名池跨上游降级(model-support 400/422 → 换池内模型) | 候选内模型池循环判据从旧 `!isRetryableUpstreamError→break` 改为 v7 `isRequestInvalidError→立即终止请求`,其余错误继续走池;model-support 错误被 isRequestInvalidError 排除,故仍可降级 |
| 池内上游模型 12h 挂起(后续请求跳过) | MarkResult 对 config-managed 凭据新增前置分支:`isModelSupportResultError` → 时间挂起(12h,`model_not_supported`);其余失败仍走"无时间冷却、仅计数冷却"的旧语义 |
| 自定义 Selector(websocket 会话固定、插件调度) | `applyCustomSelectorOrder`:非内置 selector 时,让 selector.Pick 决定链头(仅头部,尾部保持策略序,保证轮转/冷却仍遍历全池);内置 RoundRobin/FillFirst 不走此路径 |
| `max-retry-credentials` | buildRetryCandidateChain 内截断每轮候选数(0=全池) |
| `routing.strategy` | 三值:round-robin(默认,同优先级洗牌)、fill-first(稳定序,auth.ID 升序)、last-success(旧 fork 语义,failureStreak 升序);管理 API 放行 last-success |
| 请求域 404(store=false)、Cloudflare 挑战、422 | isRequestInvalidError 保留 HEAD 增强版(INVALID_ARGUMENT/FAILED_PRECONDITION/UNKNOWN 等) |
| antigravity credits 兜底 | 轮次耗尽后、exhausted 包装前;home 循环同样补齐 |

### 主要落点

- 新文件 `sdk/cliproxy/auth/error_control.go`(~1500 行):引擎全量(信号/策略/
  退避/计数冷却/候选链/并行 batch/执行器/指纹重置/exhausted 包装)。
- conductor.go:Execute/ExecuteCount/ExecuteStream 重写为轮次循环 + 独立
  executeHomeLoop 系列;MarkResult 加 AuthGeneration 守卫、streak/计数冷却打点、
  config-managed 分流;Register/Update/Remove 维护 RuntimeGeneration 与池版本。
- config.go/vertex_compat.go:ErrorControl/ProviderCooldown/Routing.Parallel*
  配置与清洗;5 类 key 增 BackoffMode/RequestRetry/ErrorControl/Cooldown 覆写;
  synthesizer 写入对应 metadata。
- 管理 API:`/v0/management/error-control`、`/provider-cooldown` GET/PUT/PATCH,
  热更新即时生效(SetErrorControlConfig / 配置 reload 触发比较)。
- usage:RequestMetadata 增 parallel_eligible / provider_cooldown_remaining /
  provider_cooldown_generated_raw;logger_plugin 下发三字段并清洗 NaN/负值;
  前端 RequestEventsDetailsCard 恢复完整 dispatch 列(6 值)与 CSV/JSON 导出,
  dist 与 embed 字节同步。
- 测试调和:上游 4 个依赖"注册序=派发序"的测试固定 fill-first;
  DisableCooling429 重试计数测试改由 retry-rounds 表达(request-retry 不再驱动
  非 home 重试);删除依赖 TCP 断流的 2 个 claude handler 测试,新增 3 个
  SSE error 事件断言(含 transport 错误可解析、空流 502、有 chunk 不追加)。

### 验证

- `go build ./...`、`gofmt` 全绿;`go test ./...` 全部通过(auth 包连跑 6 次无
  flake);vet 仅存 3 条与本次无关的历史告警(与并行 batch 的 cancel 模式为旧树
  原样,升级 go 后出现)。
- 端到端冒烟(:19999 假 SSE 上游 + :18318 服务):
  - key-500 → key-good 轮内切换成功,明细 `retry_round=1, dispatch=1→2`;
  - all500:3 轮 × 2 凭据 + 计数冷却跳过 = 预期请求数,轮间退避生效,下游
    Claude 体 `{"type":"error","error":{"type":"api_error","message":"Upstream providers exhausted after retries"}}`、
    OpenAI 体 code=`upstream_exhausted`,均 HTTP 500;
  - cut(SSE 中途断 TCP):下游收到合法 `event: error` 事件而非静默 EOF;
  - parallel-requests-enabled 热更新后明细 `parallel_eligible=true`;
  - PUT error-control 热更新 rounds/backoff 即时生效并持久化;
  - provider_cooldown_generated_raw 按 1→1.2→1.44… 递增,成功后归零。
