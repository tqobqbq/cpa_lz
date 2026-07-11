// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

var statisticsEnabled atomic.Bool

const (
	requestDetailsRetention    = 72 * time.Hour
	requestDetailsLimit        = 100
	maxUsageStoredDetailsTotal = 100
	maxUsageModelsPerAPI       = 256
	maxUsageUserAgents         = 256
	maxUsageSources            = 1024
	maxUsageAuthIndexes        = 1024
	maxUsageStoredStringBytes  = 1024
	usageOverflowKey           = "(other)"
)

func init() {
	statisticsEnabled.Store(true)
	coreusage.RegisterPlugin(NewLoggerPlugin())
}

// LoggerPlugin collects in-memory request statistics for usage analysis.
// It implements coreusage.Plugin to receive usage records emitted by the runtime.
type LoggerPlugin struct {
	stats *RequestStatistics
}

// NewLoggerPlugin constructs a new logger plugin instance.
//
// Returns:
//   - *LoggerPlugin: A new logger plugin instance wired to the shared statistics store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage implements coreusage.Plugin.
// It updates the in-memory statistics store whenever a usage record is received.
//
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to aggregate
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.stats == nil {
		return
	}
	p.stats.Record(ctx, record)
}

// SetStatisticsEnabled toggles whether in-memory statistics are recorded.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports the current recording state.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64
	detailCount   int

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64
	userAgents     map[string]struct{}
	sourceStats    map[string]RequestOutcomeStats
	authIndexStats map[string]RequestOutcomeStats
	usage20m       Usage20mSnapshot
}

type RequestOutcomeStats struct {
	Success int64 `json:"success"`
	Failure int64 `json:"failure"`
}

// apiStats holds aggregated metrics for a single API key.
type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	SuccessCount  int64
	FailureCount  int64
	Models        map[string]*modelStats
}

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests  int64
	TotalTokens    int64
	SuccessCount   int64
	FailureCount   int64
	LatencyTotalMs int64
	LatencySamples int64
	Details        []RequestDetail
}

// RequestDetail stores the timestamp, latency, and token usage for a single request.
type RequestDetail struct {
	Timestamp          time.Time  `json:"timestamp"`
	LatencyMs          int64      `json:"latency_ms"`
	Source             string     `json:"source"`
	AuthIndex          string     `json:"auth_index,omitempty"`
	UpstreamModel      string     `json:"upstream_model,omitempty"`
	UserAgent          string     `json:"user_agent,omitempty"`
	InputChars         int64      `json:"input_chars,omitempty"`
	StatusCode         int        `json:"status_code,omitempty"`
	ErrorReason        string     `json:"error_reason,omitempty"`
	ErrorMessage       string     `json:"error_message,omitempty"`
	RequestCount       uint64     `json:"request_count,omitempty"`
	RetryRound         int        `json:"retry_round,omitempty"`
	RoundDispatchIndex int        `json:"round_dispatch_index,omitempty"`
	Tokens             TokenStats `json:"tokens"`
	Failed             bool       `json:"failed"`
}

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	InputTokens                int64 `json:"input_tokens"`
	OutputTokens               int64 `json:"output_tokens"`
	ReasoningTokens            int64 `json:"reasoning_tokens"`
	CachedTokens               int64 `json:"cached_tokens"`
	CacheCreationInputTokens   int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheCreation5mInputTokens int64 `json:"cache_creation_5m_input_tokens,omitempty"`
	CacheCreation1hInputTokens int64 `json:"cache_creation_1h_input_tokens,omitempty"`
	CacheReadInputTokens       int64 `json:"cache_read_input_tokens,omitempty"`
	TotalTokens                int64 `json:"total_tokens"`
}

// UsageBucketStats captures request and token totals for a provider/model time bucket.
type UsageBucketStats struct {
	TotalRequests              int64 `json:"total_requests"`
	SuccessCount               int64 `json:"success_count"`
	FailureCount               int64 `json:"failure_count"`
	InputTokens                int64 `json:"input_tokens"`
	OutputTokens               int64 `json:"output_tokens"`
	ReasoningTokens            int64 `json:"reasoning_tokens"`
	CachedTokens               int64 `json:"cached_tokens"`
	CacheCreationInputTokens   int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheCreation5mInputTokens int64 `json:"cache_creation_5m_input_tokens,omitempty"`
	CacheCreation1hInputTokens int64 `json:"cache_creation_1h_input_tokens,omitempty"`
	CacheReadInputTokens       int64 `json:"cache_read_input_tokens,omitempty"`
	TotalTokens                int64 `json:"total_tokens"`
	LatencyTotalMs             int64 `json:"latency_total_ms,omitempty"`
	LatencySamples             int64 `json:"latency_samples,omitempty"`
}

// Usage20mSnapshot stores sparse twenty-minute buckets as provider -> bucket -> identity -> model.
type Usage20mSnapshot map[string]map[string]map[string]map[string]UsageBucketStats

// StatisticsSnapshot represents an immutable view of the aggregated metrics.
type StatisticsSnapshot struct {
	TotalRequests  int64                          `json:"total_requests"`
	SuccessCount   int64                          `json:"success_count"`
	FailureCount   int64                          `json:"failure_count"`
	TotalTokens    int64                          `json:"total_tokens"`
	UserAgents     []string                       `json:"user_agents,omitempty"`
	SourceStats    map[string]RequestOutcomeStats `json:"source_stats,omitempty"`
	AuthIndexStats map[string]RequestOutcomeStats `json:"auth_index_stats,omitempty"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
	Usage20m       Usage20mSnapshot `json:"usage_20m,omitempty"`
}

// APISnapshot summarises metrics for a single API key.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	SuccessCount  int64                    `json:"success_count"`
	FailureCount  int64                    `json:"failure_count"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests  int64           `json:"total_requests"`
	TotalTokens    int64           `json:"total_tokens"`
	SuccessCount   int64           `json:"success_count"`
	FailureCount   int64           `json:"failure_count"`
	LatencyTotalMs int64           `json:"latency_total_ms,omitempty"`
	LatencySamples int64           `json:"latency_samples,omitempty"`
	Details        []RequestDetail `json:"details,omitempty"`
}

var defaultRequestStatistics = NewRequestStatistics()

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
		userAgents:     make(map[string]struct{}),
		sourceStats:    make(map[string]RequestOutcomeStats),
		authIndexStats: make(map[string]RequestOutcomeStats),
		usage20m:       make(Usage20mSnapshot),
	}
}

// Record ingests a new usage record and updates the aggregates.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	if !statisticsEnabled.Load() {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	detail := normaliseDetail(record.Detail)
	totalTokens := detail.TotalTokens
	statsKey := capUsageStoredString(record.APIKey)
	if statsKey == "" {
		statsKey = capUsageStoredString(resolveAPIIdentifier(ctx, record))
	}
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	success := !failed
	requestedModel := strings.TrimSpace(record.Alias)
	if requestedModel == "" {
		requestedModel = record.Model
	}
	modelName := normaliseUsageDimension(requestedModel, "unknown")
	upstreamModel := strings.TrimSpace(record.UpstreamModel)
	if upstreamModel == "" {
		// Fall back to the routed model, but only when it observably differs
		// from what the client asked for (alias resolution).
		if routed := strings.TrimSpace(record.Model); routed != requestedModel {
			upstreamModel = routed
		}
	}
	statusCode := 0
	errorReason := ""
	errorMessage := ""
	if failed {
		statusCode = normaliseStatusCode(record.Fail.StatusCode)
		errorMessage = capUsageStoredString(strings.TrimSpace(record.Fail.Body))
		if statusCode > 0 {
			errorReason = http.StatusText(statusCode)
		}
	} else {
		statusCode = resolveDownstreamStatus(ctx)
	}
	dayKey := timestamp.Format("2006-01-02")
	hourKey := timestamp.Hour()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++
	if success {
		s.successCount++
	} else {
		s.failureCount++
	}
	s.totalTokens += totalTokens

	stats, ok := s.apis[statsKey]
	if !ok {
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = stats
	} else if stats.Models == nil {
		stats.Models = make(map[string]*modelStats)
	}
	requestMetadata := coreusage.MetadataFromContext(ctx)
	s.updateAPIStats(stats, modelName, RequestDetail{
		Timestamp:          timestamp,
		LatencyMs:          normaliseLatency(record.Latency),
		Source:             record.Source,
		AuthIndex:          capUsageStoredString(record.AuthIndex),
		UpstreamModel:      upstreamModel,
		UserAgent:          resolveUserAgent(ctx),
		InputChars:         normaliseInputChars(resolveInputChars(ctx)),
		StatusCode:         statusCode,
		ErrorReason:        errorReason,
		ErrorMessage:       errorMessage,
		RequestCount:       requestMetadata.RequestCount,
		RetryRound:         requestMetadata.RetryRound,
		RoundDispatchIndex: requestMetadata.RoundDispatchIndex,
		Tokens:             detail,
		Failed:             failed,
	}, timestamp)
	if userAgent := capUsageStoredString(resolveUserAgent(ctx)); userAgent != "" {
		addBoundedUsageSetValue(s.userAgents, userAgent, maxUsageUserAgents)
	}
	if source := boundedUsageStatsKey(s.sourceStats, record.Source, maxUsageSources); source != "" {
		statsValue := s.sourceStats[source]
		if failed {
			statsValue.Failure++
		} else {
			statsValue.Success++
		}
		s.sourceStats[source] = statsValue
	}
	if authIndex := boundedUsageStatsKey(s.authIndexStats, record.AuthIndex, maxUsageAuthIndexes); authIndex != "" {
		statsValue := s.authIndexStats[authIndex]
		if failed {
			statsValue.Failure++
		} else {
			statsValue.Success++
		}
		s.authIndexStats[authIndex] = statsValue
	}

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
	s.updateUsage20m(record, modelName, detail, failed, timestamp)
}

func (s *RequestStatistics) updateUsage20m(record coreusage.Record, modelName string, detail TokenStats, failed bool, timestamp time.Time) {
	if s == nil {
		return
	}
	if s.usage20m == nil {
		s.usage20m = make(Usage20mSnapshot)
	}
	provider := normaliseUsageDimension(record.Provider, "unknown")
	bucket := timestamp.UTC().Truncate(20 * time.Minute).Format(time.RFC3339)
	identity := usageIdentityKey(record)
	model := normaliseUsageDimension(modelName, "unknown")

	providerBuckets := s.usage20m[provider]
	if providerBuckets == nil {
		providerBuckets = make(map[string]map[string]map[string]UsageBucketStats)
		s.usage20m[provider] = providerBuckets
	}
	identityBuckets := providerBuckets[bucket]
	if identityBuckets == nil {
		identityBuckets = make(map[string]map[string]UsageBucketStats)
		providerBuckets[bucket] = identityBuckets
	}
	modelBuckets := identityBuckets[identity]
	if modelBuckets == nil {
		modelBuckets = make(map[string]UsageBucketStats)
		identityBuckets[identity] = modelBuckets
	}

	stats := modelBuckets[model]
	stats.TotalRequests++
	if failed {
		stats.FailureCount++
	} else {
		stats.SuccessCount++
	}
	stats.InputTokens += detail.InputTokens
	stats.OutputTokens += detail.OutputTokens
	stats.ReasoningTokens += detail.ReasoningTokens
	stats.CachedTokens += detail.CachedTokens
	stats.CacheCreationInputTokens += detail.CacheCreationInputTokens
	stats.CacheCreation5mInputTokens += detail.CacheCreation5mInputTokens
	stats.CacheCreation1hInputTokens += detail.CacheCreation1hInputTokens
	stats.CacheReadInputTokens += detail.CacheReadInputTokens
	stats.TotalTokens += detail.TotalTokens
	if latencyMs := normaliseLatency(record.Latency); latencyMs > 0 {
		stats.LatencyTotalMs += latencyMs
		stats.LatencySamples++
	}
	modelBuckets[model] = stats
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail, now time.Time) {
	if stats == nil {
		return
	}
	if stats.Models == nil {
		stats.Models = make(map[string]*modelStats)
	}
	model = s.boundedUsageModelKey(stats, model)
	detail = normaliseRequestDetail(detail)
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	if detail.Failed {
		stats.FailureCount++
	} else {
		stats.SuccessCount++
	}
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens
	if detail.Failed {
		modelStatsValue.FailureCount++
	} else {
		modelStatsValue.SuccessCount++
	}
	if detail.LatencyMs > 0 {
		modelStatsValue.LatencyTotalMs += detail.LatencyMs
		modelStatsValue.LatencySamples++
	}
	s.trimAndAppendDetail(modelStatsValue, detail, now)
}

func (s *RequestStatistics) boundedUsageModelKey(stats *apiStats, model string) string {
	model = normaliseUsageDimension(model, "unknown")
	if stats == nil || stats.Models == nil || maxUsageModelsPerAPI <= 0 {
		return model
	}
	if _, ok := stats.Models[model]; ok {
		return model
	}
	if len(stats.Models) < maxUsageModelsPerAPI-1 {
		return model
	}
	if _, ok := stats.Models[usageOverflowKey]; !ok {
		for len(stats.Models) >= maxUsageModelsPerAPI-1 {
			if !s.mergeOneUsageModelIntoOverflow(stats) {
				break
			}
		}
	}
	return usageOverflowKey
}

func (s *RequestStatistics) mergeOneUsageModelIntoOverflow(stats *apiStats) bool {
	if stats == nil || stats.Models == nil {
		return false
	}
	for modelName, modelStatsValue := range stats.Models {
		if modelName == usageOverflowKey {
			continue
		}
		overflow := stats.Models[usageOverflowKey]
		if overflow == nil {
			overflow = &modelStats{}
			stats.Models[usageOverflowKey] = overflow
		}
		if modelStatsValue != nil {
			overflow.TotalRequests += modelStatsValue.TotalRequests
			overflow.TotalTokens += modelStatsValue.TotalTokens
			overflow.SuccessCount += modelStatsValue.SuccessCount
			overflow.FailureCount += modelStatsValue.FailureCount
			overflow.LatencyTotalMs += modelStatsValue.LatencyTotalMs
			overflow.LatencySamples += modelStatsValue.LatencySamples
			if s != nil {
				s.detailCount -= len(modelStatsValue.Details)
				if s.detailCount < 0 {
					s.detailCount = 0
				}
			}
		}
		delete(stats.Models, modelName)
		return true
	}
	return false
}

func normaliseRequestDetail(detail RequestDetail) RequestDetail {
	detail.Source = capUsageStoredString(detail.Source)
	detail.AuthIndex = capUsageStoredString(detail.AuthIndex)
	detail.UpstreamModel = capUsageStoredString(detail.UpstreamModel)
	detail.UserAgent = capUsageStoredString(detail.UserAgent)
	detail.ErrorReason = capUsageStoredString(detail.ErrorReason)
	detail.ErrorMessage = capUsageStoredString(detail.ErrorMessage)
	detail.InputChars = normaliseInputChars(detail.InputChars)
	detail.StatusCode = normaliseStatusCode(detail.StatusCode)
	if detail.LatencyMs < 0 {
		detail.LatencyMs = 0
	}
	detail.Tokens = normaliseTokenStats(detail.Tokens)
	return detail
}

func normaliseUsageDimension(value, fallback string) string {
	value = capUsageStoredString(value)
	if value == "" {
		return fallback
	}
	return value
}

func usageIdentityKey(record coreusage.Record) string {
	if value := capUsageStoredString(strings.TrimSpace(record.AuthIndex)); value != "" {
		return "auth_index:" + value
	}
	if value := capUsageStoredString(strings.TrimSpace(record.AuthID)); value != "" {
		return "auth_id:" + value
	}
	if value := capUsageStoredString(strings.TrimSpace(record.APIKey)); value != "" {
		return "api_key:" + stableUsageStringID(value)
	}
	if value := capUsageStoredString(strings.TrimSpace(record.Source)); value != "" {
		return "source:" + value
	}
	return "unknown"
}

func stableUsageStringID(value string) string {
	if value == "" {
		return ""
	}
	var sum uint64 = 1469598103934665603
	for _, r := range value {
		sum ^= uint64(r)
		sum *= 1099511628211
	}
	return fmt.Sprintf("%016x", sum)
}

func capUsageStoredString(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= maxUsageStoredStringBytes {
		return value
	}
	value = value[:maxUsageStoredStringBytes]
	for !utf8.ValidString(value) && len(value) > 0 {
		value = value[:len(value)-1]
	}
	return value
}

func boundedUsageStatsKey(values map[string]RequestOutcomeStats, raw string, limit int) string {
	key := capUsageStoredString(raw)
	if key == "" || limit <= 0 {
		return key
	}
	if _, ok := values[key]; ok {
		return key
	}
	if len(values) >= limit-1 {
		return usageOverflowKey
	}
	return key
}

func addBoundedUsageSetValue(values map[string]struct{}, raw string, limit int) {
	if values == nil {
		return
	}
	value := capUsageStoredString(raw)
	if value == "" {
		return
	}
	if _, ok := values[value]; ok {
		return
	}
	if limit > 0 && len(values) >= limit-1 {
		values[usageOverflowKey] = struct{}{}
		return
	}
	values[value] = struct{}{}
}

func (s *RequestStatistics) trimAndAppendDetail(modelStatsValue *modelStats, detail RequestDetail, now time.Time) {
	if modelStatsValue == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-requestDetailsRetention)
	details := modelStatsValue.Details
	dropUntil := 0
	for dropUntil < len(details) {
		ts := details[dropUntil].Timestamp
		if ts.IsZero() || !ts.Before(cutoff) {
			break
		}
		dropUntil++
	}
	if dropUntil > 0 {
		kept := copy(details, details[dropUntil:])
		for i := kept; i < len(details); i++ {
			details[i] = RequestDetail{}
		}
		s.detailCount -= dropUntil
		if s.detailCount < 0 {
			s.detailCount = 0
		}
		details = details[:kept]
	}
	if detail.Timestamp.Before(cutoff) {
		modelStatsValue.Details = details
		return
	}
	details = append(details, detail)
	s.detailCount++
	if requestDetailsLimit > 0 && len(details) > requestDetailsLimit {
		drop := len(details) - requestDetailsLimit
		kept := copy(details, details[drop:])
		for i := kept; i < len(details); i++ {
			details[i] = RequestDetail{}
		}
		s.detailCount -= drop
		if s.detailCount < 0 {
			s.detailCount = 0
		}
		details = details[:kept]
	}
	modelStatsValue.Details = details
	s.trimTotalDetailsLocked()
}

func (s *RequestStatistics) trimTotalDetailsLocked() {
	if s == nil || maxUsageStoredDetailsTotal <= 0 {
		return
	}
	for s.detailCount > maxUsageStoredDetailsTotal {
		if !s.dropOldestDetailLocked() {
			s.detailCount = s.recountDetailsLocked()
			if s.detailCount <= maxUsageStoredDetailsTotal || !s.dropOldestDetailLocked() {
				return
			}
		}
	}
}

func (s *RequestStatistics) dropOldestDetailLocked() bool {
	if s == nil {
		return false
	}
	var oldest *modelStats
	var oldestTime time.Time
	for _, stats := range s.apis {
		if stats == nil {
			continue
		}
		for _, modelStatsValue := range stats.Models {
			if modelStatsValue == nil || len(modelStatsValue.Details) == 0 {
				continue
			}
			timestamp := modelStatsValue.Details[0].Timestamp
			if oldest == nil || timestamp.Before(oldestTime) {
				oldest = modelStatsValue
				oldestTime = timestamp
			}
		}
	}
	if oldest == nil {
		return false
	}
	copy(oldest.Details, oldest.Details[1:])
	oldest.Details[len(oldest.Details)-1] = RequestDetail{}
	oldest.Details = oldest.Details[:len(oldest.Details)-1]
	s.detailCount--
	if s.detailCount < 0 {
		s.detailCount = 0
	}
	return true
}

func (s *RequestStatistics) recountDetailsLocked() int {
	if s == nil {
		return 0
	}
	count := 0
	for _, stats := range s.apis {
		if stats == nil {
			continue
		}
		for _, modelStatsValue := range stats.Models {
			if modelStatsValue != nil {
				count += len(modelStatsValue.Details)
			}
		}
	}
	return count
}

// Snapshot returns a copy of the aggregated metrics for external consumption.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	return s.snapshot(false, 0)
}

// SnapshotWithDetails returns a copy of the aggregated metrics including retained request details.
func (s *RequestStatistics) SnapshotWithDetails() StatisticsSnapshot {
	return s.snapshot(true, 0)
}

// SnapshotWithDetailsLimit returns a copy of the aggregated metrics with at most
// limit recent request details per model. A non-positive limit means no limit.
func (s *RequestStatistics) SnapshotWithDetailsLimit(limit int) StatisticsSnapshot {
	return s.snapshot(true, limit)
}

func (s *RequestStatistics) snapshot(includeDetails bool, detailLimit int) StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens
	if len(s.userAgents) > 0 {
		result.UserAgents = make([]string, 0, len(s.userAgents))
		for userAgent := range s.userAgents {
			result.UserAgents = append(result.UserAgents, userAgent)
		}
		sort.Strings(result.UserAgents)
	}
	if len(s.sourceStats) > 0 {
		result.SourceStats = make(map[string]RequestOutcomeStats, len(s.sourceStats))
		for key, value := range s.sourceStats {
			result.SourceStats[key] = value
		}
	}
	if len(s.authIndexStats) > 0 {
		result.AuthIndexStats = make(map[string]RequestOutcomeStats, len(s.authIndexStats))
		for key, value := range s.authIndexStats {
			result.AuthIndexStats[key] = value
		}
	}

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			SuccessCount:  stats.SuccessCount,
			FailureCount:  stats.FailureCount,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			modelSnapshot := ModelSnapshot{
				TotalRequests:  modelStatsValue.TotalRequests,
				TotalTokens:    modelStatsValue.TotalTokens,
				SuccessCount:   modelStatsValue.SuccessCount,
				FailureCount:   modelStatsValue.FailureCount,
				LatencyTotalMs: modelStatsValue.LatencyTotalMs,
				LatencySamples: modelStatsValue.LatencySamples,
			}
			if includeDetails {
				details := modelStatsValue.Details
				if detailLimit > 0 && len(details) > detailLimit {
					details = details[len(details)-detailLimit:]
				}
				requestDetails := make([]RequestDetail, len(details))
				copy(requestDetails, details)
				modelSnapshot.Details = requestDetails
			}
			apiSnapshot.Models[modelName] = modelSnapshot
		}
		result.APIs[apiName] = apiSnapshot
	}

	result.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		result.RequestsByDay[k] = v
	}

	result.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		key := formatHour(hour)
		result.RequestsByHour[key] = v
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		key := formatHour(hour)
		result.TokensByHour[key] = v
	}

	result.Usage20m = cloneUsage20mSnapshot(s.usage20m)

	return result
}

func cloneUsage20mSnapshot(source Usage20mSnapshot) Usage20mSnapshot {
	if len(source) == 0 {
		return nil
	}
	out := make(Usage20mSnapshot, len(source))
	for provider, buckets := range source {
		if len(buckets) == 0 {
			continue
		}
		provider = capUsageStoredString(provider)
		if provider == "" {
			continue
		}
		outBuckets := make(map[string]map[string]map[string]UsageBucketStats, len(buckets))
		for bucket, identities := range buckets {
			if len(identities) == 0 {
				continue
			}
			bucket = capUsageStoredString(bucket)
			if bucket == "" {
				continue
			}
			outIdentities := make(map[string]map[string]UsageBucketStats, len(identities))
			for identity, models := range identities {
				if len(models) == 0 {
					continue
				}
				identity = capUsageStoredString(identity)
				if identity == "" {
					continue
				}
				outModels := make(map[string]UsageBucketStats, len(models))
				for model, stats := range models {
					model = normaliseUsageDimension(model, "unknown")
					outModels[model] = normaliseUsageBucketStats(stats)
				}
				if len(outModels) > 0 {
					outIdentities[identity] = outModels
				}
			}
			if len(outIdentities) > 0 {
				outBuckets[bucket] = outIdentities
			}
		}
		if len(outBuckets) > 0 {
			out[provider] = outBuckets
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normaliseUsageBucketStats(stats UsageBucketStats) UsageBucketStats {
	if stats.TotalRequests < 0 {
		stats.TotalRequests = 0
	}
	if stats.SuccessCount < 0 {
		stats.SuccessCount = 0
	}
	if stats.FailureCount < 0 {
		stats.FailureCount = 0
	}
	stats.InputTokens = nonNegativeInt64(stats.InputTokens)
	stats.OutputTokens = nonNegativeInt64(stats.OutputTokens)
	stats.ReasoningTokens = nonNegativeInt64(stats.ReasoningTokens)
	stats.CachedTokens = nonNegativeInt64(stats.CachedTokens)
	stats.CacheCreationInputTokens = nonNegativeInt64(stats.CacheCreationInputTokens)
	stats.CacheCreation5mInputTokens = nonNegativeInt64(stats.CacheCreation5mInputTokens)
	stats.CacheCreation1hInputTokens = nonNegativeInt64(stats.CacheCreation1hInputTokens)
	stats.CacheReadInputTokens = nonNegativeInt64(stats.CacheReadInputTokens)
	stats.TotalTokens = nonNegativeInt64(stats.TotalTokens)
	return stats
}

func nonNegativeInt64(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

type MergeResult struct {
	Added   int64 `json:"added"`
	Skipped int64 `json:"skipped"`
}

// MergeSnapshot merges an exported statistics snapshot into the current store.
// Existing data is preserved and duplicate request details are skipped.
func (s *RequestStatistics) MergeSnapshot(snapshot StatisticsSnapshot) MergeResult {
	result := MergeResult{}
	if s == nil {
		return result
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})
	for apiName, stats := range s.apis {
		if stats == nil {
			continue
		}
		for modelName, modelStatsValue := range stats.Models {
			if modelStatsValue == nil {
				continue
			}
			for _, detail := range modelStatsValue.Details {
				seen[dedupKey(apiName, modelName, detail)] = struct{}{}
			}
		}
	}

	for apiName, apiSnapshot := range snapshot.APIs {
		apiName = capUsageStoredString(apiName)
		if apiName == "" {
			continue
		}
		stats, ok := s.apis[apiName]
		if !ok || stats == nil {
			stats = &apiStats{Models: make(map[string]*modelStats)}
			s.apis[apiName] = stats
		} else if stats.Models == nil {
			stats.Models = make(map[string]*modelStats)
		}
		for modelName, modelSnapshot := range apiSnapshot.Models {
			modelName = normaliseUsageDimension(modelName, "unknown")
			for _, detail := range modelSnapshot.Details {
				detail = normaliseRequestDetail(detail)
				if detail.Timestamp.IsZero() {
					detail.Timestamp = time.Now()
				}
				key := dedupKey(apiName, modelName, detail)
				if _, exists := seen[key]; exists {
					result.Skipped++
					continue
				}
				seen[key] = struct{}{}
				s.recordImported(apiName, modelName, stats, detail)
				result.Added++
			}
		}
	}
	s.mergeUsage20mSnapshotLocked(snapshot.Usage20m)

	return result
}

func (s *RequestStatistics) mergeUsage20mSnapshotLocked(snapshot Usage20mSnapshot) {
	if s == nil || len(snapshot) == 0 {
		return
	}
	if s.usage20m == nil {
		s.usage20m = make(Usage20mSnapshot)
	}
	for provider, buckets := range cloneUsage20mSnapshot(snapshot) {
		providerBuckets := s.usage20m[provider]
		if providerBuckets == nil {
			providerBuckets = make(map[string]map[string]map[string]UsageBucketStats)
			s.usage20m[provider] = providerBuckets
		}
		for bucket, identities := range buckets {
			identityBuckets := providerBuckets[bucket]
			if identityBuckets == nil {
				identityBuckets = make(map[string]map[string]UsageBucketStats)
				providerBuckets[bucket] = identityBuckets
			}
			for identity, models := range identities {
				modelBuckets := identityBuckets[identity]
				if modelBuckets == nil {
					modelBuckets = make(map[string]UsageBucketStats)
					identityBuckets[identity] = modelBuckets
				}
				for model, stats := range models {
					existing := modelBuckets[model]
					modelBuckets[model] = mergeUsageBucketStats(existing, stats)
				}
			}
		}
	}
}

func mergeUsageBucketStats(existing, imported UsageBucketStats) UsageBucketStats {
	return UsageBucketStats{
		TotalRequests:              maxInt64(existing.TotalRequests, imported.TotalRequests),
		SuccessCount:               maxInt64(existing.SuccessCount, imported.SuccessCount),
		FailureCount:               maxInt64(existing.FailureCount, imported.FailureCount),
		InputTokens:                maxInt64(existing.InputTokens, imported.InputTokens),
		OutputTokens:               maxInt64(existing.OutputTokens, imported.OutputTokens),
		ReasoningTokens:            maxInt64(existing.ReasoningTokens, imported.ReasoningTokens),
		CachedTokens:               maxInt64(existing.CachedTokens, imported.CachedTokens),
		CacheCreationInputTokens:   maxInt64(existing.CacheCreationInputTokens, imported.CacheCreationInputTokens),
		CacheCreation5mInputTokens: maxInt64(existing.CacheCreation5mInputTokens, imported.CacheCreation5mInputTokens),
		CacheCreation1hInputTokens: maxInt64(existing.CacheCreation1hInputTokens, imported.CacheCreation1hInputTokens),
		CacheReadInputTokens:       maxInt64(existing.CacheReadInputTokens, imported.CacheReadInputTokens),
		TotalTokens:                maxInt64(existing.TotalTokens, imported.TotalTokens),
		LatencyTotalMs:             maxInt64(existing.LatencyTotalMs, imported.LatencyTotalMs),
		LatencySamples:             maxInt64(existing.LatencySamples, imported.LatencySamples),
	}
}

func maxInt64(a, b int64) int64 {
	if b > a {
		return b
	}
	return a
}

func (s *RequestStatistics) recordImported(apiName, modelName string, stats *apiStats, detail RequestDetail) {
	totalTokens := detail.Tokens.TotalTokens
	if totalTokens < 0 {
		totalTokens = 0
	}

	s.totalRequests++
	if detail.Failed {
		s.failureCount++
	} else {
		s.successCount++
	}
	s.totalTokens += totalTokens

	s.updateAPIStats(stats, modelName, detail, detail.Timestamp)
	if userAgent := capUsageStoredString(detail.UserAgent); userAgent != "" {
		if s.userAgents == nil {
			s.userAgents = make(map[string]struct{})
		}
		addBoundedUsageSetValue(s.userAgents, userAgent, maxUsageUserAgents)
	}
	if source := boundedUsageStatsKey(s.sourceStats, detail.Source, maxUsageSources); source != "" {
		if s.sourceStats == nil {
			s.sourceStats = make(map[string]RequestOutcomeStats)
		}
		statsValue := s.sourceStats[source]
		if detail.Failed {
			statsValue.Failure++
		} else {
			statsValue.Success++
		}
		s.sourceStats[source] = statsValue
	}
	dayKey := detail.Timestamp.Format("2006-01-02")
	hourKey := detail.Timestamp.Hour()

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens
}

func dedupKey(apiName, modelName string, detail RequestDetail) string {
	timestamp := detail.Timestamp.UTC().Format(time.RFC3339Nano)
	tokens := normaliseTokenStats(detail.Tokens)
	return fmt.Sprintf(
		"%s|%s|%s|%s|%s|%s|%d|%d|%s|%s|%t|%d|%d|%d|%d|%d|%d|%d",
		apiName,
		modelName,
		timestamp,
		detail.Source,
		detail.UpstreamModel,
		strings.TrimSpace(detail.UserAgent),
		normaliseInputChars(detail.InputChars),
		normaliseStatusCode(detail.StatusCode),
		strings.TrimSpace(detail.ErrorReason),
		strings.TrimSpace(detail.ErrorMessage),
		detail.Failed,
		tokens.InputTokens,
		tokens.OutputTokens,
		tokens.ReasoningTokens,
		tokens.CachedTokens,
		tokens.CacheCreationInputTokens,
		tokens.CacheReadInputTokens,
		tokens.TotalTokens,
	)
}

func resolveAPIIdentifier(ctx context.Context, record coreusage.Record) string {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
			path := ginCtx.FullPath()
			if path == "" && ginCtx.Request != nil {
				path = ginCtx.Request.URL.Path
			}
			method := ""
			if ginCtx.Request != nil {
				method = ginCtx.Request.Method
			}
			if path != "" {
				if method != "" {
					return method + " " + path
				}
				return path
			}
		}
	}
	if record.Provider != "" {
		return record.Provider
	}
	return "unknown"
}

func resolveSuccess(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return true
	}
	status := ginCtx.Writer.Status()
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

const httpStatusBadRequest = 400

func resolveUserAgent(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return ""
	}
	return strings.TrimSpace(ginCtx.Request.UserAgent())
}

func resolveInputChars(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Request == nil {
		return 0
	}
	if ginCtx.Request.ContentLength > 0 {
		return ginCtx.Request.ContentLength
	}
	return 0
}

func resolveDownstreamStatus(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil || ginCtx.Writer == nil {
		return 0
	}
	return normaliseStatusCode(ginCtx.Writer.Status())
}

func normaliseDetail(detail coreusage.Detail) TokenStats {
	tokens := TokenStats{
		InputTokens:              detail.InputTokens,
		OutputTokens:             detail.OutputTokens,
		ReasoningTokens:          detail.ReasoningTokens,
		CachedTokens:             detail.CachedTokens,
		CacheCreationInputTokens: detail.CacheCreationTokens,
		CacheReadInputTokens:     detail.CacheReadTokens,
		TotalTokens:              detail.TotalTokens,
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.CacheCreationTokens + detail.CacheReadTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = detail.InputTokens + detail.OutputTokens + detail.CachedTokens
	}
	return tokens
}

func normaliseTokenStats(tokens TokenStats) TokenStats {
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.CacheCreationInputTokens + tokens.CacheReadInputTokens
	}
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.InputTokens + tokens.OutputTokens + tokens.CachedTokens
	}
	return tokens
}

func normaliseLatency(latency time.Duration) int64 {
	if latency <= 0 {
		return 0
	}
	return latency.Milliseconds()
}

func normaliseInputChars(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

func normaliseStatusCode(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}
