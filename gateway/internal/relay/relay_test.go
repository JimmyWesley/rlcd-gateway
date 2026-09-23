package relay

import (
	"testing"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

func TestSSEFailureEvent(t *testing.T) {
	s := NewUsageReader(ir.ProtocolOpenAIResponses)
	s.Feed([]byte("event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"overloaded\"},\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}"))
	u, _, failure := s.Result() // no trailing blank line
	if failure != "overloaded" || u == nil || u.InputTokens != 5 {
		t.Errorf("failure %q usage %+v", failure, u)
	}
}

func TestMaskKeepsPrefixOnly(t *testing.T) {
	if m := Mask("Bearer sk-ant-oat01-secretsecret"); m != "Bearer sk-ant-oat0…(32 chars)" {
		t.Errorf("bearer mask %q", m)
	}
	if m := Mask("short"); m != "•••••" {
		t.Errorf("short mask %q", m)
	}
}
