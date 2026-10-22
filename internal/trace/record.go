package trace

import "github.com/jeon-jihyeon/nodloop/internal/record"

// The trace a store line holds as it was recorded
func Restore(r record.Trace) Trace {
	return Trace{
		ID: r.ID, Name: Name(r.Name), SessionID: r.SessionID, Subject: r.Subject, Ref: r.Ref, Time: r.Time, Model: r.Model,
		Input: r.Input, Output: r.Output, Error: r.Error, Usage: Usage(r.Usage), DurationMS: r.DurationMS, Tags: r.Tags,
		Producer: r.Producer, Labels: Labels(r.Labels), Draw: (*Draw)(r.Draw), Source: Source(r.Source),
	}
}

// The traces of store lines in the order the store listed them
func RestoreAll(rs []record.Trace) Traces {
	if rs == nil {
		return nil
	}
	out := make(Traces, 0, len(rs))
	for _, r := range rs {
		out = append(out, Restore(r))
	}
	return out
}

// Names without a breaking change keep the first published schema
const SchemaVersion = 1

func (n Name) SchemaVersion() int {
	if n == NameDecide || n == NameKnob {
		return 2
	}
	return SchemaVersion
}

// The line a store writes for the trace under the current version of its schema
func (t Trace) Record() record.Trace {
	return record.Trace{
		ID: t.ID, Name: string(t.Name), SessionID: t.SessionID, Subject: t.Subject, Ref: t.Ref, Time: t.Time, Model: t.Model,
		Input: t.Input, Output: t.Output, Error: t.Error, Usage: record.Usage(t.Usage), DurationMS: t.DurationMS, Tags: t.Tags,
		Producer: t.Producer, Labels: t.Labels, Draw: (*record.Draw)(t.Draw), Source: string(t.Source), SchemaVersion: t.Name.SchemaVersion(),
	}
}
