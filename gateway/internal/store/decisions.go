package store

// Decisions is the compact summary of a System One decision call (protocol
// "systemone"), kept on the record so the audit API can filter and count
// decisions without opening details.
type Decisions struct {
	// Backend is the decision backend that served the call.
	Backend string `json:"backend"`
	// Source is who asked: "client" (an app calling /v1/systemone), or the
	// gateway itself: "prune" or "router".
	Source string `json:"source"`
	// ParentID is the request whose handling made an internal call.
	ParentID string `json:"parent_id,omitempty"`
	Think    bool   `json:"think,omitempty"`
	// StateBytes is the size of the "state" JSON.
	StateBytes int `json:"state_bytes"`
	// ForwardMs and TotalMs are what the backend reports (open-rlcd's
	// x-rlcd-forward-ms and x-rlcd-total-ms headers): the model's compute
	// and the time inside the server.
	ForwardMs *float64 `json:"forward_ms,omitempty"`
	TotalMs   *float64 `json:"total_ms,omitempty"`
	// Questions are in the order the client sent them.
	Questions []DecisionQuestion `json:"questions"`
}

// DecisionQuestion is one question and its answer.
type DecisionQuestion struct {
	ID   string `json:"id"`
	Type string `json:"type"` // choice | score | noul
	// Labels are the criteria labels (choice keys, score legend).
	Labels []string `json:"labels,omitempty"`
	// Answer is the decision as a string, for filtering and display: the
	// choice, "yes"/"no" for noul (noul >= 0.5), or the score's legend label
	// at the rounded score. Empty when the backend returned no answer.
	Answer string   `json:"answer,omitempty"`
	Choice string   `json:"choice,omitempty"`
	Score  *float64 `json:"score,omitempty"`
	Noul   *float64 `json:"noul,omitempty"`
	// Confidence is the backend's own, or max(noul, 1-noul) for noul (which
	// has none), as the Open-RLCD labs compute it.
	Confidence *float64 `json:"confidence,omitempty"`
	// TopProb is the highest of the answer's probabilities (max(noul,
	// 1-noul) for noul).
	TopProb *float64 `json:"top_prob,omitempty"`
}
