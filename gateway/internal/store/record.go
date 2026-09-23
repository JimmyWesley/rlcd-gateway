package store

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"os"
	"reflect"

	"github.com/JimmyWesley/rlcd-gateway/gateway/internal/ir"
)

// Record files. A detail is written as requests/<id>.json.gz: one gzipped
// JSON document whose first members are a small head (the blobs it
// references and its sizes), so the janitor can read what a record keeps
// alive without decompressing the rest. The bodies are skeletons (body.go);
// everything else (headers, X-ray, stage details, response) is the Detail
// as before, compressed with the record.
//
// Files written before this format are requests/<id>.json, a plain Detail.
// Both are read; `rlcd-gateway storage compact` rewrites the old ones.

const (
	recordExt = ".json.gz"
	legacyExt = ".json"
	// recordFormat is the head's format number.
	recordFormat = 2
)

type recordDoc struct {
	Format int `json:"format"`
	// Refs lists every blob the record references, RefSizes their sizes
	// before compression.
	Refs     []string `json:"refs"`
	RefSizes []int    `json:"ref_sizes"`
	// BodyBytes is the length of the request and sent bodies as received.
	BodyBytes int `json:"body_bytes"`
	// RawBytes approximates the uncompressed legacy file.
	RawBytes int `json:"raw_bytes"`
	// XRayFromBody says the X-ray was left out because parsing the stored
	// request body gives it back exactly.
	XRayFromBody bool `json:"xray_from_body,omitempty"`
	// Detail holds everything but the bodies.
	Detail  *Detail `json:"detail"`
	Request *node   `json:"request_body,omitempty"`
	Sent    *node   `json:"sent_body,omitempty"`
}

// head is what the janitor reads of a record.
type head struct {
	Refs      []string
	RefSizes  []int
	BodyBytes int
	RawBytes  int
}

// encodeRecord splits d's bodies and returns the gzipped record and the
// blobs it needs.
func encodeRecord(d *Detail) ([]byte, []blob, error) {
	var sp splitter
	doc := recordDoc{Format: recordFormat, BodyBytes: len(d.RequestBody) + len(d.SentBody)}
	doc.Request = sp.splitBody(d.Protocol, []byte(d.RequestBody))
	doc.Sent = sp.splitBody(d.Protocol, []byte(d.SentBody))
	rest := *d
	rest.RequestBody, rest.SentBody = "", ""
	if d.XRay != nil && d.RequestBody != "" {
		// The X-ray is derived from the request body. Store it only when
		// parsing the body as it will read back does not reproduce it.
		if back, err := assemble(doc.Request, sp.get); err == nil {
			if x, err := ir.ParseFor(d.Protocol, []byte(back)); err == nil && reflect.DeepEqual(x, d.XRay) {
				rest.XRay, doc.XRayFromBody = nil, true
			}
		}
	}
	doc.Detail = &rest
	doc.Refs, doc.RefSizes = make([]string, len(sp.blobs)), make([]int, len(sp.blobs))
	for i, b := range sp.blobs {
		doc.Refs[i], doc.RefSizes[i] = b.hash, len(b.data)
	}
	restJSON, err := json.Marshal(&rest)
	if err != nil {
		return nil, nil, err
	}
	doc.RawBytes = len(restJSON) + doc.BodyBytes
	// No HTML escaping: inline leaves must read back as the client sent
	// them ("&&" stays "&&", not "\u0026\u0026").
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(&doc); err != nil {
		return nil, nil, err
	}
	return gzipBytes(bytes.TrimSuffix(buf.Bytes(), []byte("\n"))), sp.blobs, nil
}

// decodeRecord reads a record file and reassembles its bodies.
func (s *Store) decodeRecord(path string) (*Detail, error) {
	b, err := gunzipFile(path)
	if err != nil {
		return nil, err
	}
	var doc recordDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.Format != recordFormat || doc.Detail == nil {
		return nil, fmt.Errorf("unknown record format %d", doc.Format)
	}
	d := doc.Detail
	get := func(ref int) ([]byte, error) {
		if ref < 1 || ref > len(doc.Refs) {
			return nil, fmt.Errorf("record %s: bad blob reference %d", d.ID, ref)
		}
		return s.getBlob(doc.Refs[ref-1])
	}
	if d.RequestBody, err = assemble(doc.Request, get); err != nil {
		return nil, err
	}
	if d.SentBody, err = assemble(doc.Sent, get); err != nil {
		return nil, err
	}
	if doc.XRayFromBody {
		if d.XRay, err = ir.ParseFor(d.Protocol, []byte(d.RequestBody)); err != nil {
			return nil, err
		}
	}
	return d, nil
}

func assemble(n *node, get func(int) ([]byte, error)) (string, error) {
	if n == nil {
		return "", nil
	}
	var buf bytes.Buffer
	if err := n.assemble(&buf, get); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// readHead reads only the head of a record file.
func readHead(path string) (head, error) {
	var h head
	f, err := os.Open(path)
	if err != nil {
		return h, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return h, err
	}
	dec := json.NewDecoder(zr)
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return h, fmt.Errorf("record head: not an object")
	}
	format := 0
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return h, err
		}
		switch t {
		case "format":
			err = dec.Decode(&format)
		case "refs":
			err = dec.Decode(&h.Refs)
		case "ref_sizes":
			err = dec.Decode(&h.RefSizes)
		case "body_bytes":
			err = dec.Decode(&h.BodyBytes)
		case "raw_bytes":
			err = dec.Decode(&h.RawBytes)
		default:
			// The head ends where the detail starts.
			if format != recordFormat {
				return h, fmt.Errorf("unknown record format %d", format)
			}
			return h, nil
		}
		if err != nil {
			return h, err
		}
	}
	return h, nil
}
