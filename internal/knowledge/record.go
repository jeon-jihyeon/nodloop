package knowledge

import (
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The version a store line holds as it was recorded
func Restore(r record.Knowledge) Knowledge {
	k := Knowledge{
		ID: r.ID, Version: r.Version, Kind: Kind(r.Kind), Content: r.Content, Basis: Basis(r.Basis), Status: Status(r.Status),
		Approver: r.Approver, ApprovedAt: r.ApprovedAt, ReviewedAt: r.ReviewedAt, Supersedes: r.Supersedes, Base: r.Base,
		Time: r.Time, Author: r.Author, Drafted: r.Drafted, NewLabels: r.NewLabels, Compaction: r.Compaction,
		CompactionSize: r.CompactionSize,
		Evidence: Evidence{
			FeedbackTraceIDs: r.Evidence.FeedbackTraceIDs, OutcomeTraceIDs: r.Evidence.OutcomeTraceIDs,
			ParagraphIDs: r.Evidence.ParagraphIDs, Knowledge: restoreRefs(r.Evidence.Knowledge),
		},
	}
	if r.Run != nil {
		k.Run = &RunScope{Producer: r.Run.Producer, Labels: trace.Labels(r.Run.Labels), Except: trace.Labels(r.Run.Except)}
	}
	if r.Veto != nil {
		k.Veto = &Veto{Tool: r.Veto.Tool, When: restoreConditions(r.Veto.When), Example: r.Veto.Example}
	}
	return k
}

// The versions of store lines in the order the store listed them
func RestoreAll(rs []record.Knowledge) Set {
	if rs == nil {
		return nil
	}
	out := make(Set, 0, len(rs))
	for _, r := range rs {
		out = append(out, Restore(r))
	}
	return out
}

// The line a store writes for the version
func (k Knowledge) Record() record.Knowledge {
	r := record.Knowledge{
		ID: k.ID, Version: k.Version, Kind: string(k.Kind), Content: k.Content, Basis: string(k.Basis),
		Status: string(k.Status), Approver: k.Approver, ApprovedAt: k.ApprovedAt, ReviewedAt: k.ReviewedAt,
		Supersedes: k.Supersedes, Base: k.Base, Time: k.Time, Author: k.Author, Drafted: k.Drafted, NewLabels: k.NewLabels,
		Compaction: k.Compaction, CompactionSize: k.CompactionSize,
		Evidence: record.Evidence{
			FeedbackTraceIDs: k.Evidence.FeedbackTraceIDs, OutcomeTraceIDs: k.Evidence.OutcomeTraceIDs,
			ParagraphIDs: k.Evidence.ParagraphIDs, Knowledge: refRecords(k.Evidence.Knowledge),
		},
	}
	if k.Run != nil {
		r.Run = &record.RunScope{Producer: k.Run.Producer, Labels: k.Run.Labels, Except: k.Run.Except}
	}
	if k.Veto != nil {
		r.Veto = &record.Veto{Tool: k.Veto.Tool, When: conditionRecords(k.Veto.When), Example: k.Veto.Example}
	}
	return r
}

// The lines a store writes for the versions in their order
func (s Set) Records() []record.Knowledge {
	if s == nil {
		return nil
	}
	out := make([]record.Knowledge, 0, len(s))
	for _, k := range s {
		out = append(out, k.Record())
	}
	return out
}

// A nil list stays nil so a version without old refs encodes as it was read
func restoreRefs(rs []record.Ref) []Ref {
	if rs == nil {
		return nil
	}
	out := make([]Ref, 0, len(rs))
	for _, r := range rs {
		out = append(out, Ref(r))
	}
	return out
}

func refRecords(rs []Ref) []record.Ref {
	if rs == nil {
		return nil
	}
	out := make([]record.Ref, 0, len(rs))
	for _, r := range rs {
		out = append(out, record.Ref(r))
	}
	return out
}

// A nil list stays nil since a veto without conditions encodes its when as null
func restoreConditions(cs []record.VetoCondition) []VetoCondition {
	if cs == nil {
		return nil
	}
	out := make([]VetoCondition, 0, len(cs))
	for _, c := range cs {
		out = append(out, VetoCondition(c))
	}
	return out
}

func conditionRecords(cs []VetoCondition) []record.VetoCondition {
	if cs == nil {
		return nil
	}
	out := make([]record.VetoCondition, 0, len(cs))
	for _, c := range cs {
		out = append(out, record.VetoCondition(c))
	}
	return out
}
