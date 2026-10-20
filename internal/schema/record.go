package schema

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// One record as every transport sends it
type Record struct {
	Kind string
	// The id of a trace and else the digest of the key its store names
	ID   string
	Time time.Time
	// The stored record with record_kind and record_id first and schema_version always present
	JSON json.RawMessage
}

// The fields of a stored record its record_id derives from
type keys struct {
	ID        string    `json:"id"`
	TraceID   string    `json:"trace_id"`
	Time      time.Time `json:"time"`
	Version   int       `json:"version"`
	Status    string    `json:"status"`
	SessionID string    `json:"session_id"`
	Tool      string    `json:"tool"`
	// The veto id of a guard entry and the veto rule of a knowledge record
	Veto json.RawMessage `json:"veto"`
	// Nil on a record of an earlier release
	SchemaVersion *int `json:"schema_version"`
}

// The stored record as every transport sends it
// 1. record_kind and record_id come first so a reader of any kind finds them in one place
// 2. a stored record without schema_version reads as version 1 since that was the shape before the field
// The stored kind field of a knowledge record is the kind of the item so the record kind needs a name of its own
func (k Kind) Record(stored any) (Record, error) {
	b, err := json.Marshal(stored)
	if err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrRecordInvalid, err)
	}
	var line bytes.Buffer
	if err := json.Compact(&line, b); err != nil || !bytes.HasPrefix(line.Bytes(), []byte("{")) {
		return Record{}, fmt.Errorf("%w: a %s record is no JSON object", ErrRecordInvalid, k.Name)
	}
	var ks keys
	if err := json.Unmarshal(line.Bytes(), &ks); err != nil {
		return Record{}, fmt.Errorf("%w: %w", ErrRecordInvalid, err)
	}
	id, err := k.Store.recordID(ks)
	if err != nil {
		return Record{}, err
	}
	// Two strings always encode
	head, _ := json.Marshal(struct {
		Kind string `json:"record_kind"`
		ID   string `json:"record_id"`
	}{k.Name, id})
	body := object(line.Bytes())
	if ks.SchemaVersion == nil {
		body = body.firstVersion()
	}
	return Record{Kind: k.Name, ID: id, Time: ks.Time, JSON: slices.Concat(head[:len(head)-1], []byte(","), []byte(body[1:]))}, nil
}

// One stored record as compact JSON
type object []byte

// The object with schema_version 1 appended
func (o object) firstVersion() object {
	if bytes.Equal(o, []byte("{}")) {
		return object(`{"schema_version":1}`)
	}
	return append(o[:len(o)-1:len(o)-1], []byte(`,"schema_version":1}`)...)
}

// The record_id of a stored record
// 1. a trace keeps its own id
// 2. every other store hashes a key of the fields that tell its records apart
// 3. that key is the one its OTLP evaluation derives from where one exists so both name a record alike
func (s Store) recordID(k keys) (string, error) {
	at := strconv.FormatInt(k.Time.UnixNano(), 10)
	switch s {
	case StoreTraces:
		if k.ID == "" {
			return "", fmt.Errorf("%w: a trace without an id", ErrRecordInvalid)
		}
		return k.ID, nil
	case StoreVerdicts:
		return key("verdict:" + k.TraceID + ":" + at).digest(), nil
	case StoreOutcomes:
		return key("outcome:" + k.TraceID + ":" + at).digest(), nil
	case StoreKnowledge:
		return key("decision:" + k.ID + ":" + strconv.Itoa(k.Version) + ":" + k.Status + ":" + at).digest(), nil
	case StoreGuard:
		var veto string
		if err := json.Unmarshal(k.Veto, &veto); err != nil {
			return "", fmt.Errorf("%w: the veto of a guard entry: %w", ErrRecordInvalid, err)
		}
		return key("guard:" + k.SessionID + ":" + at + ":" + k.Tool + ":" + veto).digest(), nil
	}
	return "", fmt.Errorf("%w: %q", ErrStoreUnknown, s)
}

// The key a record_id hashes
type key string

// The hex of the first 16 bytes of SHA-256 of nodloop: and the key
func (k key) digest() string {
	sum := sha256.Sum256([]byte("nodloop:" + k))
	return hex.EncodeToString(sum[:16])
}
