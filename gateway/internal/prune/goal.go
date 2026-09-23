package prune

import (
	"encoding/json"
	"strings"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

// ConversationGoal is the pruner's goal for a request in any protocol: the
// latest turns human texts (oldest first, joined by "\n---\n"), skipping
// harness-injected <system-reminder> and <command-…> blocks, and a short
// summary of the most recent tool calls. turns < 1 means the default (2).
//
// It is exported for the router's decision rules, so "what the person
// asked" means the same thing to routing and to pruning.
func ConversationGoal(protocol string, body []byte, turns int) (goal, recent string, err error) {
	if protocol == ir.ProtocolOpenAIResponses {
		// A plain-string input is the whole conversation: one user turn.
		var head struct {
			Input json.RawMessage `json:"input"`
		}
		if json.Unmarshal(body, &head) == nil {
			var s string
			if json.Unmarshal(head.Input, &s) == nil {
				return strings.TrimSpace(headTail(s, 1200, 300)), "", nil
			}
		}
	}
	if protocol == "" {
		protocol = ir.ProtocolAnthropic
	}
	d, err := parseDialect(protocol, body)
	if err != nil {
		return "", "", err
	}
	goal, recent = d.goalAndRecent(turns)
	return goal, recent, nil
}
