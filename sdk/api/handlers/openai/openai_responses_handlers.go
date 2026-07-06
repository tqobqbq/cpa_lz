// Package openai provides HTTP handlers for OpenAIResponses API endpoints.
// This package implements the OpenAIResponses-compatible API interface, including model listing
// and chat completion functionality. It supports both streaming and non-streaming responses,
// and manages a pool of clients to interact with backend services.
// The handlers translate OpenAIResponses API requests to the appropriate backend format and
// convert responses back to OpenAIResponses-compatible format.
package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	. "github.com/router-for-me/CLIProxyAPI/v6/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func writeResponsesSSEChunk(w io.Writer, chunk []byte) {
	if w == nil || len(chunk) == 0 {
		return
	}
	if _, err := w.Write(chunk); err != nil {
		return
	}
	if bytes.HasSuffix(chunk, []byte("\n\n")) || bytes.HasSuffix(chunk, []byte("\r\n\r\n")) {
		return
	}
	suffix := []byte("\n\n")
	if bytes.HasSuffix(chunk, []byte("\r\n")) {
		suffix = []byte("\r\n")
	} else if bytes.HasSuffix(chunk, []byte("\n")) {
		suffix = []byte("\n")
	}
	if _, err := w.Write(suffix); err != nil {
		return
	}
}

type responsesSSEFramer struct {
	pending []byte
}

func (f *responsesSSEFramer) WriteChunk(w io.Writer, chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	if responsesSSENeedsLineBreak(f.pending, chunk) {
		f.pending = append(f.pending, '\n')
	}
	f.pending = append(f.pending, chunk...)
	for {
		frameLen := responsesSSEFrameLen(f.pending)
		if frameLen == 0 {
			break
		}
		writeResponsesSSEChunk(w, f.pending[:frameLen])
		copy(f.pending, f.pending[frameLen:])
		f.pending = f.pending[:len(f.pending)-frameLen]
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = f.pending[:0]
		return
	}
	if len(f.pending) == 0 || !responsesSSECanEmitWithoutDelimiter(f.pending) {
		return
	}
	writeResponsesSSEChunk(w, f.pending)
	f.pending = f.pending[:0]
}

func (f *responsesSSEFramer) Flush(w io.Writer) {
	if len(f.pending) == 0 {
		return
	}
	if len(bytes.TrimSpace(f.pending)) == 0 {
		f.pending = f.pending[:0]
		return
	}
	if !responsesSSECanEmitWithoutDelimiter(f.pending) {
		f.pending = f.pending[:0]
		return
	}
	writeResponsesSSEChunk(w, f.pending)
	f.pending = f.pending[:0]
}

func responsesSSEFrameLen(chunk []byte) int {
	if len(chunk) == 0 {
		return 0
	}
	lf := bytes.Index(chunk, []byte("\n\n"))
	crlf := bytes.Index(chunk, []byte("\r\n\r\n"))
	switch {
	case lf < 0:
		if crlf < 0 {
			return 0
		}
		return crlf + 4
	case crlf < 0:
		return lf + 2
	case lf < crlf:
		return lf + 2
	default:
		return crlf + 4
	}
}

func responsesSSENeedsMoreData(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return false
	}
	return responsesSSEHasField(trimmed, []byte("event:")) && !responsesSSEHasField(trimmed, []byte("data:"))
}

func responsesSSEHasField(chunk []byte, prefix []byte) bool {
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, prefix) {
			return true
		}
	}
	return false
}

func responsesSSECanEmitWithoutDelimiter(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 || responsesSSENeedsMoreData(trimmed) || !responsesSSEHasField(trimmed, []byte("data:")) {
		return false
	}
	return responsesSSEDataLinesValid(trimmed)
}

func responsesSSEDataLinesValid(chunk []byte) bool {
	s := chunk
	for len(s) > 0 {
		line := s
		if i := bytes.IndexByte(s, '\n'); i >= 0 {
			line = s[:i]
			s = s[i+1:]
		} else {
			s = nil
		}
		line = bytes.TrimSpace(line)
		if len(line) == 0 || !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if !json.Valid(data) {
			return false
		}
	}
	return true
}

func responsesSSEHasRealOutput(chunk []byte) bool {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return false
	}
	dataPayloads := responsesSSEDataPayloads(trimmed)
	if len(dataPayloads) == 0 {
		return false
	}
	for _, data := range dataPayloads {
		if responsesSSEDataHasRealOutput(data) {
			return true
		}
	}
	return false
}

func responsesSSEDataHasRealOutput(data []byte) bool {
	if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
		return false
	}
	if !json.Valid(data) {
		return true
	}
	typeValue := strings.TrimSpace(gjson.GetBytes(data, "type").String())
	switch typeValue {
	case "error", "response.failed":
		return false
	case "response.created", "response.in_progress", "response.output_item.added":
		return false
	case "response.reasoning_summary_part.added", "response.content_part.added":
		return false
	}
	return typeValue != ""
}

func responsesSSEDataPayloads(chunk []byte) [][]byte {
	lines := bytes.Split(chunk, []byte("\n"))
	payloads := make([][]byte, 0, len(lines))
	for _, rawLine := range lines {
		line := bytes.TrimSpace(rawLine)
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payloads = append(payloads, bytes.TrimSpace(line[len("data:"):]))
	}
	return payloads
}

func responsesSSEStartErrorFromChunk(chunk []byte) (*interfaces.ErrorMessage, bool) {
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return nil, false
	}
	eventError := responsesSSEHasField(trimmed, []byte("event: error"))
	for _, data := range responsesSSEDataPayloads(trimmed) {
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) || !json.Valid(data) {
			continue
		}
		root := gjson.ParseBytes(data)
		eventType := strings.TrimSpace(root.Get("type").String())
		topLevelError := root.Get("error")
		responseError := root.Get("response.error")
		isError := eventError ||
			eventType == "error" ||
			eventType == "response.failed" ||
			(topLevelError.Exists() && topLevelError.Type != gjson.Null) ||
			(responseError.Exists() && responseError.Type != gjson.Null)
		if !isError {
			continue
		}
		message := firstResponsesStreamErrorText(root, data)
		if message == "" {
			message = strings.TrimSpace(string(data))
		}
		status := responsesStreamErrorStatusCode(root, data, message)
		return &interfaces.ErrorMessage{StatusCode: status, Error: fmt.Errorf("%s", message)}, true
	}
	return nil, false
}

func firstResponsesStreamErrorText(root gjson.Result, raw []byte) string {
	for _, path := range []string{"error.message", "message", "response.error.message"} {
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

func responsesStreamErrorStatusCode(root gjson.Result, raw []byte, message string) int {
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
		switch strings.TrimSpace(result.String()) {
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
	lower := strings.ToLower(strings.Join([]string{message, string(raw)}, " "))
	switch {
	case strings.Contains(lower, "concurrency limit exceeded"),
		strings.Contains(lower, "rate limit"),
		strings.Contains(lower, "rate_limit"),
		strings.Contains(lower, "too many requests"),
		strings.Contains(lower, "usage_limit"),
		strings.Contains(lower, "quota"):
		return http.StatusTooManyRequests
	case strings.Contains(lower, "invalid_api_key"),
		strings.Contains(lower, "unauthorized"):
		return http.StatusUnauthorized
	case strings.Contains(lower, "forbidden"),
		strings.Contains(lower, "permission"):
		return http.StatusForbidden
	case strings.Contains(lower, "not found"):
		return http.StatusNotFound
	case strings.Contains(lower, "timeout"),
		strings.Contains(lower, "timed out"):
		return http.StatusRequestTimeout
	default:
		return http.StatusInternalServerError
	}
}

func responsesSSEErrorStatus(errMsg *interfaces.ErrorMessage) int {
	status := http.StatusInternalServerError
	if errMsg != nil && errMsg.StatusCode > 0 {
		status = errMsg.StatusCode
	}
	return status
}

func responseStartErrorMessage(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return "authentication failed before the response started"
	case http.StatusForbidden:
		return "access denied before the response started"
	case http.StatusTooManyRequests:
		return "upstream unavailable before the response started"
	case http.StatusRequestTimeout:
		return "request timed out before the response started"
	default:
		if status >= http.StatusInternalServerError {
			return "upstream unavailable before the response started"
		}
		return http.StatusText(status)
	}
}

func (h *OpenAIResponsesAPIHandler) writeResponseStartError(c *gin.Context, errMsg *interfaces.ErrorMessage) {
	if isResponseStreamStartError(errMsg) {
		status := responsesSSEErrorStatus(errMsg)
		h.WriteErrorResponse(c, &interfaces.ErrorMessage{
			StatusCode: status,
			Error:      fmt.Errorf("%s", responseStartErrorMessage(status)),
		})
		return
	}
	h.WriteErrorResponse(c, errMsg)
}

func isResponseStreamStartError(errMsg *interfaces.ErrorMessage) bool {
	if errMsg == nil || errMsg.Error == nil {
		return false
	}
	if errMsg.StatusCode == 0 {
		return false
	}
	msg := strings.ToLower(errMsg.Error.Error())
	switch {
	case strings.Contains(msg, "stream disconnected before completion"):
		return true
	case strings.Contains(msg, "upstream stream error"):
		return true
	case strings.Contains(msg, "concurrency limit exceeded"):
		return true
	case strings.Contains(msg, "rate limit"):
		return true
	case strings.Contains(msg, "too many requests"):
		return true
	case strings.Contains(msg, "quota"):
		return true
	default:
		return errMsg.StatusCode == http.StatusTooManyRequests || errMsg.StatusCode >= http.StatusInternalServerError
	}
}

func responsesSSENeedsLineBreak(pending, chunk []byte) bool {
	if len(pending) == 0 || len(chunk) == 0 {
		return false
	}
	if bytes.HasSuffix(pending, []byte("\n")) || bytes.HasSuffix(pending, []byte("\r")) {
		return false
	}
	if chunk[0] == '\n' || chunk[0] == '\r' {
		return false
	}
	trimmed := bytes.TrimLeft(chunk, " \t")
	if len(trimmed) == 0 {
		return false
	}
	for _, prefix := range [][]byte{[]byte("data:"), []byte("event:"), []byte("id:"), []byte("retry:"), []byte(":")} {
		if bytes.HasPrefix(trimmed, prefix) {
			return true
		}
	}
	return false
}

// OpenAIResponsesAPIHandler contains the handlers for OpenAIResponses API endpoints.
// It holds a pool of clients to interact with the backend service.
type OpenAIResponsesAPIHandler struct {
	*handlers.BaseAPIHandler
}

// NewOpenAIResponsesAPIHandler creates a new OpenAIResponses API handlers instance.
// It takes an BaseAPIHandler instance as input and returns an OpenAIResponsesAPIHandler.
//
// Parameters:
//   - apiHandlers: The base API handlers instance
//
// Returns:
//   - *OpenAIResponsesAPIHandler: A new OpenAIResponses API handlers instance
func NewOpenAIResponsesAPIHandler(apiHandlers *handlers.BaseAPIHandler) *OpenAIResponsesAPIHandler {
	return &OpenAIResponsesAPIHandler{
		BaseAPIHandler: apiHandlers,
	}
}

// HandlerType returns the identifier for this handler implementation.
func (h *OpenAIResponsesAPIHandler) HandlerType() string {
	return OpenaiResponse
}

// Models returns the OpenAIResponses-compatible model metadata supported by this handler.
func (h *OpenAIResponsesAPIHandler) Models() []map[string]any {
	// Get dynamic models from the global registry
	modelRegistry := registry.GetGlobalRegistry()
	return modelRegistry.GetAvailableModels("openai")
}

// OpenAIResponsesModels handles the /v1/models endpoint.
// It returns a list of available AI models with their capabilities
// and specifications in OpenAIResponses-compatible format.
func (h *OpenAIResponsesAPIHandler) OpenAIResponsesModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data":   h.Models(),
	})
}

// Responses handles the /v1/responses endpoint.
// It determines whether the request is for a streaming or non-streaming response
// and calls the appropriate handler based on the model provider.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
func (h *OpenAIResponsesAPIHandler) Responses(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	// If data retrieval fails, return a 400 Bad Request error.
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	// Check if the client requested a streaming response.
	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		h.handleStreamingResponse(c, rawJSON)
	} else {
		h.handleNonStreamingResponse(c, rawJSON)
	}

}

func (h *OpenAIResponsesAPIHandler) Compact(c *gin.Context) {
	rawJSON, err := c.GetRawData()
	if err != nil {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: fmt.Sprintf("Invalid request: %v", err),
				Type:    "invalid_request_error",
			},
		})
		return
	}

	streamResult := gjson.GetBytes(rawJSON, "stream")
	if streamResult.Type == gjson.True {
		c.JSON(http.StatusBadRequest, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported for compact responses",
				Type:    "invalid_request_error",
			},
		})
		return
	}
	if streamResult.Exists() {
		if updated, err := sjson.DeleteBytes(rawJSON, "stream"); err == nil {
			rawJSON = updated
		}
	}

	c.Header("Content-Type", "application/json")
	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)
	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "responses/compact")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleNonStreamingResponse handles non-streaming chat completion responses
// for Gemini models. It selects a client from the pool, sends the request, and
// aggregates the response before sending it back to the client in OpenAIResponses format.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleNonStreamingResponse(c *gin.Context, rawJSON []byte) {
	c.Header("Content-Type", "application/json")

	modelName := gjson.GetBytes(rawJSON, "model").String()
	cliCtx, cliCancel := h.GetContextWithCancel(h, c, context.Background())
	stopKeepAlive := h.StartNonStreamingKeepAlive(c, cliCtx)

	resp, upstreamHeaders, errMsg := h.ExecuteWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
	stopKeepAlive()
	if errMsg != nil {
		h.WriteErrorResponse(c, errMsg)
		cliCancel(errMsg.Error)
		return
	}
	handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
	_, _ = c.Writer.Write(resp)
	cliCancel()
}

// handleStreamingResponse handles streaming responses for Gemini models.
// It establishes a streaming connection with the backend service and forwards
// the response chunks to the client in real-time using Server-Sent Events.
//
// Parameters:
//   - c: The Gin context containing the HTTP request and response
//   - rawJSON: The raw JSON bytes of the OpenAIResponses-compatible request
func (h *OpenAIResponsesAPIHandler) handleStreamingResponse(c *gin.Context, rawJSON []byte) {
	// Get the http.Flusher interface to manually flush the response.
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, handlers.ErrorResponse{
			Error: handlers.ErrorDetail{
				Message: "Streaming not supported",
				Type:    "server_error",
			},
		})
		return
	}

	modelName := gjson.GetBytes(rawJSON, "model").String()
	setSSEHeaders := func() {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("Access-Control-Allow-Origin", "*")
	}
	framer := &responsesSSEFramer{}
	startRetries := handlers.StreamingBootstrapRetries(h.Cfg)
	if startRetries <= 0 {
		startRetries = 1
	}
	attempts := 0
	var (
		cliCtx          context.Context
		cliCancel       handlers.APIHandlerCancelFunc
		dataChan        <-chan []byte
		upstreamHeaders http.Header
		errChan         <-chan *interfaces.ErrorMessage
		buffered        [][]byte
	)
	startAttempt := func() {
		cliCtx, cliCancel = h.GetContextWithCancel(h, c, context.Background())
		dataChan, upstreamHeaders, errChan = h.ExecuteStreamWithAuthManager(cliCtx, h.HandlerType(), modelName, rawJSON, "")
		buffered = buffered[:0]
	}
	retryStart := func(err error) bool {
		if attempts >= startRetries {
			return false
		}
		attempts++
		if cliCancel != nil {
			cliCancel(err)
		}
		startAttempt()
		return true
	}
	flushBuffered := func() {
		for _, chunk := range buffered {
			framer.WriteChunk(c.Writer, chunk)
		}
		buffered = nil
	}
	startAttempt()

	for {
		select {
		case <-c.Request.Context().Done():
			if cliCancel != nil {
				cliCancel(c.Request.Context().Err())
			}
			return
		case errMsg, ok := <-errChan:
			if !ok {
				// Err channel closed cleanly; wait for data channel.
				errChan = nil
				continue
			}
			// Upstream failed before a real Responses output frame reached the client.
			if isResponseStreamStartError(errMsg) && retryStart(errMsg.Error) {
				continue
			}
			h.writeResponseStartError(c, errMsg)
			if errMsg != nil {
				cliCancel(errMsg.Error)
			} else {
				cliCancel(nil)
			}
			return
		case chunk, ok := <-dataChan:
			if !ok {
				streamErr := fmt.Errorf("stream disconnected before completion: stream closed before response output")
				if retryStart(streamErr) {
					continue
				}
				errMsg := &interfaces.ErrorMessage{
					StatusCode: http.StatusRequestTimeout,
					Error:      streamErr,
				}
				h.writeResponseStartError(c, errMsg)
				cliCancel(streamErr)
				return
			}

			if errMsg, isErr := responsesSSEStartErrorFromChunk(chunk); isErr {
				if retryStart(errMsg.Error) {
					continue
				}
				h.writeResponseStartError(c, errMsg)
				cliCancel(errMsg.Error)
				return
			}
			buffered = append(buffered, append([]byte(nil), chunk...))
			if !responsesSSEHasRealOutput(chunk) {
				continue
			}

			setSSEHeaders()
			handlers.WriteUpstreamHeaders(c.Writer.Header(), upstreamHeaders)
			flushBuffered()
			flusher.Flush()

			h.forwardResponsesStream(c, flusher, func(err error) { cliCancel(err) }, dataChan, errChan, framer)
			return
		}
	}
}

func (h *OpenAIResponsesAPIHandler) forwardResponsesStream(c *gin.Context, flusher http.Flusher, cancel func(error), data <-chan []byte, errs <-chan *interfaces.ErrorMessage, framer *responsesSSEFramer) {
	if framer == nil {
		framer = &responsesSSEFramer{}
	}
	h.ForwardStream(c, flusher, cancel, data, errs, handlers.StreamForwardOptions{
		WriteChunk: func(chunk []byte) {
			framer.WriteChunk(c.Writer, chunk)
		},
		WriteTerminalError: func(errMsg *interfaces.ErrorMessage) {
			framer.Flush(c.Writer)
			if errMsg == nil {
				return
			}
			status := http.StatusInternalServerError
			if errMsg.StatusCode > 0 {
				status = errMsg.StatusCode
			}
			errText := http.StatusText(status)
			if errMsg.Error != nil && errMsg.Error.Error() != "" {
				errText = errMsg.Error.Error()
			}
			chunk := handlers.BuildOpenAIResponsesStreamErrorChunk(status, errText, 0)
			_, _ = fmt.Fprintf(c.Writer, "\nevent: error\ndata: %s\n\n", string(chunk))
		},
		WriteDone: func() {
			framer.Flush(c.Writer)
			_, _ = c.Writer.Write([]byte("\n"))
		},
	})
}
