package schema

import (
	"cmp"
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// How old a record is before a page holds it
// A record a slower process appends after a faster one is never skipped while its time is that close
const Settle = 10 * time.Second

// Where a page ended: the time and the record_id of the last record it held
type Cursor struct {
	Time     time.Time
	RecordID string
}

// The opaque form a page answers and the next call passes back
// The zero cursor is empty
func (c Cursor) String() string {
	if c == (Cursor{}) {
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
	at, id, ok := strings.Cut(string(b), ":")
	nanos, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || id == "" {
		return Cursor{}, fmt.Errorf("%w: %q", ErrCursorInvalid, s)
	}
	return Cursor{Time: time.Unix(0, nanos).UTC(), RecordID: id}, nil
}

// The cursor of the record
func (r Record) Cursor() Cursor {
	return Cursor{Time: r.Time, RecordID: r.ID}
}

// Records of one kind in any order
type Records []Record

// The records after the cursor by time then record_id and the cursor to pass next
// 1. only records at least Settle old so an append that lands late in the file is never behind the cursor
// 2. a limit of 0 holds every record
// 3. an empty page answers the cursor it was given
func (rs Records) Page(after Cursor, limit int, now time.Time) (Records, Cursor) {
	ordered := slices.Clone(rs)
	slices.SortFunc(ordered, func(a, b Record) int {
		return cmp.Or(a.Time.Compare(b.Time), cmp.Compare(a.ID, b.ID))
	})
	out := Records{}
	for _, r := range ordered {
		if r.Time.After(now.Add(-Settle)) || limit > 0 && len(out) == limit {
			break
		}
		if r.after(after) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return out, after
	}
	return out, out[len(out)-1].Cursor()
}

// Whether the record comes after the cursor in the order of a page
func (r Record) after(c Cursor) bool {
	return cmp.Or(r.Time.Compare(c.Time), cmp.Compare(r.ID, c.RecordID)) > 0
}
