package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"github.com/tiktoken-go/tokenizer"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const (
	codexUserAgent  = "codex-tui/0.118.0 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9 (codex-tui; 0.118.0)"
	codexOriginator = "codex-tui"
)

var dataTag = []byte("data:")
var eventTag = []byte("event:")

type codexResponseMetadataState struct {
	responseID string
	createdAt  int64
}

// Streamed Codex responses may emit response.output_item.done events while leaving
// response.completed.response.output empty. Keep the stream path aligned with the
// already-patched non-stream path by reconstructing response.output from those items.
func collectCodexOutputItemDone(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) {
	itemResult := gjson.GetBytes(eventData, "item")
	if !itemResult.Exists() || itemResult.Type != gjson.JSON {
		return
	}
	outputIndexResult := gjson.GetBytes(eventData, "output_index")
	if outputIndexResult.Exists() {
		outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
		return
	}
	*outputItemsFallback = append(*outputItemsFallback, []byte(itemResult.Raw))
}

func patchCodexCompletedOutput(eventData []byte, outputItemsByIndex map[int64][]byte, outputItemsFallback [][]byte) []byte {
	outputResult := gjson.GetBytes(eventData, "response.output")
	shouldPatchOutput := (!outputResult.Exists() || !outputResult.IsArray() || len(outputResult.Array()) == 0) && (len(outputItemsByIndex) > 0 || len(outputItemsFallback) > 0)
	if !shouldPatchOutput {
		return eventData
	}

	indexes := make([]int64, 0, len(outputItemsByIndex))
	for idx := range outputItemsByIndex {
		indexes = append(indexes, idx)
	}
	sort.Slice(indexes, func(i, j int) bool {
		return indexes[i] < indexes[j]
	})

	items := make([][]byte, 0, len(outputItemsByIndex)+len(outputItemsFallback))
	for _, idx := range indexes {
		items = append(items, outputItemsByIndex[idx])
	}
	items = append(items, outputItemsFallback...)

	outputArray := []byte("[]")
	if len(items) > 0 {
		var buf bytes.Buffer
		totalLen := 2
		for _, item := range items {
			totalLen += len(item)
		}
		if len(items) > 1 {
			totalLen += len(items) - 1
		}
		buf.Grow(totalLen)
		buf.WriteByte('[')
		for i, item := range items {
			if i > 0 {
				buf.WriteByte(',')
			}
			buf.Write(item)
		}
		buf.WriteByte(']')
		outputArray = buf.Bytes()
	}

	completedDataPatched, _ := sjson.SetRawBytes(eventData, "response.output", outputArray)
	return completedDataPatched
}

func codexNormalizeEventType(eventName string, eventData []byte) ([]byte, string, bool) {
	eventType := strings.TrimSpace(gjson.GetBytes(eventData, "type").String())
	eventName = strings.TrimSpace(eventName)
	if eventType != "" || !strings.HasPrefix(eventName, "response.") {
		return eventData, eventType, false
	}
	updated, err := sjson.SetBytes(eventData, "type", eventName)
	if err != nil || len(updated) == 0 {
		return eventData, eventType, false
	}
	return updated, eventName, true
}

func codexEmptyDataEventError(eventName string, eventData []byte) error {
	eventName = strings.TrimSpace(eventName)
	eventData = bytes.TrimSpace(eventData)
	if eventName != "" || !bytes.Equal(eventData, []byte("{}")) {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(eventData, "type").String()) != "" {
		return nil
	}
	return helps.NewResponseFormatError(http.StatusOK, "codex_upstream_empty_sse_event", "codex upstream returned an empty SSE data event")
}

func codexResponseMetadataRepairError() error {
	return helps.NewResponseFormatError(http.StatusOK, "codex_upstream_response_metadata_repaired", "codex upstream returned incomplete Responses SSE metadata; proxy repaired missing response fields")
}

func patchCodexResponseMetadata(eventData []byte, eventType string, model string, state *codexResponseMetadataState) ([]byte, bool) {
	switch strings.TrimSpace(eventType) {
	case "response.created", "response.in_progress", "response.completed":
	default:
		return eventData, false
	}
	if state == nil {
		return eventData, false
	}

	root := gjson.ParseBytes(eventData)
	if id := strings.TrimSpace(root.Get("response.id").String()); id != "" {
		state.responseID = id
	}
	if createdAt := root.Get("response.created_at").Int(); createdAt > 0 {
		state.createdAt = createdAt
	}
	if state.responseID == "" {
		state.responseID = "resp_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	}
	if state.createdAt <= 0 {
		state.createdAt = time.Now().Unix()
	}

	patched := eventData
	repaired := false
	if responseResult := root.Get("response"); !responseResult.Exists() || !responseResult.IsObject() {
		if updated, err := sjson.SetRawBytes(patched, "response", []byte(`{}`)); err == nil {
			patched = updated
			repaired = true
		}
	}
	if strings.TrimSpace(gjson.GetBytes(patched, "response.id").String()) == "" {
		if updated, err := sjson.SetBytes(patched, "response.id", state.responseID); err == nil {
			patched = updated
			repaired = true
		}
	}
	if strings.TrimSpace(gjson.GetBytes(patched, "response.object").String()) == "" {
		if updated, err := sjson.SetBytes(patched, "response.object", "response"); err == nil {
			patched = updated
			repaired = true
		}
	}
	if createdAtResult := gjson.GetBytes(patched, "response.created_at"); !createdAtResult.Exists() || createdAtResult.Int() <= 0 {
		if updated, err := sjson.SetBytes(patched, "response.created_at", state.createdAt); err == nil {
			patched = updated
			repaired = true
		}
	}
	if model != "" && strings.TrimSpace(gjson.GetBytes(patched, "response.model").String()) == "" {
		if updated, err := sjson.SetBytes(patched, "response.model", model); err == nil {
			patched = updated
			repaired = true
		}
	}
	if !gjson.GetBytes(patched, "response.background").Exists() {
		if updated, err := sjson.SetBytes(patched, "response.background", false); err == nil {
			patched = updated
			repaired = true
		}
	}
	if !gjson.GetBytes(patched, "response.error").Exists() {
		if updated, err := sjson.SetRawBytes(patched, "response.error", []byte(`null`)); err == nil {
			patched = updated
			repaired = true
		}
	}
	if !gjson.GetBytes(patched, "response.output").Exists() {
		if updated, err := sjson.SetRawBytes(patched, "response.output", []byte(`[]`)); err == nil {
			patched = updated
			repaired = true
		}
	}

	status := "in_progress"
	if eventType == "response.completed" {
		status = "completed"
	}
	if strings.TrimSpace(gjson.GetBytes(patched, "response.status").String()) == "" {
		if updated, err := sjson.SetBytes(patched, "response.status", status); err == nil {
			patched = updated
			repaired = true
		}
	}
	return patched, repaired
}

func codexSSEEventName(line []byte) (string, bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, eventTag) {
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(string(bytes.TrimSpace(line[len(eventTag):])))), true
}

func codexSSEData(line []byte) ([]byte, bool) {
	line = bytes.TrimSpace(line)
	if !bytes.HasPrefix(line, dataTag) {
		return nil, false
	}
	return bytes.TrimSpace(line[len(dataTag):]), true
}

func codexStreamErrorFromSSE(eventName string, eventData []byte) (statusErr, bool) {
	eventData = bytes.TrimSpace(eventData)
	if len(eventData) == 0 || bytes.Equal(eventData, []byte("[DONE]")) {
		return statusErr{}, false
	}

	root := gjson.ParseBytes(eventData)
	eventType := strings.ToLower(strings.TrimSpace(root.Get("type").String()))
	topLevelError := root.Get("error")
	responseError := root.Get("response.error")
	hasTopLevelError := topLevelError.Exists() && topLevelError.Type != gjson.Null
	hasResponseError := responseError.Exists() && responseError.Type != gjson.Null
	isErrorEvent := strings.EqualFold(strings.TrimSpace(eventName), "error") ||
		eventType == "error" ||
		eventType == "response.failed" ||
		hasTopLevelError ||
		hasResponseError
	if !isErrorEvent {
		return statusErr{}, false
	}

	message := firstCodexStreamErrorText(root, eventData, []string{
		"error.message",
		"message",
		"response.error.message",
	})
	statusCode := codexStreamErrorStatusCode(root, eventData, message)
	if message == "" {
		message = strings.TrimSpace(string(eventData))
	}
	if message == "" {
		message = http.StatusText(statusCode)
	}

	err := statusErr{code: statusCode, msg: message}
	if retryAfter := parseCodexRetryAfter(statusCode, eventData, time.Now()); retryAfter != nil {
		err.retryAfter = retryAfter
	}
	return err, true
}

func firstCodexStreamErrorText(root gjson.Result, raw []byte, paths []string) string {
	for _, path := range paths {
		result := root.Get(path)
		if !result.Exists() || result.Type == gjson.Null {
			continue
		}
		if result.Type == gjson.String {
			if text := strings.TrimSpace(result.String()); text != "" {
				return text
			}
			continue
		}
		if text := strings.TrimSpace(result.Raw); text != "" {
			return text
		}
	}
	return strings.TrimSpace(string(raw))
}

func codexStreamErrorStatusCode(root gjson.Result, raw []byte, message string) int {
	for _, path := range []string{
		"status_code",
		"status",
		"error.status_code",
		"error.status",
		"response.error.status_code",
		"response.error.status",
	} {
		result := root.Get(path)
		if !result.Exists() {
			continue
		}
		if result.Type == gjson.Number {
			if code := int(result.Int()); code >= 100 && code <= 599 {
				return code
			}
		}
		if codeText := strings.TrimSpace(result.String()); codeText != "" {
			switch codeText {
			case "400":
				return http.StatusBadRequest
			case "401":
				return http.StatusUnauthorized
			case "403":
				return http.StatusForbidden
			case "404":
				return http.StatusNotFound
			case "408":
				return http.StatusRequestTimeout
			case "429":
				return http.StatusTooManyRequests
			case "500":
				return http.StatusInternalServerError
			case "502":
				return http.StatusBadGateway
			case "503":
				return http.StatusServiceUnavailable
			case "504":
				return http.StatusGatewayTimeout
			}
		}
	}

	fields := []string{message, string(raw)}
	for _, path := range []string{
		"code",
		"type",
		"error.code",
		"error.type",
		"response.error.code",
		"response.error.type",
	} {
		if result := root.Get(path); result.Exists() && result.Type != gjson.Null {
			fields = append(fields, result.String(), result.Raw)
		}
	}
	lower := strings.ToLower(strings.Join(fields, " "))
	switch {
	case strings.Contains(lower, "concurrency limit exceeded"),
		strings.Contains(lower, "rate limit"),
		strings.Contains(lower, "rate_limit"),
		strings.Contains(lower, "too many requests"),
		strings.Contains(lower, "tokens per min"),
		strings.Contains(lower, "tpm"),
		strings.Contains(lower, "usage_limit"),
		strings.Contains(lower, "quota"):
		return http.StatusTooManyRequests
	case strings.Contains(lower, "invalid_api_key"),
		strings.Contains(lower, "unauthorized"):
		return http.StatusUnauthorized
	case strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "permission"):
		return http.StatusForbidden
	case strings.Contains(lower, "model_not_found"),
		strings.Contains(lower, "not found"):
		return http.StatusNotFound
	case strings.Contains(lower, "timeout"),
		strings.Contains(lower, "timed out"):
		return http.StatusRequestTimeout
	default:
		return http.StatusInternalServerError
	}
}

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg *config.Config
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

// PrepareRequest injects Codex credentials into the outgoing HTTP request.
func (e *CodexExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey, _ := codexCreds(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects Codex credentials into the request and executes it.
func (e *CodexExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("codex executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	useV1 := codexUseV1(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body = normalizeCodexRequestBody(body, codexRemoveEmptyInputName(e.cfg))

	url := buildCodexResponsesURL(baseURL, useV1, "/responses")
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return resp, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newCodexStatusErr(httpResp.StatusCode, b)
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)

	lines := bytes.Split(data, []byte("\n"))
	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	var metadataState codexResponseMetadataState
	var compatibilityRepairErr error
	var completedPayload []byte
	var sawCompleted bool
	var currentEvent string
	for _, line := range lines {
		if eventName, ok := codexSSEEventName(line); ok {
			currentEvent = eventName
			continue
		}
		if len(bytes.TrimSpace(line)) == 0 && currentEvent == "error" {
			err = statusErr{code: http.StatusInternalServerError, msg: "codex upstream stream error"}
			return resp, err
		}
		eventData, ok := codexSSEData(line)
		if !ok {
			continue
		}
		eventName := currentEvent
		currentEvent = ""
		eventData, eventType, typeRepaired := codexNormalizeEventType(eventName, eventData)
		if typeRepaired && compatibilityRepairErr == nil {
			compatibilityRepairErr = codexResponseMetadataRepairError()
		}
		if errEmptyEvent := codexEmptyDataEventError(eventName, eventData); errEmptyEvent != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errEmptyEvent)
			err = errEmptyEvent
			return resp, err
		}
		if streamErr, ok := codexStreamErrorFromSSE(eventName, eventData); ok {
			err = streamErr
			return resp, err
		}
		if errValidate := helps.ValidateDownstreamStreamChunkWithOutputFilterForProvider(sdktranslator.FormatOpenAIResponse, e.Identifier(), line, helps.OutputFilterFromConfig(e.cfg)); errValidate != nil {
			err = errValidate
			return resp, err
		}

		var metadataRepaired bool
		eventData, metadataRepaired = patchCodexResponseMetadata(eventData, eventType, baseModel, &metadataState)
		if metadataRepaired && compatibilityRepairErr == nil {
			compatibilityRepairErr = codexResponseMetadataRepairError()
		}

		if eventType == "response.output_item.done" {
			itemResult := gjson.GetBytes(eventData, "item")
			if !itemResult.Exists() || itemResult.Type != gjson.JSON {
				continue
			}
			outputIndexResult := gjson.GetBytes(eventData, "output_index")
			if outputIndexResult.Exists() {
				outputItemsByIndex[outputIndexResult.Int()] = []byte(itemResult.Raw)
			} else {
				outputItemsFallback = append(outputItemsFallback, []byte(itemResult.Raw))
			}
			continue
		}

		if eventType != "response.completed" {
			continue
		}

		reporter.SetUpstreamModelFromPayload(eventData)
		if detail, ok := helps.ParseCodexUsage(eventData); ok {
			reporter.SetDetail(detail)
		}

		completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)

		var param any
		out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, completedData, &param)
		if _, errValidate := helps.ValidateDownstreamNonStreamPayloadWithOutputFilter(from, e.Identifier(), out, helps.OutputFilterFromConfig(e.cfg)); errValidate != nil {
			err = errValidate
			return resp, err
		}
		completedPayload = out
		sawCompleted = true
	}
	if sawCompleted {
		if compatibilityRepairErr != nil {
			reporter.PublishFailureWithError(ctx, compatibilityRepairErr)
		} else {
			reporter.PublishCurrent(ctx)
		}
		resp = cliproxyexecutor.Response{Payload: completedPayload, Headers: httpResp.Header.Clone()}
		return resp, nil
	}
	if len(bytes.TrimSpace(data)) == 0 {
		err = helps.NewResponseFormatError(http.StatusOK, "upstream_empty_response", "codex upstream returned an empty response")
		return resp, err
	}
	err = statusErr{code: 408, msg: "stream error: stream disconnected before completion: stream closed before response.completed"}
	return resp, err
}

func (e *CodexExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	useV1 := codexUseV1(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "stream")
	body = normalizeCodexRequestBody(body, codexRemoveEmptyInputName(e.cfg))

	url := buildCodexResponsesURL(baseURL, useV1, "/responses/compact")
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return resp, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, false, e.cfg)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = newCodexStatusErr(httpResp.StatusCode, b)
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.SetDetail(helps.ParseOpenAIUsage(data))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, data, &param)
	if _, errValidate := helps.ValidateDownstreamNonStreamPayloadWithOutputFilter(from, e.Identifier(), out, helps.OutputFilterFromConfig(e.cfg)); errValidate != nil {
		err = errValidate
		return resp, err
	}
	reporter.PublishCurrent(ctx)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}
	useV1 := codexUseV1(auth)

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body, _ = sjson.SetBytes(body, "model", baseModel)
	body = normalizeCodexRequestBody(body, codexRemoveEmptyInputName(e.cfg))

	url := buildCodexResponsesURL(baseURL, useV1, "/responses")
	httpReq, err := e.cacheHelper(ctx, from, url, req, body)
	if err != nil {
		return nil, err
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		var metadataState codexResponseMetadataState
		var compatibilityRepairErr error
		var linesScanned int
		var currentEvent string
		var sawCompleted bool
		for scanner.Scan() {
			line := scanner.Bytes()
			linesScanned++
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if errValidate := helps.ValidateDownstreamStreamChunkWithOutputFilterForProvider(sdktranslator.FormatOpenAIResponse, e.Identifier(), line, helps.OutputFilterFromConfig(e.cfg)); errValidate != nil {
				reporter.PublishFailureWithError(ctx, errValidate)
				out <- cliproxyexecutor.StreamChunk{Err: errValidate}
				return
			}
			if eventName, ok := codexSSEEventName(line); ok {
				currentEvent = eventName
				if eventName == "error" {
					continue
				}
				if eventName == "" {
					continue
				}
			}
			if len(bytes.TrimSpace(line)) == 0 && currentEvent == "error" {
				streamErr := statusErr{code: http.StatusInternalServerError, msg: "codex upstream stream error"}
				reporter.PublishFailureWithError(ctx, streamErr)
				out <- cliproxyexecutor.StreamChunk{Err: streamErr}
				return
			}
			translatedLine := bytes.Clone(line)

			if bytes.HasPrefix(line, dataTag) {
				data := bytes.TrimSpace(line[5:])
				eventName := currentEvent
				currentEvent = ""
				data, eventType, typeRepaired := codexNormalizeEventType(eventName, data)
				if typeRepaired && compatibilityRepairErr == nil {
					compatibilityRepairErr = codexResponseMetadataRepairError()
				}
				if errEmptyEvent := codexEmptyDataEventError(eventName, data); errEmptyEvent != nil {
					helps.RecordAPIResponseError(ctx, e.cfg, errEmptyEvent)
					reporter.PublishFailureWithError(ctx, errEmptyEvent)
					out <- cliproxyexecutor.StreamChunk{Err: errEmptyEvent}
					return
				}
				if streamErr, ok := codexStreamErrorFromSSE(eventName, data); ok {
					reporter.PublishFailureWithError(ctx, streamErr)
					out <- cliproxyexecutor.StreamChunk{Err: streamErr}
					return
				}
				var metadataRepaired bool
				data, metadataRepaired = patchCodexResponseMetadata(data, eventType, baseModel, &metadataState)
				if metadataRepaired && compatibilityRepairErr == nil {
					compatibilityRepairErr = codexResponseMetadataRepairError()
				}
				switch eventType {
				case "response.output_item.done":
					collectCodexOutputItemDone(data, outputItemsByIndex, &outputItemsFallback)
				case "response.completed":
					sawCompleted = true
					reporter.SetUpstreamModelFromPayload(data)
					if detail, ok := helps.ParseCodexUsage(data); ok {
						reporter.SetDetail(detail)
					}
					data = patchCodexCompletedOutput(data, outputItemsByIndex, outputItemsFallback)
				}
				translatedLine = append([]byte("data: "), data...)
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, body, translatedLine, &param)
			for i := range chunks {
				if errValidate := helps.ValidateDownstreamStreamChunkWithOutputFilterForProvider(from, e.Identifier(), chunks[i], helps.OutputFilterFromConfig(e.cfg)); errValidate != nil {
					reporter.PublishFailureWithError(ctx, errValidate)
					out <- cliproxyexecutor.StreamChunk{Err: errValidate}
					return
				}
				out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailureWithError(ctx, errScan)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
			return
		}
		if linesScanned == 0 {
			errEmpty := helps.NewResponseFormatError(http.StatusOK, "upstream_empty_response", "codex upstream returned an empty response")
			reporter.PublishFailureWithError(ctx, errEmpty)
			out <- cliproxyexecutor.StreamChunk{Err: errEmpty}
			return
		}
		if !sawCompleted {
			errIncomplete := statusErr{code: http.StatusRequestTimeout, msg: "stream error: stream disconnected before completion: stream closed before response.completed"}
			reporter.PublishFailureWithError(ctx, errIncomplete)
			out <- cliproxyexecutor.StreamChunk{Err: errIncomplete}
			return
		}
		if compatibilityRepairErr != nil {
			reporter.PublishFailureWithError(ctx, compatibilityRepairErr)
		} else {
			reporter.PublishCurrent(ctx)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *CodexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	body := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	body, err := thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	body, _ = sjson.SetBytes(body, "model", baseModel)
	body, _ = sjson.DeleteBytes(body, "previous_response_id")
	body, _ = sjson.DeleteBytes(body, "prompt_cache_retention")
	body, _ = sjson.DeleteBytes(body, "safety_identifier")
	body, _ = sjson.DeleteBytes(body, "stream_options")
	body, _ = sjson.SetBytes(body, "stream", false)
	body = normalizeCodexRequestBody(body, codexRemoveEmptyInputName(e.cfg))

	enc, err := tokenizerForCodexModel(baseModel)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: tokenizer init failed: %w", err)
	}

	count, err := countCodexInputTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("codex executor: token counting failed: %w", err)
	}

	usageJSON := fmt.Sprintf(`{"response":{"usage":{"input_tokens":%d,"output_tokens":0,"total_tokens":%d}}}`, count, count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, []byte(usageJSON))
	return cliproxyexecutor.Response{Payload: translated}, nil
}

func tokenizerForCodexModel(model string) (tokenizer.Codec, error) {
	sanitized := strings.ToLower(strings.TrimSpace(model))
	switch {
	case sanitized == "":
		return tokenizer.Get(tokenizer.Cl100kBase)
	case strings.HasPrefix(sanitized, "gpt-5"):
		return tokenizer.ForModel(tokenizer.GPT5)
	case strings.HasPrefix(sanitized, "gpt-4.1"):
		return tokenizer.ForModel(tokenizer.GPT41)
	case strings.HasPrefix(sanitized, "gpt-4o"):
		return tokenizer.ForModel(tokenizer.GPT4o)
	case strings.HasPrefix(sanitized, "gpt-4"):
		return tokenizer.ForModel(tokenizer.GPT4)
	case strings.HasPrefix(sanitized, "gpt-3.5"), strings.HasPrefix(sanitized, "gpt-3"):
		return tokenizer.ForModel(tokenizer.GPT35Turbo)
	default:
		return tokenizer.Get(tokenizer.Cl100kBase)
	}
}

func countCodexInputTokens(enc tokenizer.Codec, body []byte) (int64, error) {
	if enc == nil {
		return 0, fmt.Errorf("encoder is nil")
	}
	if len(body) == 0 {
		return 0, nil
	}

	root := gjson.ParseBytes(body)
	var segments []string

	if inst := strings.TrimSpace(root.Get("instructions").String()); inst != "" {
		segments = append(segments, inst)
	}

	inputItems := root.Get("input")
	if inputItems.IsArray() {
		arr := inputItems.Array()
		for i := range arr {
			item := arr[i]
			switch item.Get("type").String() {
			case "message":
				content := item.Get("content")
				if content.IsArray() {
					parts := content.Array()
					for j := range parts {
						part := parts[j]
						if text := strings.TrimSpace(part.Get("text").String()); text != "" {
							segments = append(segments, text)
						}
					}
				}
			case "function_call":
				if name := strings.TrimSpace(item.Get("name").String()); name != "" {
					segments = append(segments, name)
				}
				if args := strings.TrimSpace(item.Get("arguments").String()); args != "" {
					segments = append(segments, args)
				}
			case "function_call_output":
				if out := strings.TrimSpace(item.Get("output").String()); out != "" {
					segments = append(segments, out)
				}
			default:
				if text := strings.TrimSpace(item.Get("text").String()); text != "" {
					segments = append(segments, text)
				}
			}
		}
	}

	tools := root.Get("tools")
	if tools.IsArray() {
		tarr := tools.Array()
		for i := range tarr {
			tool := tarr[i]
			if name := strings.TrimSpace(tool.Get("name").String()); name != "" {
				segments = append(segments, name)
			}
			if desc := strings.TrimSpace(tool.Get("description").String()); desc != "" {
				segments = append(segments, desc)
			}
			if params := tool.Get("parameters"); params.Exists() {
				val := params.Raw
				if params.Type == gjson.String {
					val = params.String()
				}
				if trimmed := strings.TrimSpace(val); trimmed != "" {
					segments = append(segments, trimmed)
				}
			}
		}
	}

	textFormat := root.Get("text.format")
	if textFormat.Exists() {
		if name := strings.TrimSpace(textFormat.Get("name").String()); name != "" {
			segments = append(segments, name)
		}
		if schema := textFormat.Get("schema"); schema.Exists() {
			val := schema.Raw
			if schema.Type == gjson.String {
				val = schema.String()
			}
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				segments = append(segments, trimmed)
			}
		}
	}

	text := strings.Join(segments, "\n")
	if text == "" {
		return 0, nil
	}

	count, err := enc.Count(text)
	if err != nil {
		return 0, err
	}
	return int64(count), nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := codexauth.NewCodexAuthWithProxyURL(nil, auth.ProxyURL)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func (e *CodexExecutor) cacheHelper(ctx context.Context, from sdktranslator.Format, url string, req cliproxyexecutor.Request, rawJSON []byte) (*http.Request, error) {
	var cache helps.CodexCache
	if from == "claude" {
		userIDResult := gjson.GetBytes(req.Payload, "metadata.user_id")
		if userIDResult.Exists() {
			key := fmt.Sprintf("%s-%s", req.Model, userIDResult.String())
			var ok bool
			if cache, ok = helps.GetCodexCache(key); !ok {
				cache = helps.CodexCache{
					ID:     uuid.New().String(),
					Expire: time.Now().Add(1 * time.Hour),
				}
				helps.SetCodexCache(key, cache)
			}
		}
	} else if from == "openai-response" {
		promptCacheKey := gjson.GetBytes(req.Payload, "prompt_cache_key")
		if promptCacheKey.Exists() {
			cache.ID = promptCacheKey.String()
		}
	} else if from == "openai" {
		if apiKey := strings.TrimSpace(helps.APIKeyFromContext(ctx)); apiKey != "" {
			cache.ID = uuid.NewSHA1(uuid.NameSpaceOID, []byte("cli-proxy-api:codex:prompt-cache:"+apiKey)).String()
		}
	}

	if cache.ID != "" {
		rawJSON, _ = sjson.SetBytes(rawJSON, "prompt_cache_key", cache.ID)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(rawJSON))
	if err != nil {
		return nil, err
	}
	if cache.ID != "" {
		httpReq.Header.Set("Session_id", cache.ID)
	}
	return httpReq, nil
}

func applyCodexHeaders(r *http.Request, auth *cliproxyauth.Auth, token string, stream bool, cfg *config.Config) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	if ginHeaders.Get("X-Codex-Beta-Features") != "" {
		r.Header.Set("X-Codex-Beta-Features", ginHeaders.Get("X-Codex-Beta-Features"))
	}
	misc.EnsureHeader(r.Header, ginHeaders, "Version", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Codex-Turn-Metadata", "")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Client-Request-Id", "")
	cfgUserAgent, _ := codexHeaderDefaults(cfg, auth)
	ensureHeaderWithConfigPrecedence(r.Header, ginHeaders, "User-Agent", cfgUserAgent, codexUserAgent)

	if strings.Contains(r.Header.Get("User-Agent"), "Mac OS") {
		misc.EnsureHeader(r.Header, ginHeaders, "Session_id", uuid.NewString())
	}

	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	r.Header.Set("Connection", "Keep-Alive")

	isAPIKey := false
	if auth != nil && auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["api_key"]); v != "" {
			isAPIKey = true
		}
	}
	if originator := strings.TrimSpace(ginHeaders.Get("Originator")); originator != "" {
		r.Header.Set("Originator", originator)
	} else if !isAPIKey {
		r.Header.Set("Originator", codexOriginator)
	}
	if !isAPIKey {
		if auth != nil && auth.Metadata != nil {
			if accountID, ok := auth.Metadata["account_id"].(string); ok {
				r.Header.Set("Chatgpt-Account-Id", accountID)
			}
		}
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
}

func newCodexStatusErr(statusCode int, body []byte) statusErr {
	errCode := statusCode
	if isCodexModelCapacityError(body) {
		errCode = http.StatusTooManyRequests
	}
	err := statusErr{code: errCode, msg: string(body)}
	if retryAfter := parseCodexRetryAfter(errCode, body, time.Now()); retryAfter != nil {
		err.retryAfter = retryAfter
	}
	return err
}

func normalizeCodexInstructions(body []byte) []byte {
	instructions := gjson.GetBytes(body, "instructions")
	if !instructions.Exists() || instructions.Type == gjson.Null {
		body, _ = sjson.SetBytes(body, "instructions", "")
	}
	return body
}

func normalizeCodexRequestBody(body []byte, removeEmptyInputName bool) []byte {
	body = normalizeCodexInstructions(body)
	if !removeEmptyInputName {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	changed := false
	filteredInput := []byte("[]")
	for _, item := range input.Array() {
		itemRaw := []byte(item.Raw)
		name := item.Get("name")
		if !name.Exists() || name.Type != gjson.String || strings.TrimSpace(name.String()) != "" {
			filteredInput, _ = sjson.SetRawBytes(filteredInput, "-1", itemRaw)
			continue
		}

		changed = true
		if item.Get("type").String() == "function_call" {
			continue
		}

		itemRaw, _ = sjson.DeleteBytes(itemRaw, "name")
		filteredInput, _ = sjson.SetRawBytes(filteredInput, "-1", itemRaw)
	}
	if changed {
		body, _ = sjson.SetRawBytes(body, "input", filteredInput)
	}
	return body
}

func codexRemoveEmptyInputName(cfg *config.Config) bool {
	return cfg != nil && cfg.CodexRemoveEmptyInputName
}

func isCodexModelCapacityError(errorBody []byte) bool {
	if len(errorBody) == 0 {
		return false
	}
	candidates := []string{
		gjson.GetBytes(errorBody, "error.message").String(),
		gjson.GetBytes(errorBody, "message").String(),
		string(errorBody),
	}
	for _, candidate := range candidates {
		lower := strings.ToLower(strings.TrimSpace(candidate))
		if lower == "" {
			continue
		}
		if strings.Contains(lower, "selected model is at capacity") ||
			strings.Contains(lower, "model is at capacity. please try a different model") {
			return true
		}
	}
	return false
}

func parseCodexRetryAfter(statusCode int, errorBody []byte, now time.Time) *time.Duration {
	if statusCode != http.StatusTooManyRequests || len(errorBody) == 0 {
		return nil
	}
	if strings.TrimSpace(gjson.GetBytes(errorBody, "error.type").String()) != "usage_limit_reached" {
		return nil
	}
	if resetsAt := gjson.GetBytes(errorBody, "error.resets_at").Int(); resetsAt > 0 {
		resetAtTime := time.Unix(resetsAt, 0)
		if resetAtTime.After(now) {
			retryAfter := resetAtTime.Sub(now)
			return &retryAfter
		}
	}
	if resetsInSeconds := gjson.GetBytes(errorBody, "error.resets_in_seconds").Int(); resetsInSeconds > 0 {
		retryAfter := time.Duration(resetsInSeconds) * time.Second
		return &retryAfter
	}
	return nil
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func codexUseV1(a *cliproxyauth.Auth) bool {
	if a == nil || a.Attributes == nil {
		return false
	}
	raw, ok := a.Attributes["use_v1"]
	if !ok {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "false", "0", "no", "n", "off":
		return false
	default:
		return true
	}
}

func buildCodexResponsesURL(baseURL string, useV1 bool, path string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if useV1 && !strings.HasSuffix(strings.ToLower(trimmed), "/v1") {
		trimmed += "/v1"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return trimmed + path
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.CodexKey {
		entry := &e.cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.CodexKey {
			entry := &e.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}
