package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// A body is stored as a skeleton: a small tree that mirrors the JSON
// document down to the level of its content blocks, whose leaves are either
// inline JSON (small values) or a reference to a content-addressed blob.
// Agents resend the whole history on every turn, so the blocks of turn N are
// mostly the blocks of turn N-1, and each is stored once.
//
// Splitting follows the protocol (ir.Protocol*):
//
//   - Anthropic Messages: each system block, each tool definition and each
//     content block of each message;
//   - OpenAI Chat Completions: each message and each tool definition;
//   - OpenAI Responses: each input item, the instructions and each tool.
//
// Reassembly writes the same values, arrays and order (object keys keep
// their original order too), but compacted: insignificant whitespace is not
// preserved, so the result is semantically identical JSON, not the same
// bytes. A body that is not a JSON object is kept whole, byte for byte, as a
// single blob.

// inlineMax is the largest leaf kept inside the skeleton. Tiny blocks cost
// more as separate files (a file system block each) than they save; the
// record is gzipped as a whole, so inline leaves are still compressed.
const inlineMax = 1024

// node is one element of a skeleton. Exactly one of R, J, A or O is set.
type node struct {
	// R references a blob: 1 + its index in the record's refs.
	R int `json:"r,omitempty"`
	// Raw marks a blob holding a non-JSON body, written back verbatim.
	Raw bool `json:"raw,omitempty"`
	// J is a small value kept inline.
	J json.RawMessage `json:"j,omitempty"`
	// A is an array split per element.
	A []node `json:"a,omitempty"`
	// O is an object split per key, in the original key order.
	O []member `json:"o,omitempty"`
}

type member struct {
	K string `json:"k"`
	V node   `json:"v"`
}

// blob is a leaf that goes to the blob store.
type blob struct {
	hash string
	data []byte
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// splitter builds a skeleton and collects the blobs it references, each
// once, in the order of first use.
type splitter struct {
	blobs []blob
	index map[string]int
}

// add returns the blob's reference (1 + its index).
func (s *splitter) add(data []byte) int {
	h := hashOf(data)
	if s.index == nil {
		s.index = map[string]int{}
	}
	if i, ok := s.index[h]; ok {
		return i + 1
	}
	s.index[h] = len(s.blobs)
	s.blobs = append(s.blobs, blob{hash: h, data: data})
	return len(s.blobs)
}

// get returns a blob collected so far, by reference.
func (s *splitter) get(ref int) ([]byte, error) {
	if ref < 1 || ref > len(s.blobs) {
		return nil, fmt.Errorf("bad blob reference %d", ref)
	}
	return s.blobs[ref-1].data, nil
}

// leaf stores one JSON value, inline when small.
func (s *splitter) leaf(raw json.RawMessage) node {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		buf.Reset()
		buf.Write(raw)
	}
	b := buf.Bytes()
	if len(b) < inlineMax {
		return node{J: json.RawMessage(b)}
	}
	return node{R: s.add(b)}
}

// rawBody stores a body that cannot be split, byte for byte.
func (s *splitter) rawBody(b []byte) *node {
	cp := append([]byte(nil), b...)
	return &node{R: s.add(cp), Raw: true}
}

// splitBody turns a body into a skeleton. An empty body has none.
func (s *splitter) splitBody(protocol string, body []byte) *node {
	if len(body) == 0 {
		return nil
	}
	top, err := objectMembers(body)
	if err != nil {
		return s.rawBody(body)
	}
	out := node{O: make([]member, 0, len(top))}
	for _, m := range top {
		out.O = append(out.O, member{K: m.k, V: s.field(protocol, m.k, m.v)})
	}
	if len(out.O) == 0 {
		return s.rawBody(body)
	}
	return &out
}

// field splits one top-level value according to the protocol.
func (s *splitter) field(protocol, key string, v json.RawMessage) node {
	switch protocol {
	case "", "anthropic-messages":
		switch key {
		case "system", "tools":
			return s.array(v, s.leaf)
		case "messages":
			return s.array(v, s.anthropicMessage)
		}
	case "openai-chat":
		switch key {
		case "messages", "tools", "functions":
			return s.array(v, s.leaf)
		}
	case "openai-responses":
		switch key {
		case "input", "tools":
			return s.array(v, s.leaf)
		case "instructions":
			return s.leaf(v)
		}
	}
	return s.leaf(v)
}

// array splits a non-empty array per element; anything else is a leaf.
func (s *splitter) array(v json.RawMessage, elem func(json.RawMessage) node) node {
	var items []json.RawMessage
	if json.Unmarshal(v, &items) != nil || len(items) == 0 {
		return s.leaf(v)
	}
	out := node{A: make([]node, len(items))}
	for i, it := range items {
		out.A[i] = elem(it)
	}
	return out
}

// anthropicMessage keeps the message's own keys and splits its content
// blocks, so a block keeps its hash whatever turn it is sent in.
func (s *splitter) anthropicMessage(v json.RawMessage) node {
	ms, err := objectMembers(v)
	if err != nil || len(ms) == 0 {
		return s.leaf(v)
	}
	out := node{O: make([]member, 0, len(ms))}
	for _, m := range ms {
		var n node
		if m.k == "content" {
			n = s.array(m.v, s.leaf)
		} else {
			n = s.leaf(m.v)
		}
		out.O = append(out.O, member{K: m.k, V: n})
	}
	return out
}

type rawMember struct {
	k string
	v json.RawMessage
}

// objectMembers decodes a JSON object's members in their original order.
func objectMembers(b []byte) ([]rawMember, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("not an object")
	}
	var out []rawMember
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
		out = append(out, rawMember{k, v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	// Nothing but whitespace may follow the object.
	if len(bytes.TrimSpace(b[dec.InputOffset():])) > 0 {
		return nil, fmt.Errorf("trailing data after the object")
	}
	return out, nil
}

// assemble writes the body back; get loads the blob a reference names.
func (n *node) assemble(buf *bytes.Buffer, get func(ref int) ([]byte, error)) error {
	switch {
	case n.R != 0:
		b, err := get(n.R)
		if err != nil {
			return err
		}
		buf.Write(b)
	case n.J != nil:
		buf.Write(n.J)
	case n.A != nil:
		buf.WriteByte('[')
		for i := range n.A {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := n.A[i].assemble(buf, get); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case n.O != nil:
		buf.WriteByte('{')
		for i := range n.O {
			if i > 0 {
				buf.WriteByte(',')
			}
			writeKey(buf, n.O[i].K)
			buf.WriteByte(':')
			if err := n.O[i].V.assemble(buf, get); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("empty skeleton node")
	}
	return nil
}

// writeKey writes a JSON string without HTML escaping, as clients send it.
func writeKey(buf *bytes.Buffer, k string) {
	enc := json.NewEncoder(buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(k)
	// Encode appends a newline.
	buf.Truncate(buf.Len() - 1)
}
