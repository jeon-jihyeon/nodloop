package file

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/evidence"
)

const (
	columnEventID   = "event_id"
	columnTimestamp = "timestamp"
	columnMetric    = "metric"
	columnValue     = "value"
	columnContext   = "change_context"
)

var eventColumns = []string{columnEventID, columnTimestamp, columnMetric, columnValue}

// One csv file positioned after its header
// Header driven so column order is free
type table struct {
	name      string
	reader    *csv.Reader
	positions map[string]int
}

func readTable(name string, r io.Reader, required []string) (table, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	header, err := cr.Read()
	if err != nil {
		return table{}, fmt.Errorf("%w: %s missing header: %w", evidence.ErrMalformed, name, err)
	}
	positions := make(map[string]int, len(header))
	for i, h := range header {
		positions[strings.TrimSpace(h)] = i
	}
	for _, c := range required {
		if _, ok := positions[c]; !ok {
			return table{}, fmt.Errorf("%w: %s missing column %q", evidence.ErrMalformed, name, c)
		}
	}
	return table{name: name, reader: cr, positions: positions}, nil
}

// Calls visit with every record after the header and the line it starts on
// The reader reports the line so a quoted field spanning lines keeps later numbers right
func (t table) each(visit func(line int, rec []string) error) error {
	for {
		rec, err := t.reader.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		var parseErr *csv.ParseError
		if errors.As(err, &parseErr) {
			return fmt.Errorf("%w: %s line %d: %w", evidence.ErrMalformed, t.name, parseErr.StartLine, parseErr.Err)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
		line, _ := t.reader.FieldPos(0)
		if err := visit(line, rec); err != nil {
			return err
		}
	}
}

// Events in first seen order with their points sorted by time
func parseEvents(r io.Reader) ([]evidence.Event, error) {
	t, err := readTable(eventsFile, r, eventColumns)
	if err != nil {
		return nil, err
	}
	l := newLayout(t.positions)
	var events []evidence.Event
	index := map[string]int{}
	err = t.each(func(line int, rec []string) error {
		id, p, err := l.point(rec, line)
		if err != nil {
			return err
		}
		i, ok := index[id]
		if !ok {
			i = len(events)
			index[id] = i
			events = append(events, evidence.Event{ID: id, ChangeContext: evidence.ContextUnknown})
		}
		events[i].Points = append(events[i].Points, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		slices.SortStableFunc(ev.Points, evidence.Point.Compare)
	}
	return events, nil
}

// Column positions of one events.csv
// Every column outside the required four is a dimension so a data set adds a dimension by adding a column
type layout struct {
	positions map[string]int
	// Sorted by name
	dimensions []string
}

func newLayout(positions map[string]int) layout {
	l := layout{positions: positions}
	for name := range positions {
		if !slices.Contains(eventColumns, name) {
			l.dimensions = append(l.dimensions, name)
		}
	}
	slices.Sort(l.dimensions)
	return l
}

// The event id and the point one record carries
func (l layout) point(rec []string, line int) (string, evidence.Point, error) {
	id := rec[l.positions[columnEventID]]
	if id == "" {
		return "", evidence.Point{}, fmt.Errorf("%w: %s line %d empty %s",
			evidence.ErrMalformed, eventsFile, line, columnEventID)
	}
	ts, err := time.Parse(time.RFC3339, rec[l.positions[columnTimestamp]])
	if err != nil {
		return "", evidence.Point{}, fmt.Errorf("%w: %s line %d column %s: %w",
			evidence.ErrMalformed, eventsFile, line, columnTimestamp, err)
	}
	val, err := strconv.ParseFloat(rec[l.positions[columnValue]], 64)
	if err != nil {
		return "", evidence.Point{}, fmt.Errorf("%w: %s line %d column %s: %w",
			evidence.ErrMalformed, eventsFile, line, columnValue, err)
	}
	p := evidence.Point{Time: ts.UTC(), Metric: rec[l.positions[columnMetric]], Value: val}
	if len(l.dimensions) > 0 {
		p.Dims = make(map[string]string, len(l.dimensions))
		for _, d := range l.dimensions {
			p.Dims[d] = rec[l.positions[d]]
		}
	}
	return id, p, nil
}

// Change context per event id
// 1. an empty event id is rejected as events.csv rejects it
// 2. a repeated event id is rejected because the later row would silently win
func parseContexts(r io.Reader) (map[string]evidence.Context, error) {
	t, err := readTable(contextsFile, r, []string{columnEventID, columnContext})
	if err != nil {
		return nil, err
	}
	out := map[string]evidence.Context{}
	err = t.each(func(line int, rec []string) error {
		id := rec[t.positions[columnEventID]]
		if id == "" {
			return fmt.Errorf("%w: %s line %d empty %s", evidence.ErrMalformed, contextsFile, line, columnEventID)
		}
		if _, ok := out[id]; ok {
			return fmt.Errorf("%w: %s line %d repeats %s %q",
				evidence.ErrMalformed, contextsFile, line, columnEventID, id)
		}
		c := evidence.Context(rec[t.positions[columnContext]])
		if !c.Valid() {
			return fmt.Errorf("%w: %s line %d: %q", evidence.ErrUnknownContext, contextsFile, line, c)
		}
		out[id] = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
