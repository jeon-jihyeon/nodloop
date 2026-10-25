package knowledge

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// Each interval keeps the same effective versions until its next boundary
func (s Set) capacity(before Set, item Knowledge) (Folder, bool) {
	after, item := s.capacityApproval(item)
	best := Folder{Carried: Set{}, Producer: item.Run.Producer}
	failed := best
	fits := true
	ref := Ref{ID: item.ID, Version: item.Version}
	for _, at := range after.capacityTimes(item.Time, item.Run.Producer) {
		active := after.effectiveAt(at).validAt(at)
		if !slices.ContainsFunc(active, ref.names) {
			continue
		}
		carried := active.fullestRun(item.ID, *item.Run)
		folder := Folder{Chars: utf8.RuneCountInString(item.Text()) + carried.runes(),
			Carried: carried, Producer: item.Run.Producer}
		if folder.Chars > best.Chars || folder.Chars == best.Chars && len(carried) > len(best.Carried) {
			best = folder
		}
		previous := before.effectiveAt(at).validAt(at)
		if folder.Full() {
			if growth := active.capacityGrowth(previous, *item.Run); growth != nil {
				load := growth.runes()
				carried := slices.DeleteFunc(growth, ref.names)
				if fits || load > failed.Chars || load == failed.Chars && len(carried) > len(failed.Carried) {
					failed = Folder{Chars: load, Carried: carried, Producer: item.Run.Producer}
				}
				fits = false
			}
		}
	}
	if !fits {
		return failed, false
	}
	return best, true
}

// Preview records stay in memory and never authorize a ledger write
func (s Set) capacityApproval(item Knowledge) (Set, Knowledge) {
	if current, err := s.Version(item.ID, item.Version); err == nil && current.Status == StatusApproved {
		item = current
	}
	if item.Status == StatusCandidate {
		for _, k := range s {
			if k.Time.After(item.Time) {
				item.Time = k.Time
			}
		}
		item.ApprovedAt = item.Time
	}
	item.Status = StatusApproved
	records := Set{item}
	current := s.current(item.ID)
	if current != nil && current.Status == StatusApproved && current.Version != item.Version {
		item.Supersedes = current.Version
		records[0] = item
		records = append(records, current.changed(StatusSuperseded, current.Approver, item.Time))
	}
	return slices.Concat(records, s), item
}

func (s Set) capacityTimes(from time.Time, producer string) []time.Time {
	points := []time.Time{from}
	for _, k := range s {
		if k.Run == nil || k.Run.Producer != producer {
			continue
		}
		for _, at := range []time.Time{k.Time, k.ApprovedAt, k.Run.ValidFrom, k.Run.ValidUntil} {
			if at.After(from) {
				points = append(points, at)
			}
		}
	}
	slices.SortFunc(points, time.Time.Compare)
	return slices.CompactFunc(points, time.Time.Equal)
}

func (s Set) validAt(at time.Time) Set {
	out := Set{}
	for _, k := range s {
		if k.Run.At(at) {
			out = append(out, k)
		}
	}
	slices.SortFunc(out, func(a, b Knowledge) int { return strings.Compare(a.ID, b.ID) })
	return out
}
