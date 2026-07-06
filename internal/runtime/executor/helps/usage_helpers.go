package helps

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	appusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type UsageReporter struct {
	provider      string
	model         string
	upstreamModel string
	authID        string
	authIndex     string
	apiKey        string
	source        string
	userAgent     string
	inputChars    int64
	requestedAt   time.Time
	once          sync.Once
	detailMu      sync.RWMutex
	detail        usage.Detail
	statusCode    int
}

const requestInputCharsContextKey = "request_input_chars"
const upstreamStatusCodeContextKey = "upstream_status_code"

func usageStatisticsEnabled() bool {
	return appusage.StatisticsEnabled()
}

func NewUsageReporter(ctx context.Context, provider, model string, auth *cliproxyauth.Auth) *UsageReporter {
	if !usageStatisticsEnabled() {
		return nil
	}
	apiKey := APIKeyFromContext(ctx)
	reporter := &UsageReporter{
		provider:    provider,
		model:       model,
		requestedAt: time.Now(),
		apiKey:      apiKey,
		source:      resolveUsageSource(auth, apiKey),
		userAgent:   RequestUserAgentFromContext(ctx),
		inputChars:  RequestInputCharsFromContext(ctx),
	}
	if auth != nil {
		reporter.authID = auth.ID
		reporter.authIndex = auth.EnsureIndex()
	}
	return reporter
}

func (r *UsageReporter) Publish(ctx context.Context, detail usage.Detail) {
	r.SetDetail(detail)
	r.publishWithOutcome(ctx, r.currentDetail(), false, nil)
}

func (r *UsageReporter) PublishFailure(ctx context.Context) {
	r.PublishFailureWithError(ctx, nil)
}

func (r *UsageReporter) PublishFailureWithError(ctx context.Context, failureErr error) {
	r.publishWithOutcome(ctx, r.currentDetail(), true, failureErr)
}

func (r *UsageReporter) PublishCurrent(ctx context.Context) {
	r.publishWithOutcome(ctx, r.currentDetail(), false, nil)
}

func (r *UsageReporter) TrackFailure(ctx context.Context, errPtr *error) {
	if r == nil || errPtr == nil {
		return
	}
	if *errPtr != nil {
		r.PublishFailureWithError(ctx, *errPtr)
	}
}

func (r *UsageReporter) SetDetail(detail usage.Detail) {
	if r == nil || !usageStatisticsEnabled() {
		return
	}
	detail = normalizeUsageDetail(detail)
	r.detailMu.Lock()
	r.detail = detail
	if upstreamModel := strings.TrimSpace(detail.UpstreamModel); upstreamModel != "" {
		r.upstreamModel = upstreamModel
	}
	r.detailMu.Unlock()
}

func (r *UsageReporter) SetUpstreamModel(model string) {
	if r == nil || !usageStatisticsEnabled() {
		return
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return
	}
	r.detailMu.Lock()
	r.upstreamModel = model
	r.detailMu.Unlock()
}

func (r *UsageReporter) SetUpstreamModelFromPayload(data []byte) {
	if r == nil || !usageStatisticsEnabled() {
		return
	}
	r.SetUpstreamModel(ExtractUpstreamModel(data))
}

func (r *UsageReporter) currentDetail() usage.Detail {
	if r == nil {
		return usage.Detail{}
	}
	r.detailMu.RLock()
	detail := r.detail
	r.detailMu.RUnlock()
	return normalizeUsageDetail(detail)
}

func (r *UsageReporter) currentUpstreamModel() string {
	if r == nil {
		return ""
	}
	r.detailMu.RLock()
	upstreamModel := r.upstreamModel
	r.detailMu.RUnlock()
	return strings.TrimSpace(upstreamModel)
}

func (r *UsageReporter) publishWithOutcome(ctx context.Context, detail usage.Detail, failed bool, failureErr error) {
	if r == nil || !usageStatisticsEnabled() {
		return
	}
	detail = normalizeUsageDetail(detail)
	r.once.Do(func() {
		usage.PublishRecord(ctx, r.buildRecordWithContext(ctx, detail, failed, failureErr))
	})
}

// ensurePublished guarantees that a usage record is emitted exactly once.
// It is safe to call multiple times; only the first call wins due to once.Do.
// This is used to ensure request counting even when upstream responses do not
// include any usage fields (tokens), especially for streaming paths.
func (r *UsageReporter) EnsurePublished(ctx context.Context) {
	if r == nil || !usageStatisticsEnabled() {
		return
	}
	r.once.Do(func() {
		usage.PublishRecord(ctx, r.buildRecordWithContext(ctx, r.currentDetail(), false, nil))
	})
}

func (r *UsageReporter) buildRecord(detail usage.Detail, failed bool, failureErr error) usage.Record {
	return r.buildRecordWithContext(context.Background(), detail, failed, failureErr)
}

func (r *UsageReporter) buildRecordWithContext(ctx context.Context, detail usage.Detail, failed bool, failureErr error) usage.Record {
	detail = normalizeUsageDetail(detail)
	statusCode, errorReason, errorMessage := failureMetadataFromError(failureErr)
	metadata := usage.MetadataFromContext(ctx)
	parallelAborted := failed && usage.IsParallelRequestAborted(ctx)
	if parallelAborted {
		errorReason = "parallel_request_aborted"
		errorMessage = usage.ErrParallelRequestAborted.Error()
	}
	if statusCode <= 0 {
		statusCode = r.currentStatusCode(ctx)
	}
	if !parallelAborted && shouldTreatFailureAsSuccess(failed, failureErr, statusCode) {
		failed = false
	}
	if !failed {
		errorReason = ""
		errorMessage = ""
	}
	providerCooldownGeneratedRaw := metadata.ProviderCooldownGeneratedRaw
	if !failed {
		providerCooldownGeneratedRaw = 0
	}
	if r == nil {
		return usage.Record{
			UpstreamModel:                strings.TrimSpace(detail.UpstreamModel),
			Detail:                       detail,
			Failed:                       failed,
			StatusCode:                   statusCode,
			ErrorReason:                  errorReason,
			ErrorMessage:                 errorMessage,
			RequestCount:                 metadata.RequestCount,
			RetryRound:                   metadata.RetryRound,
			RoundDispatchIndex:           metadata.RoundDispatchIndex,
			ParallelEligible:             metadata.ParallelEligible,
			ProviderCooldownRemaining:    metadata.ProviderCooldownRemaining,
			ProviderCooldownGeneratedRaw: providerCooldownGeneratedRaw,
		}
	}
	return usage.Record{
		Provider:                     r.provider,
		Model:                        r.model,
		UpstreamModel:                r.currentUpstreamModel(),
		Source:                       r.source,
		APIKey:                       r.apiKey,
		AuthID:                       r.authID,
		AuthIndex:                    r.authIndex,
		UserAgent:                    r.userAgent,
		InputChars:                   r.inputChars,
		RequestedAt:                  r.requestedAt,
		Latency:                      r.latency(),
		Failed:                       failed,
		StatusCode:                   statusCode,
		ErrorReason:                  errorReason,
		ErrorMessage:                 errorMessage,
		RequestCount:                 metadata.RequestCount,
		RetryRound:                   metadata.RetryRound,
		RoundDispatchIndex:           metadata.RoundDispatchIndex,
		ParallelEligible:             metadata.ParallelEligible,
		ProviderCooldownRemaining:    metadata.ProviderCooldownRemaining,
		ProviderCooldownGeneratedRaw: providerCooldownGeneratedRaw,
		Detail:                       detail,
	}
}

func (r *UsageReporter) SetStatusCode(statusCode int) {
	if r == nil || statusCode <= 0 {
		return
	}
	r.detailMu.Lock()
	r.statusCode = statusCode
	r.detailMu.Unlock()
}

func (r *UsageReporter) currentStatusCode(ctx context.Context) int {
	if r != nil {
		r.detailMu.RLock()
		statusCode := r.statusCode
		r.detailMu.RUnlock()
		if statusCode > 0 {
			return statusCode
		}
	}
	return UpstreamStatusCodeFromContext(ctx)
}

func normalizeUsageDetail(detail usage.Detail) usage.Detail {
	detail.UpstreamModel = strings.TrimSpace(detail.UpstreamModel)
	if detail.TotalTokens == 0 {
		total := detail.InputTokens + detail.OutputTokens + detail.CacheCreationInputTokens + detail.CacheReadInputTokens
		if total > 0 {
			detail.TotalTokens = total
		}
	}
	if detail.CachedTokens == 0 && detail.CacheReadInputTokens > 0 {
		detail.CachedTokens = detail.CacheReadInputTokens
	}
	return detail
}

func shouldTreatFailureAsSuccess(failed bool, err error, statusCode int) bool {
	return failed && isSuccessfulStatusCode(statusCode) && errors.Is(err, context.Canceled)
}

func isSuccessfulStatusCode(statusCode int) bool {
	return statusCode >= http.StatusOK && statusCode < http.StatusBadRequest
}

func failureMetadataFromError(err error) (int, string, string) {
	if err == nil {
		return 0, "", ""
	}

	type statusCoder interface {
		StatusCode() int
	}
	type usageStatusCoder interface {
		UsageStatusCode() int
	}
	type errorReasonProvider interface {
		ErrorReason() string
	}
	type errorMessageProvider interface {
		ErrorMessage() string
	}

	statusCode := 0
	var usc usageStatusCoder
	if errors.As(err, &usc) && usc != nil {
		statusCode = usc.UsageStatusCode()
	}
	if statusCode <= 0 {
		var sc statusCoder
		if errors.As(err, &sc) && sc != nil {
			statusCode = sc.StatusCode()
		}
	}

	errorReason := ""
	var rp errorReasonProvider
	if errors.As(err, &rp) && rp != nil {
		errorReason = strings.TrimSpace(rp.ErrorReason())
	}

	errorMessage := ""
	var mp errorMessageProvider
	if errors.As(err, &mp) && mp != nil {
		errorMessage = strings.TrimSpace(mp.ErrorMessage())
	}
	if errorMessage == "" {
		errorMessage = strings.TrimSpace(err.Error())
	}

	return statusCode, errorReason, errorMessage
}

func SetUpstreamStatusCode(ctx context.Context, statusCode int) {
	if ctx == nil {
		return
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return
	}
	if statusCode <= 0 {
		ginCtx.Set(upstreamStatusCodeContextKey, 0)
		return
	}
	ginCtx.Set(upstreamStatusCodeContextKey, statusCode)
}

func UpstreamStatusCodeFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return 0
	}
	raw, exists := ginCtx.Get(upstreamStatusCodeContextKey)
	if !exists || raw == nil {
		return 0
	}
	switch value := raw.(type) {
	case int:
		return normalizeStatusCode(value)
	case int32:
		return normalizeStatusCode(int(value))
	case int64:
		if value > int64(maxInt()) {
			return 0
		}
		return normalizeStatusCode(int(value))
	case float64:
		if value > float64(maxInt()) {
			return 0
		}
		return normalizeStatusCode(int(value))
	case float32:
		if value > float32(maxInt()) {
			return 0
		}
		return normalizeStatusCode(int(value))
	default:
		return 0
	}
}

func normalizeStatusCode(statusCode int) int {
	if statusCode < 0 {
		return 0
	}
	return statusCode
}

func maxInt() int {
	return int(^uint(0) >> 1)
}

func (r *UsageReporter) latency() time.Duration {
	if r == nil || r.requestedAt.IsZero() {
		return 0
	}
	latency := time.Since(r.requestedAt)
	if latency < 0 {
		return 0
	}
	return latency
}

func APIKeyFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return ""
	}
	if v, exists := ginCtx.Get("apiKey"); exists {
		switch value := v.(type) {
		case string:
			return value
		case fmt.Stringer:
			return value.String()
		default:
			return fmt.Sprintf("%v", value)
		}
	}
	return ""
}

func RequestUserAgentFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return ""
	}
	return strings.TrimSpace(ginCtx.Request.UserAgent())
}

func RequestInputCharsFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return 0
	}
	raw, exists := ginCtx.Get(requestInputCharsContextKey)
	if !exists || raw == nil {
		return 0
	}
	switch value := raw.(type) {
	case int:
		if value < 0 {
			return 0
		}
		return int64(value)
	case int32:
		if value < 0 {
			return 0
		}
		return int64(value)
	case int64:
		if value < 0 {
			return 0
		}
		return value
	case float64:
		if value < 0 {
			return 0
		}
		return int64(value)
	case float32:
		if value < 0 {
			return 0
		}
		return int64(value)
	default:
		return 0
	}
}

func resolveUsageSource(auth *cliproxyauth.Auth, ctxAPIKey string) string {
	if auth != nil {
		if auth.Attributes != nil {
			if display := strings.TrimSpace(auth.Attributes["display_source"]); display != "" {
				if source := strings.TrimSpace(auth.Attributes["source"]); strings.HasPrefix(strings.ToLower(source), "config:") {
					return display
				}
			}
		}
		provider := strings.TrimSpace(auth.Provider)
		if strings.EqualFold(provider, "gemini-cli") {
			if id := strings.TrimSpace(auth.ID); id != "" {
				return id
			}
		}
		if strings.EqualFold(provider, "vertex") {
			if auth.Metadata != nil {
				if projectID, ok := auth.Metadata["project_id"].(string); ok {
					if trimmed := strings.TrimSpace(projectID); trimmed != "" {
						return trimmed
					}
				}
				if project, ok := auth.Metadata["project"].(string); ok {
					if trimmed := strings.TrimSpace(project); trimmed != "" {
						return trimmed
					}
				}
			}
		}
		if _, value := auth.AccountInfo(); value != "" {
			return strings.TrimSpace(value)
		}
		if auth.Metadata != nil {
			if email, ok := auth.Metadata["email"].(string); ok {
				if trimmed := strings.TrimSpace(email); trimmed != "" {
					return trimmed
				}
			}
		}
		if auth.Attributes != nil {
			if key := strings.TrimSpace(auth.Attributes["api_key"]); key != "" {
				return key
			}
		}
	}
	if trimmed := strings.TrimSpace(ctxAPIKey); trimmed != "" {
		return trimmed
	}
	return ""
}

var upstreamModelJSONPaths = [...]string{
	"response.model",
	"response.modelVersion",
	"response.model_version",
	"message.model",
	"model",
	"modelVersion",
	"model_version",
}

func ExtractUpstreamModel(data []byte) string {
	payload := jsonPayload(data)
	if len(payload) == 0 {
		payload = bytes.TrimSpace(data)
	}
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return ""
	}
	root := gjson.ParseBytes(payload)
	for _, path := range upstreamModelJSONPaths {
		value := strings.TrimSpace(root.Get(path).String())
		if value != "" {
			return value
		}
	}
	return ""
}

func ParseCodexUsage(data []byte) (usage.Detail, bool) {
	if !usageStatisticsEnabled() {
		return usage.Detail{}, false
	}
	usageNode := gjson.ParseBytes(data).Get("response.usage")
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	detail := usage.Detail{
		UpstreamModel: ExtractUpstreamModel(data),
		InputTokens:   usageNode.Get("input_tokens").Int(),
		OutputTokens:  usageNode.Get("output_tokens").Int(),
		TotalTokens:   usageNode.Get("total_tokens").Int(),
	}
	if cached := usageNode.Get("input_tokens_details.cached_tokens"); cached.Exists() {
		detail.CachedTokens = cached.Int()
	}
	if reasoning := usageNode.Get("output_tokens_details.reasoning_tokens"); reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	return detail, true
}

func ParseOpenAIUsage(data []byte) usage.Detail {
	if !usageStatisticsEnabled() {
		return usage.Detail{}
	}
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	inputNode := usageNode.Get("prompt_tokens")
	if !inputNode.Exists() {
		inputNode = usageNode.Get("input_tokens")
	}
	outputNode := usageNode.Get("completion_tokens")
	if !outputNode.Exists() {
		outputNode = usageNode.Get("output_tokens")
	}
	detail := usage.Detail{
		UpstreamModel: ExtractUpstreamModel(data),
		InputTokens:   inputNode.Int(),
		OutputTokens:  outputNode.Int(),
		TotalTokens:   usageNode.Get("total_tokens").Int(),
	}
	cached := usageNode.Get("prompt_tokens_details.cached_tokens")
	if !cached.Exists() {
		cached = usageNode.Get("input_tokens_details.cached_tokens")
	}
	if cached.Exists() {
		detail.CachedTokens = cached.Int()
	}
	reasoning := usageNode.Get("completion_tokens_details.reasoning_tokens")
	if !reasoning.Exists() {
		reasoning = usageNode.Get("output_tokens_details.reasoning_tokens")
	}
	if reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	return detail
}

func ParseOpenAIStreamUsage(line []byte) (usage.Detail, bool) {
	if !usageStatisticsEnabled() {
		return usage.Detail{}, false
	}
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	inputNode := usageNode.Get("prompt_tokens")
	if !inputNode.Exists() {
		inputNode = usageNode.Get("input_tokens")
	}
	outputNode := usageNode.Get("completion_tokens")
	if !outputNode.Exists() {
		outputNode = usageNode.Get("output_tokens")
	}
	detail := usage.Detail{
		UpstreamModel: ExtractUpstreamModel(payload),
		InputTokens:   inputNode.Int(),
		OutputTokens:  outputNode.Int(),
		TotalTokens:   usageNode.Get("total_tokens").Int(),
	}
	cached := usageNode.Get("prompt_tokens_details.cached_tokens")
	if !cached.Exists() {
		cached = usageNode.Get("input_tokens_details.cached_tokens")
	}
	if cached.Exists() {
		detail.CachedTokens = cached.Int()
	}
	reasoning := usageNode.Get("completion_tokens_details.reasoning_tokens")
	if !reasoning.Exists() {
		reasoning = usageNode.Get("output_tokens_details.reasoning_tokens")
	}
	if reasoning.Exists() {
		detail.ReasoningTokens = reasoning.Int()
	}
	return detail, true
}

func ParseClaudeUsage(data []byte) usage.Detail {
	if !usageStatisticsEnabled() {
		return usage.Detail{}
	}
	usageNode := gjson.ParseBytes(data).Get("usage")
	if !usageNode.Exists() {
		return usage.Detail{}
	}
	detail := usage.Detail{
		UpstreamModel:              ExtractUpstreamModel(data),
		InputTokens:                usageNode.Get("input_tokens").Int(),
		OutputTokens:               usageNode.Get("output_tokens").Int(),
		CacheCreationInputTokens:   usageNode.Get("cache_creation_input_tokens").Int(),
		CacheCreation5mInputTokens: usageNode.Get("cache_creation.ephemeral_5m_input_tokens").Int(),
		CacheCreation1hInputTokens: usageNode.Get("cache_creation.ephemeral_1h_input_tokens").Int(),
		CacheReadInputTokens:       usageNode.Get("cache_read_input_tokens").Int(),
	}
	detail.CachedTokens = detail.CacheReadInputTokens
	if detail.CacheCreationInputTokens == 0 {
		detail.CacheCreationInputTokens = detail.CacheCreation5mInputTokens + detail.CacheCreation1hInputTokens
	}
	detail.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.CacheCreationInputTokens + detail.CacheReadInputTokens
	return detail
}

func ParseClaudeStreamUsage(line []byte) (usage.Detail, bool) {
	if !usageStatisticsEnabled() {
		return usage.Detail{}, false
	}
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	usageNode := gjson.GetBytes(payload, "usage")
	if !usageNode.Exists() {
		return usage.Detail{}, false
	}
	detail := usage.Detail{
		UpstreamModel:              ExtractUpstreamModel(payload),
		InputTokens:                usageNode.Get("input_tokens").Int(),
		OutputTokens:               usageNode.Get("output_tokens").Int(),
		CacheCreationInputTokens:   usageNode.Get("cache_creation_input_tokens").Int(),
		CacheCreation5mInputTokens: usageNode.Get("cache_creation.ephemeral_5m_input_tokens").Int(),
		CacheCreation1hInputTokens: usageNode.Get("cache_creation.ephemeral_1h_input_tokens").Int(),
		CacheReadInputTokens:       usageNode.Get("cache_read_input_tokens").Int(),
	}
	detail.CachedTokens = detail.CacheReadInputTokens
	if detail.CacheCreationInputTokens == 0 {
		detail.CacheCreationInputTokens = detail.CacheCreation5mInputTokens + detail.CacheCreation1hInputTokens
	}
	detail.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.CacheCreationInputTokens + detail.CacheReadInputTokens
	return detail, true
}

func parseGeminiFamilyUsageDetail(node gjson.Result) usage.Detail {
	detail := usage.Detail{
		InputTokens:     node.Get("promptTokenCount").Int(),
		OutputTokens:    node.Get("candidatesTokenCount").Int(),
		ReasoningTokens: node.Get("thoughtsTokenCount").Int(),
		TotalTokens:     node.Get("totalTokenCount").Int(),
		CachedTokens:    node.Get("cachedContentTokenCount").Int(),
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.ReasoningTokens
	}
	return detail
}

func ParseGeminiCLIUsage(data []byte) usage.Detail {
	if !usageStatisticsEnabled() {
		return usage.Detail{}
	}
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("response.usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("response.usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	detail := parseGeminiFamilyUsageDetail(node)
	detail.UpstreamModel = ExtractUpstreamModel(data)
	return detail
}

func ParseGeminiUsage(data []byte) usage.Detail {
	if !usageStatisticsEnabled() {
		return usage.Detail{}
	}
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	detail := parseGeminiFamilyUsageDetail(node)
	detail.UpstreamModel = ExtractUpstreamModel(data)
	return detail
}

func ParseGeminiStreamUsage(line []byte) (usage.Detail, bool) {
	if !usageStatisticsEnabled() {
		return usage.Detail{}, false
	}
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	detail := parseGeminiFamilyUsageDetail(node)
	detail.UpstreamModel = ExtractUpstreamModel(payload)
	return detail, true
}

func ParseGeminiCLIStreamUsage(line []byte) (usage.Detail, bool) {
	if !usageStatisticsEnabled() {
		return usage.Detail{}, false
	}
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "response.usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	detail := parseGeminiFamilyUsageDetail(node)
	detail.UpstreamModel = ExtractUpstreamModel(payload)
	return detail, true
}

func ParseAntigravityUsage(data []byte) usage.Detail {
	if !usageStatisticsEnabled() {
		return usage.Detail{}
	}
	usageNode := gjson.ParseBytes(data)
	node := usageNode.Get("response.usageMetadata")
	if !node.Exists() {
		node = usageNode.Get("usageMetadata")
	}
	if !node.Exists() {
		node = usageNode.Get("usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}
	}
	detail := parseGeminiFamilyUsageDetail(node)
	detail.UpstreamModel = ExtractUpstreamModel(data)
	return detail
}

func ParseAntigravityStreamUsage(line []byte) (usage.Detail, bool) {
	if !usageStatisticsEnabled() {
		return usage.Detail{}, false
	}
	payload := jsonPayload(line)
	if len(payload) == 0 || !gjson.ValidBytes(payload) {
		return usage.Detail{}, false
	}
	node := gjson.GetBytes(payload, "response.usageMetadata")
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usageMetadata")
	}
	if !node.Exists() {
		node = gjson.GetBytes(payload, "usage_metadata")
	}
	if !node.Exists() {
		return usage.Detail{}, false
	}
	detail := parseGeminiFamilyUsageDetail(node)
	detail.UpstreamModel = ExtractUpstreamModel(payload)
	return detail, true
}

var stopChunkWithoutUsage sync.Map

func rememberStopWithoutUsage(traceID string) {
	stopChunkWithoutUsage.Store(traceID, struct{}{})
	time.AfterFunc(10*time.Minute, func() { stopChunkWithoutUsage.Delete(traceID) })
}

// FilterSSEUsageMetadata removes usageMetadata from SSE events that are not
// terminal (finishReason != "stop"). Stop chunks are left untouched. This
// function is shared between aistudio and antigravity executors.
func FilterSSEUsageMetadata(payload []byte) []byte {
	if len(payload) == 0 {
		return payload
	}

	lines := bytes.Split(payload, []byte("\n"))
	modified := false
	foundData := false
	for idx, line := range lines {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || !bytes.HasPrefix(trimmed, []byte("data:")) {
			continue
		}
		foundData = true
		dataIdx := bytes.Index(line, []byte("data:"))
		if dataIdx < 0 {
			continue
		}
		rawJSON := bytes.TrimSpace(line[dataIdx+5:])
		traceID := gjson.GetBytes(rawJSON, "traceId").String()
		if isStopChunkWithoutUsage(rawJSON) && traceID != "" {
			rememberStopWithoutUsage(traceID)
			continue
		}
		if traceID != "" {
			if _, ok := stopChunkWithoutUsage.Load(traceID); ok && hasUsageMetadata(rawJSON) {
				stopChunkWithoutUsage.Delete(traceID)
				continue
			}
		}

		cleaned, changed := StripUsageMetadataFromJSON(rawJSON)
		if !changed {
			continue
		}
		var rebuilt []byte
		rebuilt = append(rebuilt, line[:dataIdx]...)
		rebuilt = append(rebuilt, []byte("data:")...)
		if len(cleaned) > 0 {
			rebuilt = append(rebuilt, ' ')
			rebuilt = append(rebuilt, cleaned...)
		}
		lines[idx] = rebuilt
		modified = true
	}
	if !modified {
		if !foundData {
			// Handle payloads that are raw JSON without SSE data: prefix.
			trimmed := bytes.TrimSpace(payload)
			cleaned, changed := StripUsageMetadataFromJSON(trimmed)
			if !changed {
				return payload
			}
			return cleaned
		}
		return payload
	}
	return bytes.Join(lines, []byte("\n"))
}

// StripUsageMetadataFromJSON drops usageMetadata unless finishReason is present (terminal).
// It handles both formats:
// - Aistudio: candidates.0.finishReason
// - Antigravity: response.candidates.0.finishReason
func StripUsageMetadataFromJSON(rawJSON []byte) ([]byte, bool) {
	jsonBytes := bytes.TrimSpace(rawJSON)
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return rawJSON, false
	}

	// Check for finishReason in both aistudio and antigravity formats
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	terminalReason := finishReason.Exists() && strings.TrimSpace(finishReason.String()) != ""

	usageMetadata := gjson.GetBytes(jsonBytes, "usageMetadata")
	if !usageMetadata.Exists() {
		usageMetadata = gjson.GetBytes(jsonBytes, "response.usageMetadata")
	}

	// Terminal chunk: keep as-is.
	if terminalReason {
		return rawJSON, false
	}

	// Nothing to strip
	if !usageMetadata.Exists() {
		return rawJSON, false
	}

	// Remove usageMetadata from both possible locations
	cleaned := jsonBytes
	var changed bool

	if usageMetadata = gjson.GetBytes(cleaned, "usageMetadata"); usageMetadata.Exists() {
		// Rename usageMetadata to cpaUsageMetadata in the message_start event of Claude
		cleaned, _ = sjson.SetRawBytes(cleaned, "cpaUsageMetadata", []byte(usageMetadata.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "usageMetadata")
		changed = true
	}

	if usageMetadata = gjson.GetBytes(cleaned, "response.usageMetadata"); usageMetadata.Exists() {
		// Rename usageMetadata to cpaUsageMetadata in the message_start event of Claude
		cleaned, _ = sjson.SetRawBytes(cleaned, "response.cpaUsageMetadata", []byte(usageMetadata.Raw))
		cleaned, _ = sjson.DeleteBytes(cleaned, "response.usageMetadata")
		changed = true
	}

	return cleaned, changed
}

func hasUsageMetadata(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	if gjson.GetBytes(jsonBytes, "usageMetadata").Exists() {
		return true
	}
	if gjson.GetBytes(jsonBytes, "response.usageMetadata").Exists() {
		return true
	}
	return false
}

func isStopChunkWithoutUsage(jsonBytes []byte) bool {
	if len(jsonBytes) == 0 || !gjson.ValidBytes(jsonBytes) {
		return false
	}
	finishReason := gjson.GetBytes(jsonBytes, "candidates.0.finishReason")
	if !finishReason.Exists() {
		finishReason = gjson.GetBytes(jsonBytes, "response.candidates.0.finishReason")
	}
	trimmed := strings.TrimSpace(finishReason.String())
	if !finishReason.Exists() || trimmed == "" {
		return false
	}
	return !hasUsageMetadata(jsonBytes)
}

func JSONPayload(line []byte) []byte {
	return jsonPayload(line)
}

func jsonPayload(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("event:")) {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	return trimmed
}
