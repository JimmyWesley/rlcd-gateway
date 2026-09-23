package resilience

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// The incident: OpenRouter → GMICloud, no max_tokens, GMICloud defaulted it
// to the whole window. Captured from OpenRouter (the input count differs
// from the chatbot's 193; the shape is the same): the provider's error is
// a JSON string in metadata.raw, whose error.details is another JSON string.
const gmiCloudOverflow = `{"error":{"message":"Provider returned error","code":400,"metadata":{"raw":"{\"error\":{\"message\":\"Backend request failed with status 400\",\"type\":\"backend_error\",\"code\":400,\"details\":\"{\\\"error\\\":{\\\"message\\\":\\\"Requested token count exceeds the model's maximum context length of 131072 tokens. You requested a total of 131265 tokens: 193 tokens from the input messages and 131072 tokens for the completion. Please reduce the number of tokens in the input messages or the completion to fit within the limit.\\\",\\\"type\\\":\\\"BadRequest\\\"},\\\"request_id\\\":\\\"req-z45v35-1790169494999043114\\\"}\"}}","provider_name":"GMICloud","is_byok":false,"provider_error_code":"400"}},"user_id":"user_x"}`

// Captured too: an explicit max_tokens above what GMICloud allows.
const gmiCloudMaxTokens = `{"error":{"message":"Provider returned error","code":400,"metadata":{"raw":"{\"error\":{\"message\":\"Backend request failed with status 400\",\"type\":\"backend_error\",\"code\":400,\"details\":\"{\\\"error\\\":{\\\"message\\\":\\\"max_completion_tokens is too large: 235923.This model supports at most 131072 completion tokens.\\\",\\\"type\\\":\\\"BadRequest\\\"},\\\"request_id\\\":\\\"req-kq8pph-1790169487215515578\\\"}\"}}","provider_name":"GMICloud","is_byok":false,"provider_error_code":"400"}},"user_id":"user_x"}`

func TestClassifyGMICloudIncident(t *testing.T) {
	f := Classify(400, nil, []byte(gmiCloudOverflow))
	if f.Class != ClassOutputTooLarge {
		t.Fatalf("class %q", f.Class)
	}
	if !strings.HasPrefix(f.Message, "Requested token count exceeds the model's maximum context length of 131072 tokens.") {
		t.Fatalf("provider message not unwrapped: %q", f.Message)
	}
	if f.Outer != "Provider returned error" || f.UpstreamProvider != "GMICloud" || f.Type != "BadRequest" {
		t.Fatalf("outer %q provider %q type %q", f.Outer, f.UpstreamProvider, f.Type)
	}
	if f.Window != 131072 || f.Input != 193 || f.Output != 131072 {
		t.Fatalf("numbers: window %d input %d output %d", f.Window, f.Input, f.Output)
	}
	// Clamping to what fits fixes it.
	if got := ClampTarget(f, Limits{}, 0, 256, 131072); got <= 0 || got > 131072-193 {
		t.Fatalf("clamp target %d", got)
	}

	f = Classify(400, nil, []byte(gmiCloudMaxTokens))
	if f.Class != ClassOutputTooLarge || f.MaxOutput != 131072 || f.Output != 235923 {
		t.Fatalf("max tokens error: %+v", f)
	}
	if got := ClampTarget(f, Limits{}, 0, 256, 235923); got != 131072 {
		t.Fatalf("clamp target %d", got)
	}
}

func TestClassifyShapes(t *testing.T) {
	h := func(kv ...string) http.Header {
		out := http.Header{}
		for i := 0; i+1 < len(kv); i += 2 {
			out.Set(kv[i], kv[i+1])
		}
		return out
	}
	cases := []struct {
		name   string
		status int
		h      http.Header
		body   string
		class  string
		msg    string
		check  func(t *testing.T, f Failure)
	}{
		{"anthropic 529", 529, nil, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`,
			ClassOverloaded, "Overloaded", nil},
		{"openai 429 retry-after", 429, h("Retry-After", "7"),
			`{"error":{"message":"Rate limit reached for gpt-4.1 in organization org-x on tokens per min (TPM): Limit 30000. Please try again in 1.2s.","type":"tokens","param":null,"code":"rate_limit_exceeded"}}`,
			ClassRateLimited, "Rate limit reached", func(t *testing.T, f Failure) {
				if f.RetryAfter != 7*time.Second {
					t.Errorf("retry after %v", f.RetryAfter)
				}
			}},
		{"429 message wait", 429, nil, `{"error":{"message":"Please try again in 350ms.","code":"rate_limit_exceeded"}}`,
			ClassRateLimited, "", func(t *testing.T, f Failure) {
				if f.RetryAfter != 350*time.Millisecond {
					t.Errorf("retry after %v", f.RetryAfter)
				}
			}},
		{"openrouter 429 provider", 429, h("Retry-After-Ms", "1500"),
			`{"error":{"message":"Provider returned error","code":429,"metadata":{"raw":"qwen/qwen3-235b-a22b:free is temporarily rate-limited upstream. Please retry shortly.","provider_name":"Chutes"}}}`,
			ClassRateLimited, "temporarily rate-limited upstream", func(t *testing.T, f Failure) {
				if f.UpstreamProvider != "Chutes" || f.RetryAfter != 1500*time.Millisecond {
					t.Errorf("%+v", f)
				}
			}},
		{"503", 503, nil, `service unavailable`, ClassOverloaded, "service unavailable", nil},
		{"openrouter 502", 502, nil, `{"error":{"message":"Provider returned error","code":502,"metadata":{"raw":"upstream connect error","provider_name":"Flaky"}}}`,
			ClassProviderError, "upstream connect error", nil},
		{"500", 500, nil, `{"type":"error","error":{"type":"api_error","message":"Internal server error"}}`, ClassProviderError, "Internal server error", nil},
		{"401", 401, nil, `{"error":{"message":"No auth credentials found","code":401}}`, ClassAuth, "No auth credentials found", nil},
		{"403", 403, nil, `{"type":"error","error":{"type":"permission_error","message":"not allowed"}}`, ClassAuth, "", nil},
		{"openai context", 400, nil, `{"error":{"message":"This model's maximum context length is 128000 tokens. However, your messages resulted in 130211 tokens. Please reduce the length of the messages.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`,
			ClassContextOverflow, "", func(t *testing.T, f Failure) {
				if f.Window != 128000 || f.Input != 130211 {
					t.Errorf("%+v", f)
				}
			}},
		{"anthropic prompt too long", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 215001 tokens > 200000 maximum"}}`,
			ClassContextOverflow, "", func(t *testing.T, f Failure) {
				if f.Window != 200000 || f.Input != 215001 {
					t.Errorf("%+v", f)
				}
			}},
		{"anthropic input + max_tokens", 400, nil, "{\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"input length and `max_tokens` exceed context limit: 188240 + 21333 > 200000, decrease input length or `max_tokens` and try again\"}}",
			ClassOutputTooLarge, "", func(t *testing.T, f Failure) {
				if f.Window != 200000 || f.Input != 188240 || f.Output != 21333 {
					t.Errorf("%+v", f)
				}
			}},
		{"anthropic max_tokens", 400, nil, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 100000 > 64000, which is the maximum allowed number of output tokens for claude-sonnet-4-5-20250929"}}`,
			ClassOutputTooLarge, "", func(t *testing.T, f Failure) {
				if f.MaxOutput != 64000 {
					t.Errorf("%+v", f)
				}
			}},
		{"openrouter own window", 400, nil, `{"error":{"message":"This endpoint's maximum context length is 131072 tokens. However, you requested about 140000 tokens (9000 of text input, 131000 in the output). Please reduce the length of either one, or use the \"middle-out\" transform to compress your prompt automatically.","code":400}}`,
			ClassOutputTooLarge, "", func(t *testing.T, f Failure) {
				if f.Window != 131072 || f.Input != 9000 || f.Output != 131000 {
					t.Errorf("%+v", f)
				}
			}},
		{"openai max_tokens value", 400, nil, `{"error":{"message":"Invalid 'max_tokens': integer above maximum value. Expected a value <= 16384, but got 50000 instead.","type":"invalid_request_error","param":"max_tokens","code":"integer_above_max_value"}}`,
			ClassOutputTooLarge, "", func(t *testing.T, f Failure) {
				if f.MaxOutput != 16384 {
					t.Errorf("%+v", f)
				}
			}},
		{"unknown model", 400, nil, `{"error":{"message":"qwen/does-not-exist is not a valid model ID","code":400},"user_id":"u"}`,
			ClassModelUnavailable, "not a valid model ID", nil},
		{"404", 404, nil, `{"error":{"message":"No endpoints found for x/y.","code":404}}`, ClassModelUnavailable, "", nil},
		{"bad request", 400, nil, `{"error":{"message":"messages: at least one message is required","type":"invalid_request_error"}}`,
			ClassBadRequest, "at least one message", nil},
		{"402", 402, nil, `{"error":{"message":"Insufficient credits","code":402}}`, ClassBadRequest, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Classify(c.status, c.h, []byte(c.body))
			if f.Class != c.class {
				t.Fatalf("class %q, want %q (%+v)", f.Class, c.class, f)
			}
			if c.msg != "" && !strings.Contains(f.Message, c.msg) {
				t.Fatalf("message %q", f.Message)
			}
			if c.check != nil {
				c.check(t, f)
			}
		})
	}
}

func TestClassifyStreamEvents(t *testing.T) {
	f, ok := ClassifyStreamEvent("error", []byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	if !ok || f.Class != ClassOverloaded || f.Status != 529 {
		t.Fatalf("anthropic stream error: %v %+v", ok, f)
	}
	f, ok = ClassifyStreamEvent("", []byte(`{"id":"x","error":{"message":"Provider returned error","code":502,"metadata":{"raw":"boom","provider_name":"Flaky"}}}`))
	if !ok || f.Class != ClassProviderError || f.UpstreamProvider != "Flaky" {
		t.Fatalf("openrouter stream error: %v %+v", ok, f)
	}
	f, ok = ClassifyStreamEvent("response.failed", []byte(`{"type":"response.failed","response":{"error":{"code":"rate_limit_exceeded","message":"slow down"}}}`))
	if !ok || f.Class != ClassRateLimited {
		t.Fatalf("responses failure: %v %+v", ok, f)
	}
	if _, ok := ClassifyStreamEvent("message_start", []byte(`{"type":"message_start","message":{}}`)); ok {
		t.Fatal("message_start is not an error")
	}
	if _, ok := ClassifyStreamEvent("", []byte(`{"choices":[{"delta":{"content":"hi"}}],"error":null}`)); ok {
		t.Fatal("a content chunk is not an error")
	}
}

func TestClassifyTransport(t *testing.T) {
	if f := ClassifyTransport(errString("dial tcp: connection refused")); f.Class != ClassProviderError {
		t.Fatalf("%+v", f)
	}
}

type errString string

func (e errString) Error() string { return string(e) }
