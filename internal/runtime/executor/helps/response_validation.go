package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/downstreamtext"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

const (
	responseFormatStatusCode = http.StatusBadGateway
)

// ResponseFormatError marks a 2xx upstream response whose payload cannot be
// safely forwarded to the downstream client.
type ResponseFormatError struct {
	statusCode      int
	usageStatusCode int
	reason          string
	message         string
}

func NewResponseFormatError(usageStatusCode int, reason, message string) *ResponseFormatError {
	reason = strings.TrimSpace(reason)
	message = strings.TrimSpace(message)
	if message == "" {
		message = reason
	}
	if usageStatusCode <= 0 {
		usageStatusCode = http.StatusOK
	}
	return &ResponseFormatError{
		statusCode:      responseFormatStatusCode,
		usageStatusCode: usageStatusCode,
		reason:          reason,
		message:         message,
	}
}

func (e *ResponseFormatError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *ResponseFormatError) StatusCode() int {
	if e == nil || e.statusCode == 0 {
		return responseFormatStatusCode
	}
	return e.statusCode
}

func (e *ResponseFormatError) UsageStatusCode() int {
	if e == nil || e.usageStatusCode == 0 {
		return http.StatusOK
	}
	return e.usageStatusCode
}

func (e *ResponseFormatError) ErrorReason() string {
	if e == nil {
		return ""
	}
	return e.reason
}

func (e *ResponseFormatError) ErrorMessage() string {
	if e == nil {
		return ""
	}
	return e.message
}

func ValidateDownstreamNonStreamPayload(format sdktranslator.Format, provider string, payload []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return nil, newEmptyResponseError(provider)
	}
	if !expectsJSONPayload(format) {
		return payload, nil
	}
	if !json.Valid(trimmed) {
		return nil, newMalformedJSONError(provider)
	}
	return payload, nil
}

func ValidateDownstreamNonStreamPayloadWithOutputFilter(format sdktranslator.Format, provider string, payload []byte, cfg config.OutputFilterConfig) ([]byte, error) {
	validated, err := ValidateDownstreamNonStreamPayload(format, provider, payload)
	if err != nil {
		return nil, err
	}
	if matched, keyword := DownstreamTextMatchesFilterForProvider(format, provider, validated, cfg); matched {
		return nil, newFilteredOutputError(keyword)
	}
	return validated, nil
}

func ValidateDownstreamStreamChunk(format sdktranslator.Format, chunk []byte) error {
	if !expectsStreamValidation(format) {
		return nil
	}
	lines := bytes.Split(chunk, []byte("\n"))
	for _, rawLine := range lines {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if !json.Valid(data) {
			return NewResponseFormatError(http.StatusOK, "upstream_invalid_sse_json", "upstream returned invalid SSE JSON")
		}
	}
	return nil
}

func ValidateDownstreamStreamChunkWithOutputFilter(format sdktranslator.Format, chunk []byte, cfg config.OutputFilterConfig) error {
	return ValidateDownstreamStreamChunkWithOutputFilterForProvider(format, "", chunk, cfg)
}

func ValidateDownstreamStreamChunkWithOutputFilterForProvider(format sdktranslator.Format, provider string, chunk []byte, cfg config.OutputFilterConfig) error {
	if err := ValidateDownstreamStreamChunk(format, chunk); err != nil {
		return err
	}
	if !expectsStreamValidation(format) {
		return nil
	}
	lines := bytes.Split(chunk, []byte("\n"))
	for _, rawLine := range lines {
		line := bytes.TrimSpace(rawLine)
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if matched, keyword := DownstreamTextMatchesFilterForProvider(format, provider, data, cfg); matched {
			return newFilteredOutputError(keyword)
		}
	}
	return nil
}

func DownstreamTextMatchesFilter(format sdktranslator.Format, output []byte, cfg config.OutputFilterConfig) (bool, string) {
	return DownstreamTextMatchesFilterForProvider(format, "", output, cfg)
}

func DownstreamTextMatchesFilterForProvider(format sdktranslator.Format, provider string, output []byte, cfg config.OutputFilterConfig) (bool, string) {
	text, ok := ExtractDownstreamText(format, output)
	if !ok {
		return false, ""
	}
	return OutputMatchesFilterForProvider([]byte(text), provider, cfg)
}

func ExtractDownstreamText(format sdktranslator.Format, output []byte) (string, bool) {
	return downstreamtext.Extract(format, output)
}

func OutputMatchesFilter(output []byte, cfg config.OutputFilterConfig) (bool, string) {
	return outputMatchesFilterRule(output, cfg.GlobalRule())
}

func OutputMatchesFilterForProvider(output []byte, provider string, cfg config.OutputFilterConfig) (bool, string) {
	if matched, keyword := outputMatchesFilterRule(output, cfg.GlobalRule()); matched {
		return true, keyword
	}
	rule, ok := outputFilterRuleForProvider(provider, cfg.Providers)
	if !ok {
		return false, ""
	}
	return outputMatchesFilterRule(output, rule)
}

func outputMatchesFilterRule(output []byte, rule config.OutputFilterRule) (bool, string) {
	if !rule.Enabled || rule.MaxLength <= 0 {
		return false, ""
	}
	trimmed := bytes.TrimSpace(output)
	if len(trimmed) == 0 || len(trimmed) >= rule.MaxLength {
		return false, ""
	}
	outputText := string(trimmed)
	for _, keyword := range rule.Keywords {
		keyword = strings.TrimSpace(keyword)
		if keyword == "" {
			continue
		}
		expression, err := regexp.Compile("(?i:" + keyword + ")")
		if err != nil {
			continue
		}
		if expression.MatchString(outputText) {
			return true, keyword
		}
	}
	return false, ""
}

func outputFilterRuleForProvider(provider string, providers map[string]config.OutputFilterRule) (config.OutputFilterRule, bool) {
	provider = strings.TrimSpace(provider)
	if provider == "" || len(providers) == 0 {
		return config.OutputFilterRule{}, false
	}
	if rule, ok := providers[provider]; ok {
		return rule, true
	}
	for configuredProvider, rule := range providers {
		if strings.EqualFold(strings.TrimSpace(configuredProvider), provider) {
			return rule, true
		}
	}
	return config.OutputFilterRule{}, false
}

func OutputFilterFromConfig(cfg *config.Config) config.OutputFilterConfig {
	if cfg == nil {
		return config.OutputFilterConfig{}
	}
	return cfg.OutputFilter
}

func expectsJSONPayload(format sdktranslator.Format) bool {
	switch format {
	case sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatClaude,
		sdktranslator.FormatGemini,
		sdktranslator.FormatCodex,
		sdktranslator.FormatAntigravity:
		return true
	default:
		return true
	}
}

func expectsStreamValidation(format sdktranslator.Format) bool {
	switch format {
	case sdktranslator.FormatOpenAI,
		sdktranslator.FormatOpenAIResponse,
		sdktranslator.FormatClaude,
		sdktranslator.FormatGemini,
		sdktranslator.FormatCodex,
		sdktranslator.FormatAntigravity:
		return true
	default:
		return true
	}
}

func newEmptyResponseError(provider string) *ResponseFormatError {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return NewResponseFormatError(http.StatusOK, "upstream_empty_response", "upstream returned an empty response")
	}
	return NewResponseFormatError(http.StatusOK, "upstream_empty_response", fmt.Sprintf("%s upstream returned an empty response", provider))
}

func newMalformedJSONError(provider string) *ResponseFormatError {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return NewResponseFormatError(http.StatusOK, "upstream_malformed_json", "upstream returned malformed JSON")
	}
	return NewResponseFormatError(http.StatusOK, "upstream_malformed_json", fmt.Sprintf("%s upstream returned malformed JSON", provider))
}

func newFilteredOutputError(keyword string) *ResponseFormatError {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return NewResponseFormatError(http.StatusOK, "upstream_filtered_output", "upstream output matched the configured output filter")
	}
	return NewResponseFormatError(http.StatusOK, "upstream_filtered_output", fmt.Sprintf("upstream output matched output filter keyword %q", keyword))
}
