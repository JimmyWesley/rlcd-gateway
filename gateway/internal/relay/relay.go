// Package relay holds the forwarding helpers every request path shares:
// header copy, credential masking, capped response capture, request ids,
// provider-shaped error bodies, and the streaming copy loop.
//
// The response is always passed to the client byte for byte as it arrives;
// relay only watches it on the side (for usage and the request log).
package relay

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxRequestBody bounds what the gateway reads from a client.
	MaxRequestBody = 64 << 20
	// MaxCapturedResponse is how much of a response is kept for the log;
	// the client always gets the whole response.
	MaxCapturedResponse = 4 << 20
)

// NewClient is the upstream HTTP client. It has no overall timeout: a
// streamed turn can legitimately run for minutes, and cancellation comes
// from the client's request context instead.
func NewClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ResponseHeaderTimeout: 10 * time.Minute,
		IdleConnTimeout:       90 * time.Second,
		ForceAttemptHTTP2:     true,
	}}
}

// skipHeaders are hop-by-hop headers plus those Go's transport must own.
// Dropping Accept-Encoding lets the transport negotiate gzip and hand us
// plain bytes, which we need to read usage out of the stream.
var skipHeaders = map[string]bool{
	"Connection": true, "Proxy-Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true, "Transfer-Encoding": true,
	"Upgrade": true, "Host": true, "Content-Length": true, "Accept-Encoding": true,
}

// CopyHeaders copies end-to-end headers from src to dst.
func CopyHeaders(dst, src http.Header) {
	for k, vs := range src {
		if skipHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// IsHopHeader reports whether a header is owned by the connection or the
// transport and must never be set from configuration.
func IsHopHeader(name string) bool { return skipHeaders[http.CanonicalHeaderKey(name)] }

// secretHeaders are masked before a record is stored. The account and
// organization ids are not credentials, but they identify the user.
var secretHeaders = map[string]bool{
	"Authorization": true, "X-Api-Key": true, "Api-Key": true, "Cookie": true,
	"Chatgpt-Account-Id": true, "Openai-Organization": true, "Openai-Project": true,
	"X-Rlcd-Key": true,
}

// IsSecretHeader reports whether a header carries a credential or an
// account identifier.
func IsSecretHeader(name string) bool { return secretHeaders[http.CanonicalHeaderKey(name)] }

// RedactHeaders flattens h for the log, masking credentials.
func RedactHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		v := strings.Join(vs, ", ")
		if secretHeaders[http.CanonicalHeaderKey(k)] {
			v = Mask(v)
		}
		out[k] = v
	}
	return out
}

// Mask keeps a short prefix, enough to tell an OAuth token from an API key
// ("Bearer sk-ant-oat0…", "Bearer eyJhbGciOi…"), and the length.
func Mask(v string) string {
	keep := 12
	if strings.HasPrefix(v, "Bearer ") {
		keep = 18
	}
	if len(v) <= keep {
		return strings.Repeat("•", len([]rune(v)))
	}
	return v[:keep] + "…(" + strconv.Itoa(len(v)) + " chars)"
}

// NewID is a request id: sortable time plus 8 random bytes.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

// Capped is a buffer that silently stops growing at Limit.
type Capped struct {
	bytes.Buffer
	Limit int
}

func (c *Capped) Write(p []byte) {
	if room := c.Limit - c.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		c.Buffer.Write(p)
	}
}

// ErrorMessage pulls a readable message out of a provider error body.
func ErrorMessage(b []byte) string {
	var e struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(b, &e) == nil {
		if e.Error.Message != "" {
			return e.Error.Message
		}
		if e.Detail != "" {
			return e.Detail
		}
	}
	s := string(b)
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

// Error types, as the providers name them.
const (
	ErrAPI            = "api_error"
	ErrAuthentication = "authentication_error"
	ErrPermission     = "permission_error"
	ErrRateLimit      = "rate_limit_error"
	ErrInvalidRequest = "invalid_request_error"
)

// WriteAnthropicError writes an error in the Anthropic Messages shape.
func WriteAnthropicError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": typ, "message": "rlcd-gateway: " + msg},
	})
}

// WriteOpenAIError writes an error in the OpenAI shape. OpenAI names
// authentication and rate-limit failures by code, under the
// invalid_request_error / requests types; SDKs map them from the status.
func WriteOpenAIError(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body := map[string]any{"type": typ, "message": "rlcd-gateway: " + msg}
	switch typ {
	case ErrAuthentication:
		body["type"], body["code"] = ErrInvalidRequest, "invalid_api_key"
	case ErrRateLimit:
		body["type"], body["code"] = "requests", "rate_limit_exceeded"
	case ErrPermission:
		body["type"], body["code"] = ErrInvalidRequest, "model_not_allowed"
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}

// Pump copies resp.Body to w as it arrives, flushing after every chunk,
// and hands each chunk to watch (which must not modify it). It returns a
// description of what went wrong, or "" when the stream ended cleanly.
func Pump(w http.ResponseWriter, body io.Reader, watch func([]byte)) string {
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 32<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := w.Write(chunk); werr != nil {
				return "client went away: " + werr.Error()
			}
			if flusher != nil {
				flusher.Flush()
			}
			watch(chunk)
		}
		if rerr != nil {
			if rerr != io.EOF && !errors.Is(rerr, context.Canceled) {
				return "upstream read: " + rerr.Error()
			}
			return ""
		}
	}
}

// IsSSE reports whether a response is an event stream.
func IsSSE(h http.Header) bool {
	return strings.HasPrefix(h.Get("Content-Type"), "text/event-stream")
}

// IsChatGPTLogin tells a ChatGPT subscription login from an OpenAI API key.
// Codex sends the login as a JWT access token plus ChatGPT-Account-ID;
// API keys start with "sk-".
func IsChatGPTLogin(h http.Header) bool {
	if h.Get("Chatgpt-Account-Id") != "" {
		return true
	}
	tok := strings.TrimSpace(strings.TrimPrefix(h.Get("Authorization"), "Bearer "))
	return strings.HasPrefix(tok, "eyJ")
}

// HasCredentials reports whether the client sent a provider login of its own.
func HasCredentials(h http.Header) bool {
	return h.Get("Authorization") != "" || h.Get("X-Api-Key") != "" || h.Get("Api-Key") != ""
}

// AuthMode describes the credential the client sent, as seen from the
// client: oauth (a subscription login), api-key, bearer or none.
func AuthMode(openAI bool, h http.Header) string {
	if openAI {
		if IsChatGPTLogin(h) {
			return "oauth"
		}
		if a := h.Get("Authorization"); a != "" {
			if strings.Contains(a, "sk-") {
				return "api-key"
			}
			return "bearer"
		}
		if h.Get("Api-Key") != "" || h.Get("X-Api-Key") != "" {
			return "api-key"
		}
		return "none"
	}
	if a := h.Get("Authorization"); a != "" {
		if strings.Contains(a, "sk-ant-oat") {
			return "oauth"
		}
		return "bearer"
	}
	if h.Get("X-Api-Key") != "" {
		return "api-key"
	}
	return "none"
}

// DecodeBody undoes request compression for parsing; the upstream gets the
// client's own bytes unless a stage changed them. Codex can compress
// requests with zstd, which the standard library cannot read.
func DecodeBody(encoding string, body []byte) ([]byte, error) {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "", "identity":
		return body, nil
	case "gzip":
		zr, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		defer zr.Close()
		return io.ReadAll(io.LimitReader(zr, MaxRequestBody))
	}
	return nil, errors.New("request body is " + encoding + "-compressed; X-ray, routing by content and pruning unavailable")
}
