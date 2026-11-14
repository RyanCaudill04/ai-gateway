package upstream

import (
	"bytes"
	"encoding/json"
	"errors"
)

// rewriteBody sets `model` and `stream` in the client's JSON body and leaves
// every other member's value exactly as the client sent it (json.RawMessage
// is copied, not re-encoded), including fields this package doesn't know.
// Member order may change, which JSON doesn't care about.
//
// For a stream with addUsage set, stream_options.include_usage is added
// unless the client sent stream_options itself; added reports whether it was.
// For a non-stream request stream_options is removed.
func rewriteBody(body []byte, model string, stream, addUsage bool) (out []byte, added bool, err error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, false, errors.New("not a JSON object")
	}
	if fields == nil { // the body was `null`
		return nil, false, errors.New("not a JSON object")
	}
	if model == "" {
		return nil, false, errors.New("no provider model ID given")
	}

	fields["model"] = mustJSON(model)
	fields["stream"] = mustJSON(stream)
	if !stream {
		delete(fields, "stream_options")
	} else if _, ok := fields["stream_options"]; !ok && addUsage {
		fields["stream_options"] = json.RawMessage(`{"include_usage":true}`)
		added = true
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // don't turn the client's "<" into "<"
	if err := enc.Encode(fields); err != nil {
		return nil, false, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), added, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only called with strings and bools
	}
	return b
}
