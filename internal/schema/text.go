package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The record without the free text of its kind
// 1. every text path of the index is left out so ids, enums, scores and hashes alone follow the flow
// 2. a path goes through arrays as if each element stood alone
// 3. the fields that stay keep their order so record_kind and record_id still come first
func (k Kind) Strip(r Record) (Record, error) {
	out := r.JSON
	for _, path := range k.Text {
		var err error
		if out, err = without(out, strings.Split(path, ".")); err != nil {
			return Record{}, fmt.Errorf("%w: %s %s: %w", ErrRecordInvalid, k.Name, r.ID, err)
		}
	}
	r.JSON = out
	return r, nil
}

// The JSON value without the field the path names
// A value that is no object or array holds no field and stays as it is
func without(raw json.RawMessage, path []string) (json.RawMessage, error) {
	switch first(raw) {
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, err
		}
		for i, item := range items {
			var err error
			if items[i], err = without(item, path); err != nil {
				return nil, err
			}
		}
		// Raw values always encode
		b, _ := json.Marshal(items)
		return b, nil
	case '{':
		ms, err := decodeMembers(raw)
		if err != nil {
			return nil, err
		}
		return ms.without(path)
	}
	return raw, nil
}

func first(raw []byte) byte {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

// One field of a JSON object with its value as written
type member struct {
	key   string
	value json.RawMessage
}

// The fields of a JSON object in the order they are written
type members []member

func decodeMembers(raw []byte) (members, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	open, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if open != json.Delim('{') {
		return nil, errNoObject
	}
	var out members
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		// A token in the place of a key of an object is always its string
		key, _ := tok.(string)
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		out = append(out, member{key: key, value: value})
	}
	return out, nil
}

// The object without the field the path names below the first of its keys
func (ms members) without(path []string) (json.RawMessage, error) {
	kept := make(members, 0, len(ms))
	for _, m := range ms {
		switch {
		case m.key != path[0]:
		case len(path) == 1:
			continue
		default:
			var err error
			if m.value, err = without(m.value, path[1:]); err != nil {
				return nil, err
			}
		}
		kept = append(kept, m)
	}
	return kept.encode(), nil
}

func (ms members) encode() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range ms {
		if i > 0 {
			b.WriteByte(',')
		}
		// A string always encodes
		key, _ := json.Marshal(m.key)
		b.Write(key)
		b.WriteByte(':')
		b.Write(m.value)
	}
	b.WriteByte('}')
	return b.Bytes()
}

// One top level field of a published schema with its own schema as written
type Property struct {
	Name   string
	Schema json.RawMessage
}

// The top level fields of the file of the version in the order the file lists them
// A table of the records keeps the order a reader of the file sees
func (k Kind) Properties(version int) ([]Property, error) {
	b, err := k.File(version)
	if err != nil {
		return nil, err
	}
	file, err := decodeMembers(b)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %w", ErrIndexInvalid, FileName(k.Name, version), err)
	}
	var props members
	for _, m := range file {
		if m.key == "properties" {
			if props, err = decodeMembers(m.value); err != nil {
				return nil, fmt.Errorf("%w: %s: %w", ErrIndexInvalid, FileName(k.Name, version), err)
			}
		}
	}
	out := make([]Property, 0, len(props))
	for _, p := range props {
		out = append(out, Property{Name: p.key, Schema: p.value})
	}
	return out, nil
}
