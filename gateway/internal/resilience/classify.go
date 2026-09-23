package resilience

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Failure classes.
const (
	ClassContextOverflow  = "context_overflow"  // the input alone does not fit the window
	ClassOutputTooLarge   = "output_too_large"  // max_tokens is above what the model allows
	ClassRateLimited      = "rate_limited"      // 429
	ClassOverloaded       = "overloaded"        // 529, 503
	ClassProviderError    = "provider_error"    // other 5xx, or no response at all
	ClassModelUnavailable = "model_unavailable" // 404, unknown model id, no endpoint serves it
	ClassAuth             = "auth"              // 401, 403
	ClassBadRequest       = "bad_request"       // other 4xx
	ClassUnknown          = "unknown"
)

// Classes lists every class, for the stats and the dashboard.
var Classes = []string{ClassContextOverflow, ClassOutputTooLarge, ClassRateLimited, ClassOverloaded,
	ClassProviderError, ClassModelUnavailable, ClassAuth, ClassBadRequest, ClassUnknown}

// Failure is an upstream error, classified.
type Failure struct {
	Class  string `json:"class"`
	Status int    `json:"status"`
	// Message is the provider's own message: the most deeply nested one
	// (OpenRouter wraps the provider's error in metadata.raw, which may wrap
	// another JSON document in details). Outer is the top-level message
	// when it differs ("Provider returned error").
	Message string `json:"message"`
	Outer   string `json:"outer,omitempty"`
	// Type is the provider's error type (overloaded_error, BadRequest, ...).
	Type string `json:"type,omitempty"`
	// UpstreamProvider is OpenRouter's metadata.provider_name.
	UpstreamProvider string `json:"upstream_provider,omitempty"`
	// RetryAfter is what the provider asked to wait (Retry-After,
	// retry-after-ms, or "try again in 20s" in the message).
	RetryAfter time.Duration `json:"retry_after_ns,omitempty"`
	// Token numbers read from the message; 0 when absent.
	Window    int `json:"window,omitempty"`
	Input     int `json:"input,omitempty"`
	Output    int `json:"output,omitempty"`
	MaxOutput int `json:"max_output,omitempty"`
}

// minUsefulOutput is the smallest output budget worth sending: below it
// the request is treated as a context overflow, not a max_tokens problem.
const minUsefulOutput = 16

// Classify reads an upstream error response.
func Classify(status int, h http.Header, body []byte) Failure {
	f := Failure{Status: status}
	var w walker
	var v any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if dec.Decode(&v) == nil {
		w.walk(v, 0)
	}
	f.Type, f.UpstreamProvider = w.typ, w.provider
	f.Message, f.Outer = w.best()
	if f.Message == "" {
		f.Message = strings.TrimSpace(string(body))
		if len(f.Message) > 500 {
			f.Message = f.Message[:500]
		}
	}
	if f.Status == 0 {
		f.Status = w.code
	}
	f.RetryAfter = retryAfter(h, strings.Join(w.msgs, "\n"))
	f.classify(strings.Join(w.msgs, "\n"))
	return f
}

// ClassifyTransport classifies a call that got no response.
func ClassifyTransport(err error) Failure {
	f := Failure{Class: ClassProviderError, Message: err.Error()}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		f.Message = "upstream timed out: " + err.Error()
	}
	return f
}

// ClassifyStreamEvent classifies an error event that arrived in an event
// stream before any content (status is the stream's HTTP status, 200).
func ClassifyStreamEvent(event string, data []byte) (Failure, bool) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if dec.Decode(&m) != nil {
		if event == "error" {
			f := Failure{Status: 0, Message: strings.TrimSpace(string(data))}
			f.classify(f.Message)
			return f, true
		}
		return Failure{}, false
	}
	typ, _ := m["type"].(string)
	var errPart any
	switch {
	case m["error"] != nil:
		errPart = m
	case typ == "error" || event == "error":
		errPart = m
	case typ == "response.failed":
		if resp, _ := m["response"].(map[string]any); resp != nil && resp["error"] != nil {
			errPart = map[string]any{"error": resp["error"]}
		}
	}
	if errPart == nil {
		return Failure{}, false
	}
	var w walker
	w.walk(errPart, 0)
	f := Failure{Type: w.typ, UpstreamProvider: w.provider, Status: w.code}
	f.Message, f.Outer = w.best()
	if f.Status == 0 {
		f.Status = statusForType(w.typ)
	}
	f.classify(strings.Join(w.msgs, "\n"))
	return f, true
}

// statusForType maps a provider error type to the status it stands for.
func statusForType(t string) int {
	switch strings.ToLower(t) {
	case "overloaded_error", "overloaded", "server_is_overloaded":
		return 529
	case "rate_limit_error", "rate_limit_exceeded", "requests", "tokens":
		return 429
	case "api_error", "server_error", "internal_server_error":
		return 500
	case "authentication_error":
		return 401
	case "permission_error":
		return 403
	case "not_found_error":
		return 404
	case "invalid_request_error", "context_length_exceeded", "request_too_large":
		return 400
	}
	return 0
}

var (
	reWindow = []*regexp.Regexp{
		regexp.MustCompile(`maximum context length (?:is|of) (\d+)`),
		regexp.MustCompile(`context (?:window|length|limit)(?: of| is)? (\d+)`),
		regexp.MustCompile(`\d+ \+ \d+ > (\d+)`),
		regexp.MustCompile(`tokens > (\d+) maximum`),
		regexp.MustCompile(`maximum number of tokens allowed \((\d+)\)`),
	}
	reInput = []*regexp.Regexp{
		regexp.MustCompile(`(\d+) tokens from the input`),
		regexp.MustCompile(`\((\d+) in the messages`),
		regexp.MustCompile(`\((\d+) of text input`),
		regexp.MustCompile(`messages resulted in (\d+) tokens`),
		regexp.MustCompile(`prompt is too long: (\d+) tokens`),
		regexp.MustCompile(`input token count \((\d+)\)`),
		regexp.MustCompile(`(\d+) \+ \d+ > \d+`),
	}
	reOutput = []*regexp.Regexp{
		regexp.MustCompile(`(\d+) tokens for the completion`),
		regexp.MustCompile(`(\d+) in the completion`),
		regexp.MustCompile(`(\d+) in the output`),
		regexp.MustCompile(`\d+ \+ (\d+) > \d+`),
		regexp.MustCompile(`max_(?:completion_|output_)?tokens(?: is too large)?: (\d+)`),
	}
	reMaxOut = []*regexp.Regexp{
		regexp.MustCompile(`supports at most (\d+) (?:completion|output) tokens`),
		regexp.MustCompile(`> (\d+), which is the maximum allowed number of output tokens`),
		regexp.MustCompile(`max(?:imum)? (?:completion|output) tokens(?: is| of)? (\d+)`),
		regexp.MustCompile("must be (?:less than or equal to|at most|<=) `?(\\d+)"),
		regexp.MustCompile(`expected a value <= (\d+)`),
	}
	// A message about the context window at all.
	reContext = regexp.MustCompile(`context length|context window|context limit|context_length_exceeded|prompt is too long|` +
		`too many (?:input )?tokens|input is too long|input too long|reduce the length|exceeds the maximum number of tokens|` +
		`input token count|request too large for model|maximum context`)
	// A message about the output limit.
	reOutputParam = regexp.MustCompile(`max_tokens|max_completion_tokens|max_output_tokens|output tokens|completion tokens`)
	reOutputBad   = regexp.MustCompile(`too large|exceeds?|at most|maximum allowed|must be|greater than|> \d+|less than or equal`)
	reModel       = regexp.MustCompile(`not a valid model|model_not_found|model not found|no endpoints found|unknown model|` +
		`model .* does not exist|does not exist or you do not have access|no allowed providers are available`)
)

func firstInt(res []*regexp.Regexp, s string) int {
	for _, re := range res {
		if m := re.FindStringSubmatch(s); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				return n
			}
		}
	}
	return 0
}

func (f *Failure) classify(text string) {
	low := strings.ToLower(text)
	f.Window, f.Input, f.Output, f.MaxOutput = firstInt(reWindow, low), firstInt(reInput, low),
		firstInt(reOutput, low), firstInt(reMaxOut, low)
	status := f.Status
	if status == 0 {
		status = statusForType(f.Type)
	}
	typ := strings.ToLower(f.Type)
	tokenish := status == 0 || status == 400 || status == 413 || status == 422
	switch {
	case status == 429 || typ == "rate_limit_error" || typ == "rate_limit_exceeded":
		f.Class = ClassRateLimited
	case status == 401 || status == 403 || typ == "authentication_error" || typ == "permission_error":
		f.Class = ClassAuth
	case tokenish && (reContext.MatchString(low) || typ == "context_length_exceeded"):
		f.Class = ClassContextOverflow
		// The window was exceeded by input + max_tokens. When the input
		// alone fits with room for an answer, it is max_tokens that is too
		// large, and clamping it fixes the call.
		switch {
		case f.Window > 0 && f.Input > 0 && f.Input+minUsefulOutput <= f.Window:
			f.Class = ClassOutputTooLarge
		case f.Window > 0 && f.Input == 0 && f.Output > 0 && f.Output >= f.Window/2:
			f.Class = ClassOutputTooLarge
		}
	case tokenish && reOutputParam.MatchString(low) && reOutputBad.MatchString(low):
		f.Class = ClassOutputTooLarge
	case status == 529 || status == 503 || typ == "overloaded_error" || typ == "overloaded":
		f.Class = ClassOverloaded
	case status == 404 || (status >= 400 && status < 500 && reModel.MatchString(low)):
		f.Class = ClassModelUnavailable
	case status >= 500:
		f.Class = ClassProviderError
	case status >= 400:
		f.Class = ClassBadRequest
	default:
		f.Class = ClassUnknown
	}
}

// walker collects the messages of a nested error document, outermost first.
type walker struct {
	msgs     []string
	depths   []int
	typ      string
	provider string
	code     int
}

// generic messages say nothing the nested one does not.
var genericMsg = regexp.MustCompile(`(?i)^(provider returned error|backend request failed with status \d+|upstream error|internal server error)$`)

// best returns the most specific message (the deepest non-generic one) and
// the outermost one when it differs.
func (w *walker) best() (msg, outer string) {
	bestDepth := -1
	for i, m := range w.msgs {
		d := w.depths[i]
		if genericMsg.MatchString(strings.TrimSpace(m)) {
			d -= 1000
		}
		if d > bestDepth || msg == "" {
			msg, bestDepth = m, d
		}
	}
	if len(w.msgs) > 0 && w.msgs[0] != msg {
		outer = w.msgs[0]
	}
	return msg, outer
}

func (w *walker) push(s string, depth int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	for _, m := range w.msgs {
		if m == s {
			return
		}
	}
	w.msgs = append(w.msgs, s)
	w.depths = append(w.depths, depth)
}

// nested parses a string that holds another JSON document.
func nested(s string) (any, bool) {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "{") && !strings.HasPrefix(t, "[") {
		return nil, false
	}
	var v any
	dec := json.NewDecoder(strings.NewReader(t))
	dec.UseNumber()
	if dec.Decode(&v) != nil {
		return nil, false
	}
	return v, true
}

func (w *walker) walk(v any, depth int) {
	if depth > 16 {
		return
	}
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			w.walk(e, depth+1)
		}
	case string:
		if n, ok := nested(x); ok {
			w.walk(n, depth+1)
			return
		}
		w.push(x, depth)
	case map[string]any:
		// Fixed key order: the outer message first, then what it wraps.
		if s, ok := x["message"].(string); ok {
			if n, ok := nested(s); ok {
				w.walk(n, depth+1)
			} else {
				w.push(s, depth)
			}
		}
		if t, ok := x["type"].(string); ok && t != "error" && t != "" {
			w.typ = t // the innermost type wins
		}
		switch c := x["code"].(type) {
		case json.Number:
			if n, err := c.Int64(); err == nil && n >= 100 && n < 600 && w.code == 0 {
				w.code = int(n)
			}
		case string:
			if w.typ == "" || w.typ == "invalid_request_error" {
				if statusForType(c) != 0 {
					w.typ = c
				}
			}
			if n, err := strconv.Atoi(c); err == nil && n >= 100 && n < 600 && w.code == 0 {
				w.code = n
			}
		}
		if e, ok := x["error"]; ok {
			w.walk(e, depth+1)
		}
		for _, k := range []string{"detail", "details"} {
			if d, ok := x[k]; ok {
				w.walk(d, depth+1)
			}
		}
		if md, ok := x["metadata"].(map[string]any); ok {
			if p, ok := md["provider_name"].(string); ok && w.provider == "" {
				w.provider = p
			}
			if raw, ok := md["raw"]; ok {
				w.walk(raw, depth+1)
			}
		}
	}
}

var reTryAgain = regexp.MustCompile(`(?i)try again in (\d+(?:\.\d+)?)\s*(ms|s|sec|seconds|m|min)\b`)

// maxRetryAfter bounds a provider's request: waits beyond the time budget
// are not taken anyway.
const maxRetryAfter = 10 * time.Minute

func retryAfter(h http.Header, text string) time.Duration {
	if h != nil {
		if v := h.Get("Retry-After-Ms"); v != "" {
			if ms, err := strconv.ParseFloat(v, 64); err == nil && ms >= 0 {
				return clampWait(time.Duration(ms * float64(time.Millisecond)))
			}
		}
		if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
			if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
				return clampWait(time.Duration(s * float64(time.Second)))
			}
			if t, err := http.ParseTime(v); err == nil {
				return clampWait(time.Until(t))
			}
		}
	}
	if m := reTryAgain.FindStringSubmatch(text); m != nil {
		n, _ := strconv.ParseFloat(m[1], 64)
		unit := time.Second
		switch strings.ToLower(m[2]) {
		case "ms":
			unit = time.Millisecond
		case "m", "min":
			unit = time.Minute
		}
		return clampWait(time.Duration(n * float64(unit)))
	}
	return 0
}

func clampWait(d time.Duration) time.Duration {
	if d < 0 {
		return 0
	}
	if d > maxRetryAfter {
		return maxRetryAfter
	}
	return d
}
