package upstream

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
)

// maxEvent caps one SSE event. Chunks are normally tiny; a big tool-call
// argument can be large, but not this large.
const maxEvent = 4 << 20

// Stream is an open streaming response. Read it with Next until it returns an
// error, then Close it. It is not safe for concurrent use.
type Stream struct {
	Status int
	Header http.Header // upstream response headers, e.g. x-ratelimit-*
	// UsageAdded is true when Stream added stream_options.include_usage to the
	// request (the client didn't ask). The provider's final chunk then has
	// `"choices": []` and a `usage` object; a caller can keep the counts and
	// skip forwarding that chunk.
	UsageAdded bool

	req    Request
	ctx    context.Context // this stream's context (child of parent)
	parent context.Context // the caller's context
	cancel context.CancelCauseFunc
	body   io.ReadCloser
	events *eventReader
}

// Next returns the data of the next SSE event: one chat.completion.chunk JSON
// object, exactly as the provider sent it. The caller forwards it to its own
// client as "data: " + data + "\n\n". Comments (keep-alives such as
// OpenRouter's ": OPENROUTER PROCESSING") and event/id fields are skipped.
//
// At the end of the stream ("data: [DONE]", or the provider closing the body
// cleanly) Next returns io.EOF; the caller then writes its own
// "data: [DONE]\n\n".
//
// Some providers (OpenRouter, Gemini) answer 200 and then report a failure as
// an event with a top-level "error" member. Next returns that event's data
// together with an *Error whose InStream is true, classified like an HTTP
// error (OpenRouter's error.code carries the real status, e.g. 429). If the
// caller hasn't forwarded anything yet it can fall back; otherwise it can pass
// the data on so its client sees the error. The stream is over either way.
//
// If the connection breaks mid-stream Next returns a KindProvider *Error with
// InStream set; if the caller's context is done it returns the context's error.
func (s *Stream) Next() ([]byte, error) {
	data, err := s.events.next()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		if s.parent.Err() != nil {
			return nil, fmt.Errorf("upstream %s: %w", s.req.Provider, s.parent.Err())
		}
		e := s.req.errorf(KindProvider, s.Status, s.Header, err, "stream interrupted: %s", redact(err.Error(), s.req.APIKey))
		e.InStream = true
		return nil, e
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return nil, io.EOF
	}
	if d, ok := parseInlineError(data); ok {
		return data, s.req.classify(s.Status, d.effectiveStatus(s.Status), s.Header, d, true)
	}
	return data, nil
}

// Close stops the upstream request and releases the connection. Closing
// before the end of the stream is fine (and is how a caller abandons it).
func (s *Stream) Close() error {
	s.cancel(nil)
	return s.body.Close()
}

// eventReader splits a text/event-stream body into events' data fields, per
// the SSE spec: lines are "field: value", a blank line ends an event, and
// several data lines in one event are joined with "\n".
type eventReader struct {
	r *bufio.Reader
}

func newEventReader(r io.Reader) *eventReader {
	return &eventReader{r: bufio.NewReaderSize(r, 64<<10)}
}

func (er *eventReader) next() ([]byte, error) {
	var data []byte
	hasData := false
	for {
		line, err := er.readLine()
		if err != nil {
			// A last event without its trailing blank line still counts.
			if errors.Is(err, io.EOF) && hasData {
				return data, nil
			}
			return nil, err
		}
		if len(line) == 0 {
			if hasData {
				return data, nil
			}
			continue // blank line after a comment-only or id-only event
		}
		if line[0] == ':' {
			continue // comment / keep-alive
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		if string(field) != "data" {
			continue // event:, id:, retry: don't matter for chat completions
		}
		if hasData {
			data = append(data, '\n')
		}
		data = append(data, value...)
		hasData = true
		if len(data) > maxEvent {
			return nil, fmt.Errorf("event larger than %d bytes", maxEvent)
		}
	}
}

// readLine returns one line without its "\n" or "\r\n" ending, bounded by
// maxEvent. (SSE also allows a bare "\r"; no provider sends that.)
func (er *eventReader) readLine() ([]byte, error) {
	var line []byte
	for {
		frag, err := er.r.ReadSlice('\n')
		line = append(line, frag...)
		if len(line) > maxEvent {
			return nil, fmt.Errorf("line larger than %d bytes", maxEvent)
		}
		if err == nil {
			break
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) && len(line) > 0 {
			break // final line with no newline
		}
		return nil, err
	}
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	return line, nil
}
