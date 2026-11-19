// Package jsonx has the small helpers the API uses to read loosely typed
// JSON request bodies with the same rules as the original Python agent:
// booleans are never numbers, and integers must be written as integers.
package jsonx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// Object is a decoded JSON object. Numbers are json.Number.
type Object = map[string]any

// ErrNotObject is returned by Decode when the body isn't a JSON object.
var ErrNotObject = errors.New("body must be a JSON object")

// ErrNotJSON is returned by Decode when the body isn't JSON at all.
var ErrNotJSON = errors.New("body must be JSON")

// Decode parses data as a JSON object. An empty body is an empty object.
func Decode(data []byte) (Object, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Object{}, nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, ErrNotJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrNotJSON
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, ErrNotObject
	}
	return obj, nil
}

// Int reports v as an integer if it was written as one (not 1.0, not true).
func Int(v any) (int64, bool) {
	n, ok := v.(json.Number)
	if !ok || strings.ContainsAny(string(n), ".eE") {
		return 0, false
	}
	i, err := strconv.ParseInt(string(n), 10, 64)
	return i, err == nil
}

// Float reports v as a number (integer or not, never a bool).
func Float(v any) (float64, bool) {
	n, ok := v.(json.Number)
	if !ok {
		return 0, false
	}
	f, err := n.Float64()
	return f, err == nil
}

// Str is Python's str(value) for the simple values a body can hold, used
// where the agent accepts anything for a free-text field (label, source).
func Str(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case string:
		return t
	case bool:
		if t {
			return "True"
		}
		return "False"
	case json.Number:
		return string(t)
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}
