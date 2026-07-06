package usage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

type requestMetadataKey struct{}

// RequestMetadata carries per-downstream-request scheduling details into usage records.
type RequestMetadata struct {
	RequestCount                 uint64
	RetryRound                   int
	RoundDispatchIndex           int
	ParallelEligible             bool
	ProviderCooldownRemaining    int
	ProviderCooldownGeneratedRaw float64
}

// Record contains the usage statistics captured for a single provider request.
type Record struct {
	Provider                     string
	Model                        string
	UpstreamModel                string
	APIKey                       string
	AuthID                       string
	AuthIndex                    string
	Source                       string
	UserAgent                    string
	InputChars                   int64
	RequestedAt                  time.Time
	Latency                      time.Duration
	Failed                       bool
	StatusCode                   int
	ErrorReason                  string
	ErrorMessage                 string
	RequestCount                 uint64
	RetryRound                   int
	RoundDispatchIndex           int
	ParallelEligible             bool
	ProviderCooldownRemaining    int
	ProviderCooldownGeneratedRaw float64
	Detail                       Detail
}

// Detail holds the token usage breakdown plus response metadata parsed with it.
type Detail struct {
	UpstreamModel              string
	InputTokens                int64
	OutputTokens               int64
	ReasoningTokens            int64
	CachedTokens               int64
	CacheCreationInputTokens   int64
	CacheCreation5mInputTokens int64
	CacheCreation1hInputTokens int64
	CacheReadInputTokens       int64
	TotalTokens                int64
}

// Plugin consumes usage records emitted by the proxy runtime.
type Plugin interface {
	HandleUsage(ctx context.Context, record Record)
}

type queueItem struct {
	ctx    context.Context
	record Record
}

// Manager maintains a queue of usage records and delivers them to registered plugins.
type Manager struct {
	once     sync.Once
	stopOnce sync.Once
	cancel   context.CancelFunc

	mu       sync.Mutex
	cond     *sync.Cond
	queue    []queueItem
	maxQueue int
	closed   bool

	pluginsMu sync.RWMutex
	plugins   []Plugin
}

// NewManager constructs a manager with a buffered queue.
func NewManager(buffer int) *Manager {
	if buffer <= 0 {
		buffer = 512
	}
	m := &Manager{maxQueue: buffer}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// Start launches the background dispatcher. Calling Start multiple times is safe.
func (m *Manager) Start(ctx context.Context) {
	if m == nil {
		return
	}
	m.once.Do(func() {
		if ctx == nil {
			ctx = context.Background()
		}
		var workerCtx context.Context
		workerCtx, m.cancel = context.WithCancel(ctx)
		go m.run(workerCtx)
	})
}

// Stop stops the dispatcher and drains the queue.
func (m *Manager) Stop() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
		m.mu.Lock()
		m.closed = true
		m.mu.Unlock()
		m.cond.Broadcast()
	})
}

// Register appends a plugin to the delivery list.
func (m *Manager) Register(plugin Plugin) {
	if m == nil || plugin == nil {
		return
	}
	m.pluginsMu.Lock()
	m.plugins = append(m.plugins, plugin)
	m.pluginsMu.Unlock()
}

// Publish enqueues a usage record for processing. If no plugin is registered
// the record will be discarded downstream.
func (m *Manager) Publish(ctx context.Context, record Record) {
	if m == nil {
		return
	}
	// ensure worker is running even if Start was not called explicitly
	m.Start(context.Background())
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.trimQueueForAppendLocked()
	m.queue = append(m.queue, queueItem{ctx: ctx, record: record})
	m.mu.Unlock()
	m.cond.Signal()
}

func (m *Manager) trimQueueForAppendLocked() {
	if m == nil || m.maxQueue <= 0 || len(m.queue) < m.maxQueue {
		return
	}
	drop := len(m.queue) - m.maxQueue + 1
	copy(m.queue, m.queue[drop:])
	for i := len(m.queue) - drop; i < len(m.queue); i++ {
		m.queue[i] = queueItem{}
	}
	m.queue = m.queue[:len(m.queue)-drop]
}

func (m *Manager) run(ctx context.Context) {
	for {
		m.mu.Lock()
		for !m.closed && len(m.queue) == 0 {
			m.cond.Wait()
		}
		if len(m.queue) == 0 && m.closed {
			m.mu.Unlock()
			return
		}
		item := m.queue[0]
		m.queue = m.queue[1:]
		m.mu.Unlock()
		m.dispatch(item)
	}
}

func (m *Manager) dispatch(item queueItem) {
	m.pluginsMu.RLock()
	plugins := make([]Plugin, len(m.plugins))
	copy(plugins, m.plugins)
	m.pluginsMu.RUnlock()
	if len(plugins) == 0 {
		return
	}
	for _, plugin := range plugins {
		if plugin == nil {
			continue
		}
		safeInvoke(plugin, item.ctx, item.record)
	}
}

func safeInvoke(plugin Plugin, ctx context.Context, record Record) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("usage: plugin panic recovered: %v", r)
		}
	}()
	plugin.HandleUsage(ctx, record)
}

var defaultManager = NewManager(512)

var requestCounter atomic.Uint64

var ErrParallelRequestAborted = errors.New("parallel upstream request aborted after another candidate succeeded")

// EnsureRequestContext attaches a process-local downstream request count when missing.
func EnsureRequestContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if MetadataFromContext(ctx).RequestCount > 0 {
		return ctx
	}
	return WithRequestMetadata(ctx, RequestMetadata{RequestCount: requestCounter.Add(1)})
}

// CurrentRequestCount returns the latest process-local downstream request sequence.
func CurrentRequestCount() uint64 {
	return requestCounter.Load()
}

// IsParallelRequestAborted reports whether ctx was canceled because another parallel candidate succeeded.
func IsParallelRequestAborted(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	return errors.Is(context.Cause(ctx), ErrParallelRequestAborted)
}

// WithRequestMetadata returns a context carrying usage request metadata.
func WithRequestMetadata(ctx context.Context, metadata RequestMetadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestMetadataKey{}, metadata)
}

// MetadataFromContext returns request metadata previously attached to ctx.
func MetadataFromContext(ctx context.Context) RequestMetadata {
	if ctx == nil {
		return RequestMetadata{}
	}
	metadata, _ := ctx.Value(requestMetadataKey{}).(RequestMetadata)
	return metadata
}

// DefaultManager returns the global usage manager instance.
func DefaultManager() *Manager { return defaultManager }

// RegisterPlugin registers a plugin on the default manager.
func RegisterPlugin(plugin Plugin) { DefaultManager().Register(plugin) }

// PublishRecord publishes a record using the default manager.
func PublishRecord(ctx context.Context, record Record) { DefaultManager().Publish(ctx, record) }

// StartDefault starts the default manager's dispatcher.
func StartDefault(ctx context.Context) { DefaultManager().Start(ctx) }

// StopDefault stops the default manager's dispatcher.
func StopDefault() { DefaultManager().Stop() }
