package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ProtocolSystemOne is a System One decision call (POST /v1/systemone), the
// API TypeSafe's Jev and open-rlcd share. It is proxied and audited, never
// routed or pruned.
const ProtocolSystemOne = "systemone"

// Block kinds of a System One X-ray.
const (
	// KindState is the whole "state" object the questions are asked about.
	KindState = "state"
	// KindQuestion is one question: Key is its id, Name its type (choice,
	// score or noul) and Labels its criteria labels.
	KindQuestion = "question"
)

// ParseSystemOne builds the X-ray of a decision call: one block for the
// state (its size) and one per question, in the order the client sent them.
func ParseSystemOne(body []byte) (*Request, error) {
	var raw struct {
		Model     string          `json:"model"`
		State     json.RawMessage `json:"state"`
		Questions json.RawMessage `json:"questions"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse request: %w", err)
	}
	req := &Request{Model: raw.Model, ByKind: map[string]int{}}
	add := func(b Block) {
		b.Tokens = EstimateTokens(b.Chars)
		req.Blocks = append(req.Blocks, b)
		req.Tokens += b.Tokens
		req.ByKind[b.Kind] += b.Tokens
	}
	if len(raw.State) > 0 {
		s := string(raw.State)
		var str string
		if json.Unmarshal(raw.State, &str) == nil {
			s = str
		}
		add(Block{Key: "state", Kind: KindState, Msg: -1, Index: 0, Chars: len(s), Preview: preview(s)})
	}
	qs, err := OrderedMembers(raw.Questions)
	if err != nil {
		return req, nil // questions missing or not an object: the backend will say so
	}
	for i, q := range qs {
		var head struct {
			Type         string          `json:"type"`
			Instructions string          `json:"instructions"`
			Criteria     json.RawMessage `json:"criteria"`
		}
		_ = json.Unmarshal(q.Value, &head)
		add(Block{Key: q.Key, Kind: KindQuestion, Msg: -1, Index: i, Name: head.Type,
			Labels: CriteriaLabels(head.Criteria), Chars: len(q.Value), Preview: preview(head.Instructions)})
	}
	return req, nil
}

// CriteriaLabels lists a question's criteria labels: the keys of a choice's
// criteria object, or the items of a score's list, in order.
func CriteriaLabels(c json.RawMessage) []string {
	if len(c) == 0 {
		return nil
	}
	var list []any
	if json.Unmarshal(c, &list) == nil {
		out := make([]string, 0, len(list))
		for _, v := range list {
			if s, ok := v.(string); ok {
				out = append(out, s)
			} else {
				b, _ := json.Marshal(v)
				out = append(out, string(b))
			}
		}
		return out
	}
	ms, err := OrderedMembers(c)
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Key)
	}
	return out
}

// Member is one member of a JSON object.
type Member struct {
	Key   string
	Value json.RawMessage
}

// OrderedMembers decodes a JSON object's members in their original order.
func OrderedMembers(b []byte) ([]Member, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not a JSON object")
	}
	var out []Member
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, err
		}
		k, _ := kt.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		out = append(out, Member{Key: k, Value: v})
	}
	return out, nil
}
