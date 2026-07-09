package executor

import (
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestCodexStreamErrorFromSSEResponseFailed(t *testing.T) {
	payload := []byte(`{"type":"response.failed","response":{"error":{"message":"Concurrency limit exceeded for user, please retry later"}}}`)

	err, ok := codexStreamErrorFromSSE("", payload)
	if !ok {
		t.Fatal("expected response.failed to be treated as stream error")
	}
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", err.StatusCode())
	}
	if err.Error() != "Concurrency limit exceeded for user, please retry later" {
		t.Fatalf("unexpected error message: %q", err.Error())
	}
}

func TestCodexStreamErrorFromSSEEventError(t *testing.T) {
	payload := []byte(`{"message":"too many requests"}`)

	err, ok := codexStreamErrorFromSSE("error", payload)
	if !ok {
		t.Fatal("expected event:error to be treated as stream error")
	}
	if err.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", err.StatusCode())
	}
}

func TestPatchCodexResponseMetadataSynthesizesMissingResponse(t *testing.T) {
	var state codexResponseMetadataState

	created, repaired := patchCodexResponseMetadata([]byte(`{"type":"response.created"}`), "response.created", "gpt-5.5", &state)
	if !repaired {
		t.Fatal("expected missing response metadata to be repaired")
	}
	createdID := gjson.GetBytes(created, "response.id").String()
	if !strings.HasPrefix(createdID, "resp_") {
		t.Fatalf("expected synthesized response id, got %q in %s", createdID, string(created))
	}
	if got := gjson.GetBytes(created, "response.status").String(); got != "in_progress" {
		t.Fatalf("expected created status in_progress, got %q", got)
	}
	if got := gjson.GetBytes(created, "response.model").String(); got != "gpt-5.5" {
		t.Fatalf("expected response model to be patched, got %q", got)
	}

	completed, repaired := patchCodexResponseMetadata([]byte(`{"type":"response.completed"}`), "response.completed", "gpt-5.5", &state)
	if !repaired {
		t.Fatal("expected missing completed metadata to be repaired")
	}
	if got := gjson.GetBytes(completed, "response.id").String(); got != createdID {
		t.Fatalf("expected completed to reuse response id %q, got %q", createdID, got)
	}
	if got := gjson.GetBytes(completed, "response.status").String(); got != "completed" {
		t.Fatalf("expected completed status, got %q", got)
	}
	if !gjson.GetBytes(completed, "response.output").IsArray() {
		t.Fatalf("expected completed response.output array, got %s", string(completed))
	}
}

func TestPatchCodexCompletedOutputKeepsSynthesizedMetadata(t *testing.T) {
	var state codexResponseMetadataState
	outputItemsByIndex := map[int64][]byte{
		1: []byte(`{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"OK"}]}`),
	}

	completed, repaired := patchCodexResponseMetadata([]byte(`{"type":"response.completed"}`), "response.completed", "gpt-5.5", &state)
	if !repaired {
		t.Fatal("expected missing completed metadata to be repaired")
	}
	completed = patchCodexCompletedOutput(completed, outputItemsByIndex, nil)

	if got := gjson.GetBytes(completed, "response.id").String(); !strings.HasPrefix(got, "resp_") {
		t.Fatalf("expected synthesized response id after output patch, got %q", got)
	}
	if got := gjson.GetBytes(completed, "response.output.0.id").String(); got != "msg_1" {
		t.Fatalf("expected collected output item in completed output, got %s", string(completed))
	}
}

func TestCodexNormalizeAndRejectMalformedEmptyEvents(t *testing.T) {
	data, eventType, repaired := codexNormalizeEventType("response.completed", []byte(`{}`))
	if !repaired {
		t.Fatal("expected event type to be repaired from SSE event name")
	}
	if eventType != "response.completed" {
		t.Fatalf("expected event type inferred from SSE event name, got %q", eventType)
	}
	if got := gjson.GetBytes(data, "type").String(); got != "response.completed" {
		t.Fatalf("expected normalized data type, got %q", got)
	}
	err := codexEmptyDataEventError("", []byte(`{}`))
	if err == nil {
		t.Fatal("expected blank event data object to be rejected")
	}
	if got := err.Error(); got != "codex upstream returned an empty SSE data event" {
		t.Fatalf("unexpected empty event error: %q", got)
	}
	if err := codexEmptyDataEventError("response.completed", data); err != nil {
		t.Fatalf("did not expect typed response.completed event to be rejected: %v", err)
	}
}
