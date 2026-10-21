package schema

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/record"
)

// The mark of the opaque form of a cursor of a position
// A cursor of a time starts with its digits
const positionMark = "@"

// Where a page ended
// 1. a page answers the position its store read up to so the next page reads on in the order the store appended records
// 2. a cursor of a time and a record_id starts after them by time as a digest names the last decision of a person
type Cursor struct {
	// Opaque to every reader but the store that answered it and empty for a cursor of a time
	Position string
	Time     time.Time
	RecordID string
}

// The opaque form of the cursor that starts after the record of the time and the record_id
// The func a composition root hands curate for the cursor of a digest
func CursorAt(at time.Time, recordID string) string {
	return Cursor{Time: at, RecordID: recordID}.String()
}

// The opaque form of the cursor that starts after a knowledge record by time
// The func a composition root hands curate for a digest whose last decision is a ledger record no trace names
func KnowledgeCursor(k record.Knowledge) string {
	// The record_id of a knowledge record is a digest of its fields and never fails
	id, _ := StoreKnowledge.recordID(keys{ID: k.ID, Version: k.Version, Status: k.Status, Time: k.Time})
	return CursorAt(k.Time, id)
}

// The opaque form a page answers and the next call passes back
// The zero cursor is empty
func (c Cursor) String() string {
	switch {
	case c.Position != "":
		return base64.RawURLEncoding.EncodeToString([]byte(positionMark + c.Position))
	case c.Time.IsZero():
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(c.Time.UnixNano(), 10) + ":" + c.RecordID))
}

// The cursor of its opaque form
// An empty form is the zero cursor that starts at the first record
func ParseCursor(s string) (Cursor, error) {
	if s == "" {
		return Cursor{}, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, fmt.Errorf("%w: %w", ErrCursorInvalid, err)
	}
	if position, ok := strings.CutPrefix(string(b), positionMark); ok && position != "" {
		return Cursor{Position: position}, nil
	}
	at, id, ok := strings.Cut(string(b), ":")
	nanos, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" {
		return Cursor{}, fmt.Errorf("%w: %q", ErrCursorInvalid, s)
	}
	return Cursor{Time: time.Unix(0, nanos).UTC(), RecordID: id}, nil
}

// Records of one kind
type Records []Record

// Whether the record comes after the cursor of a time by time then record_id
func (r Record) after(c Cursor) bool {
	return cmp.Or(r.Time.Compare(c.Time), cmp.Compare(r.ID, c.RecordID)) > 0
}
