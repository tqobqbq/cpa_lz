# CPA 需求与改动记录

本文档记录当前工作区已实现并准备上传的需求、代码改动、验证范围和排除上传的本地文件。目标仓库为 `git@github.com:tqobqbq/cpa_lz.git`。

## 上传范围

上传范围包含运行和构建项目所需的源代码、配置模板、管理端前端代码、嵌入式管理页面、脚本和本记录文档。

不会上传本地临时文件、个人工具状态、真实运行配置、认证材料、编译产物和调试残留，例如：

- `.superpowers/`
- `a.txt`
- `config.yaml`
- `config1.yaml`
- `config.yaml2`
- `.env`
- `auths/*` 中的认证材料
- `cli-proxy-api`
- `test-output`
- 日志、临时目录和本地 IDE/Agent 元数据

## 需求汇总

1. 增强上游错误重试控制，替换旧的 `max-retry-credentials` 思路，支持全局、按 provider、按凭据的 retry policy。
2. 新增输出过滤能力，对短响应中的指定关键字或正则命中进行拦截，避免把无效、拒绝、占位或异常文本继续透传给下游。
3. 改善流式响应质量控制，空流、无语义输出、上游空响应要返回明确错误，而不是伪装成正常 `[DONE]`。
4. 完善用量统计，保留更丰富的请求明细、缓存 token 维度、延迟、来源、状态码、错误信息和 20 分钟聚合桶。
5. 提供用量队列接口，方便外部采集器增量拉取 usage record。
6. 强化认证文件和运行时状态展示，在管理端可看到 last error、quota、model states、禁用状态和刷新状态。
7. 优化管理端 AI Providers 页面，支持更紧凑的卡片、启用项侧栏、快速导入和 provider 测试请求生成。
8. 优化管理端 Visual Config，支持配置输出过滤、错误控制、Codex 空 input name 移除开关等新配置。
9. 增强 Codex、Claude、Gemini、OpenAI-compatible、Antigravity、Kimi 等 executor 的响应校验、用量上报和配置兼容。
10. 改善配置热重载和 auth 目录监听，在 `auth-dir` 变化时切换 watcher 并保留必要运行时状态。

## 后端配置改动

- 新增 `output-filter` 配置：
  - 支持全局规则。
  - 支持 `providers` 下按 provider 覆盖。
  - 规则包含 `enabled`、`max-length`、`keywords`。
  - 匹配忽略大小写，关键字按正则处理，非法正则会跳过。
- 新增 `error-control` 配置：
  - `provider-retries` 控制同一 auth/provider 的本地重试次数。
  - `retry-rounds` 控制完整候选集合的重试轮次。
  - `round-backoff-base`、`round-backoff-exponent`、`round-backoff-max` 控制轮次间指数退避。
  - 支持默认策略和 provider 策略。
  - Gemini、Claude、Codex、OpenAI-compatible、Vertex compatible 等凭据可配置 override。
- 移除 `max-retry-credentials` 配置字段及相关 UI 表单。
- 新增 `codex-remove-empty-input-name`：
  - 开启后移除 Codex 请求里空字符串 `input[].name`。
  - 对 `function_call` 且 name 为空的 input item 会直接丢弃，避免上游校验失败。
- Claude key 新增 `auth-mode`：
  - `auto` 保持兼容行为。
  - `api-key` 强制使用 `x-api-key`。
  - `bearer` 强制使用 `Authorization: Bearer`。
- Codex key 新增 `use-v1`：
  - 控制 API key base URL 是否追加 `/v1`。
  - 未设置时保持 API-key entries 默认追加 `/v1`。
- SDK 配置导出新增 `OutputFilterRule`、`OutputFilterConfig`、`ErrorControlConfig`、`ErrorControlPolicy` 类型别名。
- 移除源码内置 Google OAuth client 凭据：
  - Gemini OAuth 改为读取 `CPA_GEMINI_OAUTH_CLIENT_ID` 和 `CPA_GEMINI_OAUTH_CLIENT_SECRET`。
  - Antigravity OAuth 改为读取 `CPA_ANTIGRAVITY_OAUTH_CLIENT_ID` 和 `CPA_ANTIGRAVITY_OAUTH_CLIENT_SECRET`。
  - 管理端 OAuth 工具、auth file token flow、Gemini CLI executor 和 Antigravity executor 均改为使用环境变量派生的凭据。
  - 缺少必要环境变量时返回明确错误，避免上传仓库携带可用密钥或被 GitHub push protection 拦截。

## 请求调度与认证运行时

- `sdk/cliproxy/auth` 的调度逻辑改为基于 retry candidate 和 retry round：
  - 每个候选可按 provider/auth 策略重复尝试。
  - 候选集合支持轮次重试和指数退避。
  - 最终失败统一返回 `upstream_exhausted`，下游错误体会被清洗，避免泄露上游原始敏感错误。
- 增加 `RuntimeGeneration`：
  - auth 配置变化时递增 generation。
  - 请求执行过程中检测 generation 变化，触发执行重置。
  - 避免旧配置请求继续污染新配置状态。
- 保留和重置运行时状态：
  - 普通 source refresh 尽量保留 quota、last error、model state、token metadata。
  - 配置发生实质变化时重置 runtime state。
  - 删除 auth 时标记 `runtime_removed`，并清理 access token、refresh token、credit balance 等 runtime metadata。
- 禁用状态同步：
  - file/git/object/postgres store 保存时把 disabled 状态同步回 metadata。
  - 读取 auth file 时根据 metadata 还原 `Disabled` 和 `StatusDisabled`。
- scheduler、selector、persist policy 增补了异常、禁用、优先级和重试相关处理。

## Executor 与响应校验

- 新增 `internal/downstreamtext`，用于从 OpenAI Responses、Claude、Gemini 等格式中提取下游可见文本。
- 新增 `internal/runtime/executor/helps/response_validation.go`：
  - 对非流式响应执行 output filter。
  - 对 SSE/流式 chunk 执行 output filter。
  - Provider 专属规则优先匹配，同时保留全局规则。
- 各 executor 接入输出过滤和空响应检测：
  - Claude
  - Codex
  - Codex WebSocket
  - Gemini
  - Gemini CLI
  - Gemini Vertex
  - AI Studio
  - Antigravity
  - Kimi
  - OpenAI-compatible
- Claude executor：
  - 支持 `auth-mode` 控制 header 行为。
  - 规范 base URL 拼接，避免重复斜杠。
  - 流式响应引入语义状态检测，只有看到文本、thinking、tool 或 usage output tokens 后才认为响应有效。
  - 空流或只有 usage 但没有输出时返回 `upstream_empty_response`。
  - 用量上报改为先设置 detail，再在响应确认有效后发布。
- Codex executor：
  - 支持 `use-v1` 和 `codex-remove-empty-input-name`。
  - 流式和非流式均校验空响应。
  - `/responses` 和 `/responses/compact` URL 根据 `use-v1` 构造。
- Antigravity executor：
  - 非流式转换后执行输出过滤。
  - 流式响应无任何行时返回空响应错误。
  - 保证 terminal chunk 与 usage 发布顺序更明确。
- API handler：
  - OpenAI、Completions、Gemini、Claude Code 流式首包为空时返回错误，不再静默发送 DONE。
  - `BuildErrorResponseBody` 对 `upstream_exhausted` 使用固定、安全的错误 code/message。

## 管理 API 改动

- `GET /management/config` 返回 provider key 列表时补充 `auth-index`，便于前端把配置项、用量和运行时状态关联。
- 新增配置接口：
  - `GET /management/error-control`
  - `PUT /management/error-control`
  - `PATCH /management/error-control`
  - `GET /management/codex-remove-empty-input-name`
  - `PUT /management/codex-remove-empty-input-name`
  - `PATCH /management/codex-remove-empty-input-name`
- Provider key patch 接口新增对 `error-control`、Claude `auth-mode`、Codex `use-v1` 的处理和规范化。
- Auth files API 返回更多运行时状态：
  - `last_error`
  - `quota`
  - `model_states`
  - 推导后的 `status_message`
- Usage API：
  - `GET /management/usage-statistics?include_details=true&details_limit=N` 可返回受限明细。
  - export usage 时包含完整保留明细。
  - 新增 usage queue pop 接口，用于外部采集。

## 用量统计改动

- 请求明细新增字段：
  - `remote_ip`
  - `status_code`
  - `error_reason`
  - `error_message`
  - 细分 cache creation/read token
- 聚合维度新增：
  - source success/failure
  - auth index success/failure
  - provider/bucket/identity/model 的 20 分钟快照
  - 模型级 latency total/sample
- 存储保护：
  - 明细默认保留 72 小时。
  - 每模型明细上限 100。
  - 全局明细上限 100。
  - API 下模型数量、user agent、source、auth index 和字符串长度均有上限。
  - 超限数据合并到 `(other)`。
- 新增 usage queue，记录原始 usage JSON，支持外部按数量弹出。
- 前端价格计算支持更多 token 维度，包括 cache write/read、5m/1h cache creation 等。

## 管理端前端改动

- Visual Config：
  - 增加 Output Filter 配置区。
  - 增加本地测试框，可输入示例输出验证规则是否命中。
  - 增加 Error Control 默认策略和 provider override 编辑。
  - 增加 Codex remove empty input name 开关。
  - 移除 max retry credentials 表单。
  - 新增校验、类型转换、保存和 i18n 文案。
- AI Providers：
  - 页面布局改成更紧凑的信息面板。
  - 增加当前 provider 的启用配置侧栏。
  - 增加快速导入面板，用于识别和追加 provider 配置。
  - Provider 测试增强，Codex/Claude 可生成更贴近实际接口的测试请求。
  - 编辑页面支持更多字段：error control、auth mode、use-v1、模型配置、禁用规则等。
  - 长 key、模型、header 和状态信息使用横向滚动，避免卡片撑开。
- Usage 页面：
  - API 详情改成可排序表格。
  - 展示输入、输出、reasoning、cache、cache write/read、延迟、成功率和费用。
  - 请求事件详情支持错误展开、source tooltip、跳转编辑 source、状态码和成本显示。
  - 费用设置支持所有价格字段，不再只支持 prompt/completion/cache。
  - Sparkline、summary、cost overview 和模型统计同步适配新 usage schema。
- Auth Files：
  - 卡片显示 runtime quota 状态。
  - 单个 auth file 可刷新 quota。
  - 页面可批量刷新全部 quota。
  - 批量启用/禁用支持混合状态更新。
  - source resolver 增强，可把 usage/auth index 映射回配置来源。
- i18n：
  - 更新 English、简体中文、繁体中文、俄语文案。

## 存储与热重载

- file/git/object/postgres store 在保存和读取 auth metadata 时保持 disabled 状态一致。
- Watcher：
  - `SetConfig` 和 reload 时解析并规范化 auth dir。
  - `auth-dir` 变化时切换 fs watcher。
  - retry 配置变化检测改为包含 `error-control`。
  - config diff 会记录 `error-control changed` 和 Claude `auth-mode` 变化。
- Synthesizer：
  - 将 error control 写入 runtime auth metadata。
  - 将 Claude `auth-mode` 写入 attributes。
  - 将 Codex `use_v1` 写入 attributes。
  - 从 auth file metadata 恢复 disabled 状态。

## 测试与验证结果

已新增或更新 Go/前端测试，覆盖以下方向：

- error control 规范化和重试轮次。
- output filter 规则、provider override、流式/非流式校验。
- Codex empty input name、use-v1、base URL、WebSocket。
- Claude auth mode、空流检测、headers 行为。
- usage queue、usage aggregation、cost、request event display。
- auth files runtime status、批量状态更新和 quota UI。
- config watcher、synthesizer、source resolver。

已执行验证：

- `gofmt -w .`：通过。
- `go test ./...`：通过，`1648 passed in 100 packages`。
- `go build -o test-output ./cmd/server`：通过，随后已删除 `test-output`。
- 敏感 OAuth 字符串扫描：通过，仓库中不再包含已知 Google OAuth client ID/secret 字符串。
- `npm run build`（`web/management-center`）：通过，包含 `tsc` 和 Vite production build，构建后 `dist/index.html` 与 `internal/managementasset/embed/management.html` 字节一致。

## 当前注意事项

- 真实运行配置仍应使用本地 `config.yaml`，不会提交。
- 认证材料仍保存在 `auths/` 或配置的 store 中，不会提交。
- 输出过滤命中后会把响应视为上游失败，可能触发 error control 的重试和 failover。
- `error-control` 数值小于 1 或 backoff 小于等于 0 时会被规范化到安全默认值。
- `upstream_exhausted` 下游错误体经过清洗，排查细节应看服务端日志和管理端 runtime 状态。
