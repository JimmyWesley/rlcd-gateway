package resilience

import (
	"bytes"
	"io"
	"strings"
	"time"
)

// Peek is the start of an event stream, read before anything is written to
// the client so that an error announced at the start can still be retried.
type Peek struct {
	// Buffered is every byte read so far; the client gets it first.
	Buffered []byte
	// Failure is set when the first meaningful event is an error.
	Failure *Failure
	// Rest reads the remainder of the stream after Buffered.
	Rest io.Reader
	// Committed is true when a content event (or a timeout, a size limit,
	// or the end of the stream) ended the peek: the stream is good to relay.
	Committed bool
	// Why says what ended the peek, for the record.
	Why string
}

// preamble events carry no content: a provider may still fail after them
// (Anthropic can send overloaded_error right after message_start).
var preamble = map[string]bool{
	"message_start": true, "ping": true,
	"response.created": true, "response.in_progress": true,
}

type chunk struct {
	b   []byte
	err error
}

// chanReader continues a stream whose reads happen in a goroutine.
type chanReader struct {
	ch  <-chan chunk
	cur []byte
	err error
}

func (c *chanReader) Read(p []byte) (int, error) {
	for len(c.cur) == 0 {
		if c.err != nil {
			return 0, c.err
		}
		ch, ok := <-c.ch
		if !ok {
			c.err = io.EOF
			continue
		}
		c.cur, c.err = ch.b, ch.err
	}
	n := copy(p, c.cur)
	c.cur = c.cur[n:]
	return n, nil
}

// PeekStream reads body until the first event with content, an error
// event, maxBytes, the end of the stream, or wait. The returned stop must
// be called once the caller is done with the stream (after closing it).
func PeekStream(body io.Reader, maxBytes int, wait time.Duration) (Peek, func()) {
	ch := make(chan chunk, 16)
	stop := make(chan struct{})
	go func() {
		defer close(ch)
		for {
			buf := make([]byte, 32<<10)
			n, err := body.Read(buf)
			if n > 0 || err != nil {
				select {
				case ch <- chunk{buf[:n], err}:
				case <-stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	var once bool
	stopFn := func() {
		if !once {
			once = true
			close(stop)
		}
	}
	rest := &chanReader{ch: ch}
	p := Peek{Rest: rest}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	scanned := 0
	for {
		select {
		case c, ok := <-ch:
			if !ok {
				p.Committed, p.Why = true, "end of stream"
				return p, stopFn
			}
			p.Buffered = append(p.Buffered, c.b...)
			if c.err != nil {
				// Hand the error to whoever reads Rest next.
				rest.err = c.err
				p.Committed, p.Why = true, "end of stream"
				if c.err != io.EOF {
					p.Why = "read error"
				}
				// A stream that ended during the peek may still hold an
				// error event: look at what arrived.
				if f := scanEvents(p.Buffered, &scanned, true); f.done {
					p.Failure = f.failure
				}
				return p, stopFn
			}
			if f := scanEvents(p.Buffered, &scanned, false); f.done {
				p.Failure, p.Committed = f.failure, f.failure == nil
				p.Why = "content"
				if f.failure != nil {
					p.Why = "error event"
				}
				return p, stopFn
			}
			if len(p.Buffered) >= maxBytes {
				p.Committed, p.Why = true, "peek size limit"
				return p, stopFn
			}
		case <-timer.C:
			p.Committed, p.Why = true, "peek time limit"
			return p, stopFn
		}
	}
}

type scanResult struct {
	done    bool
	failure *Failure
}

// scanEvents looks at complete events from *from on. It is done at the
// first error event (failure set) or content event (failure nil).
func scanEvents(buf []byte, from *int, final bool) scanResult {
	for {
		rest := buf[*from:]
		end, sep := eventEnd(rest)
		if end < 0 {
			if final && len(bytes.TrimSpace(rest)) > 0 {
				end, sep = len(rest), 0
			} else {
				return scanResult{}
			}
		}
		block := rest[:end]
		*from += end + sep
		event, data := parseEvent(block)
		if data == "" && event == "" {
			if *from >= len(buf) {
				return scanResult{}
			}
			continue // comment or keep-alive
		}
		if f, isErr := ClassifyStreamEvent(event, []byte(data)); isErr {
			return scanResult{done: true, failure: &f}
		}
		if preamble[event] || preamble[eventType(data)] {
			if *from >= len(buf) {
				return scanResult{}
			}
			continue
		}
		return scanResult{done: true}
	}
}

func eventEnd(b []byte) (int, int) {
	i := bytes.Index(b, []byte("\n\n"))
	j := bytes.Index(b, []byte("\r\n\r\n"))
	switch {
	case i < 0 && j < 0:
		return -1, 0
	case j >= 0 && (i < 0 || j < i):
		return j, 4
	}
	return i, 2
}

func parseEvent(block []byte) (event, data string) {
	var datas []string
	for _, line := range strings.Split(strings.ReplaceAll(string(block), "\r\n", "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			datas = append(datas, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return event, strings.Join(datas, "\n")
}

// eventType reads "type" from an event's JSON data cheaply.
func eventType(data string) string {
	i := strings.Index(data, `"type"`)
	if i < 0 {
		return ""
	}
	s := data[i+6:]
	s = strings.TrimLeft(s, " \t:")
	if !strings.HasPrefix(s, `"`) {
		return ""
	}
	s = s[1:]
	if j := strings.IndexByte(s, '"'); j >= 0 {
		return s[:j]
	}
	return ""
}
