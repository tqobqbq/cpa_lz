package usage

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

const usageQueueMaxRecords = 4096

var usageQueue = struct {
	mu      sync.Mutex
	records [][]byte
}{}

type queuedUsageRecord struct {
	Timestamp                    string     `json:"timestamp"`
	TimestampMS                  int64      `json:"timestamp_ms"`
	Provider                     string     `json:"provider,omitempty"`
	Model                        string     `json:"model"`
	UpstreamModel                string     `json:"upstream_model,omitempty"`
	Source                       string     `json:"source,omitempty"`
	APIKey                       string     `json:"api_key,omitempty"`
	AuthID                       string     `json:"auth_id,omitempty"`
	UserAgent                    string     `json:"user_agent,omitempty"`
	InputChars                   int64      `json:"input_chars,omitempty"`
	LatencyMs                    int64      `json:"latency_ms,omitempty"`
	Failed                       bool       `json:"failed"`
	StatusCode                   int        `json:"status_code,omitempty"`
	ErrorReason                  string     `json:"error_reason,omitempty"`
	ErrorMessage                 string     `json:"error_message,omitempty"`
	RequestCount                 uint64     `json:"request_count,omitempty"`
	RetryRound                   int        `json:"retry_round,omitempty"`
	RoundDispatchIndex           int        `json:"round_dispatch_index,omitempty"`
	ParallelEligible             bool       `json:"parallel_eligible"`
	ProviderCooldownRemaining    int        `json:"provider_cooldown_remaining,omitempty"`
	ProviderCooldownGeneratedRaw float64    `json:"provider_cooldown_generated_raw,omitempty"`
	Tokens                       TokenStats `json:"tokens"`
}

func enqueueUsageQueueRecord(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	model := normaliseUsageDimension(record.Model, "unknown")
	queued := queuedUsageRecord{
		Timestamp:                    timestamp.UTC().Format(time.RFC3339Nano),
		TimestampMS:                  timestamp.UnixMilli(),
		Provider:                     capUsageStoredString(record.Provider),
		Model:                        model,
		UpstreamModel:                capUsageStoredString(record.UpstreamModel),
		Source:                       capUsageStoredString(record.Source),
		APIKey:                       capUsageStoredString(record.APIKey),
		AuthID:                       capUsageStoredString(record.AuthID),
		UserAgent:                    capUsageStoredString(record.UserAgent),
		InputChars:                   normaliseInputChars(record.InputChars),
		LatencyMs:                    normaliseLatency(record.Latency),
		Failed:                       record.Failed,
		StatusCode:                   normaliseStatusCode(record.StatusCode),
		ErrorReason:                  capUsageStoredString(record.ErrorReason),
		ErrorMessage:                 capUsageStoredString(record.ErrorMessage),
		RequestCount:                 record.RequestCount,
		RetryRound:                   record.RetryRound,
		RoundDispatchIndex:           record.RoundDispatchIndex,
		ParallelEligible:             record.ParallelEligible,
		ProviderCooldownRemaining:    record.ProviderCooldownRemaining,
		ProviderCooldownGeneratedRaw: record.ProviderCooldownGeneratedRaw,
		Tokens:                       normaliseDetail(record.Detail),
	}
	if !queued.Failed {
		queued.Failed = !resolveSuccess(ctx)
	}
	data, err := json.Marshal(queued)
	if err != nil {
		return
	}

	usageQueue.mu.Lock()
	defer usageQueue.mu.Unlock()
	trimUsageQueueForAppendLocked()
	usageQueue.records = append(usageQueue.records, data)
}

func trimUsageQueueForAppendLocked() {
	if usageQueueMaxRecords <= 0 || len(usageQueue.records) < usageQueueMaxRecords {
		return
	}
	drop := len(usageQueue.records) - usageQueueMaxRecords + 1
	copy(usageQueue.records, usageQueue.records[drop:])
	for i := len(usageQueue.records) - drop; i < len(usageQueue.records); i++ {
		usageQueue.records[i] = nil
	}
	usageQueue.records = usageQueue.records[:len(usageQueue.records)-drop]
}

// PopUsageQueue removes and returns up to count oldest queued usage records.
func PopUsageQueue(count int) [][]byte {
	if count <= 0 {
		count = 1
	}
	usageQueue.mu.Lock()
	defer usageQueue.mu.Unlock()
	if count > len(usageQueue.records) {
		count = len(usageQueue.records)
	}
	if count == 0 {
		return nil
	}
	out := make([][]byte, count)
	for i := 0; i < count; i++ {
		out[i] = append([]byte(nil), usageQueue.records[i]...)
		usageQueue.records[i] = nil
	}
	usageQueue.records = usageQueue.records[count:]
	return out
}

func clearUsageQueueForTest() {
	usageQueue.mu.Lock()
	defer usageQueue.mu.Unlock()
	for i := range usageQueue.records {
		usageQueue.records[i] = nil
	}
	usageQueue.records = nil
}
