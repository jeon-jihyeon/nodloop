package knowledge

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

// The candidates a person has yet to approve that would reach a run of the producer with the labels
// 1. per id only the newest candidate version waits so an older one never asks beside it
// 2. a candidate beside the approved version of its id counts because it waits all the same
// 3. a candidate of a compaction is left out because only its coverage check leads to approval
// 4. each is the record of its propose or its last corroborate so its evidence is the newest
// 5. more teaching runs come first since each one is a correction the lesson would have spared
// 6. then the later of its propose and its last corroborate so fresh evidence comes before stale
// 7. then the id so every surface asks in one order
func (s Set) Waiting(producer string, labels trace.Labels) Set {
	return s.waiting(func(r RunScope) bool { return r.Admits(producer, labels) })
}

// The candidates Waiting gives for some run of any producer in the Waiting order
func (s Set) AllWaiting() Set {
	return s.waiting(func(RunScope) bool { return true })
}

// The retired versions that would reach a run of the producer with the labels
// 1. only ids with no approved version and no candidate so the current version names every other id
// 2. per id the highest such version
// 3. a version a compaction retired or proposed is left out since the item that merged it says it now
// 4. newest record first
func (s Set) Retired(producer string, labels trace.Labels) Set {
	active := map[string]bool{}
	for _, k := range s.Current() {
		active[k.ID] = true
	}
	highest := map[string]int{}
	for _, k := range s.Versions() {
		if active[k.ID] || k.Status != StatusRetired || k.Compaction != "" || k.Run == nil || !k.Run.Admits(producer, labels) {
			continue
		}
		highest[k.ID] = max(highest[k.ID], k.Version)
	}
	out := Set{}
	for _, k := range s.Versions() {
		if v, ok := highest[k.ID]; ok && v == k.Version {
			out = append(out, k)
		}
	}
	return out
}

// The waiting candidates whose run scope reaches accepts in the Waiting order
func (s Set) waiting(reaches func(RunScope) bool) Set {
	newest := s.newestCandidates()
	out := Set{}
	for _, id := range slices.Sorted(maps.Keys(newest)) {
		if k := newest[id]; k.Compaction == "" && k.Run != nil && reaches(*k.Run) {
			out = append(out, k)
		}
	}
	slices.SortStableFunc(out, Knowledge.waitsBefore)
	return out
}

// The run items of the set in clusters that runs join
// 1. two items join when one run may carry both as a folder joins them
// 2. a cluster takes every item joined to one of its items so the join is transitive
// 3. each cluster keeps the order of the set and the clusters come in the order of their first item
// 4. an item no other joins is a cluster of its own and an item without a run scope joins none
func (s Set) Clusters() []Set {
	// Positions in the set per cluster
	var clusters [][]int
	for at, k := range s {
		var joined []int
		for i, c := range clusters {
			if slices.ContainsFunc(c, func(other int) bool { return k.sharesRun(s[other]) }) {
				joined = append(joined, i)
			}
		}
		if len(joined) == 0 {
			clusters = append(clusters, []int{at})
			continue
		}
		first := joined[0]
		for _, i := range slices.Backward(joined[1:]) {
			clusters[first] = append(clusters[first], clusters[i]...)
			clusters = slices.Delete(clusters, i, i+1)
		}
		clusters[first] = append(clusters[first], at)
	}
	out := make([]Set, 0, len(clusters))
	for _, c := range clusters {
		slices.Sort(c)
		members := make(Set, 0, len(c))
		for _, at := range c {
			members = append(members, s[at])
		}
		out = append(out, members)
	}
	return out
}

// Negative when a waits before b
func (a Knowledge) waitsBefore(b Knowledge) int {
	return cmp.Or(
		cmp.Compare(len(b.Evidence.FeedbackTraceIDs), len(a.Evidence.FeedbackTraceIDs)),
		b.Time.Compare(a.Time),
		strings.Compare(a.ID, b.ID),
	)
}

// The newest version per id whose latest record is a candidate
// That record is the propose or the last corroborate the version waits as
func (s Set) newestCandidates() map[string]Knowledge {
	out := map[string]Knowledge{}
	for _, k := range s.Versions() {
		if cur, ok := out[k.ID]; k.Status == StatusCandidate && (!ok || k.Version > cur.Version) {
			out[k.ID] = k
		}
	}
	return out
}

// The record the version waits as
// Fails with ErrNotCandidate unless the version is the newest candidate of its id
func (s Set) waitingVersion(id string, version int) (Knowledge, error) {
	k, ok := s.history(id).newestCandidates()[id]
	if !ok || k.Version != version {
		return Knowledge{}, fmt.Errorf("%w: %s v%d", ErrNotCandidate, id, version)
	}
	return k, nil
}

// The record that joins a corrected run to the teaching runs of the newest candidate version of id
// 1. only the evidence grows so the version a person approves never changes
// 2. a run the candidate already cites returns no record so a repeated call changes nothing
func (s Set) corroborate(id string, version int, run trace.Trace, verdicts feedback.Records, now time.Time) ([]Knowledge, error) {
	k, err := s.waitingVersion(id, version)
	if err != nil {
		return nil, err
	}
	if err := run.CheckRun(); err != nil {
		return nil, err
	}
	if latest := verdicts.Latest(); len(latest) == 0 || !latest[0].Corrects() {
		return nil, fmt.Errorf("%w: %s", ErrNotCorrected, run.ID)
	}
	if k.Run == nil || !k.Run.Admits(run.Producer, run.Labels) {
		return nil, fmt.Errorf("%w: %s v%d does not reach run %s", ErrScopeWidened, id, version, run.ID)
	}
	if slices.Contains(k.Evidence.FeedbackTraceIDs, run.ID) {
		return nil, nil
	}
	k.Evidence.FeedbackTraceIDs = append(slices.Clone(k.Evidence.FeedbackTraceIDs), run.ID)
	k.Time = now
	return []Knowledge{k}, nil
}
