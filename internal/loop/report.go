package loop

import (
	"slices"
	"time"

	"github.com/jeon-jihyeon/nodloop/internal/feedback"
)

// How the runs fare with a person read from the records alone
type Report struct {
	Since time.Time `json:"since"`
	// Weeks from Monday UTC by the time of the verdict in time order
	Weeks []Week `json:"weeks"`
	// Verdicts recorded before their run and left out of the waits
	InvalidWaits     int    `json:"invalid_waits"`
	KnowledgeApplied Cohort `json:"knowledge_applied"`
	WithoutKnowledge Cohort `json:"without_knowledge"`
}

type Week struct {
	Start    time.Time `json:"start"`
	Verdicts Verdicts  `json:"verdicts"`
	// The verdicts on random audit samples of the queue
	Audit Verdicts `json:"audit"`
	// Seconds from the run to its verdict
	// A wait for the verdict and not the working time of the person
	MedianWaitSeconds *float64 `json:"median_wait_seconds"`
	// Top level fields an edit changed counted over the edits only
	MedianEditWidth *float64 `json:"median_edit_width"`
}

type Verdicts struct {
	Total       int      `json:"total"`
	Approve     int      `json:"approve"`
	Edit        int      `json:"edit"`
	Reject      int      `json:"reject"`
	ApproveRate *float64 `json:"approve_rate"`
	EditRate    *float64 `json:"edit_rate"`
	RejectRate  *float64 `json:"reject_rate"`
	// Counts per reason code over the corrections that carry one
	ReasonCodes map[feedback.ReasonCode]int `json:"reason_codes,omitempty"`
}

// An empty code counts only in the verdict counts
func (v *Verdicts) add(verdict feedback.Verdict, code feedback.ReasonCode) {
	if code != "" {
		if v.ReasonCodes == nil {
			v.ReasonCodes = map[feedback.ReasonCode]int{}
		}
		v.ReasonCodes[code]++
	}
	v.Total++
	switch verdict {
	case feedback.VerdictApprove:
		v.Approve++
	case feedback.VerdictEdit:
		v.Edit++
	case feedback.VerdictReject:
		v.Reject++
	}
	v.ApproveRate, v.EditRate, v.RejectRate = share(v.Approve, v.Total), share(v.Edit, v.Total), share(v.Reject, v.Total)
}

type Outcomes struct {
	Total            int      `json:"total"`
	Confirmed        int      `json:"confirmed"`
	Refuted          int      `json:"refuted"`
	Inconclusive     int      `json:"inconclusive"`
	ConfirmedRate    *float64 `json:"confirmed_rate"`
	RefutedRate      *float64 `json:"refuted_rate"`
	InconclusiveRate *float64 `json:"inconclusive_rate"`
}

func (o *Outcomes) add(result feedback.Result) {
	o.Total++
	switch result {
	case feedback.ResultConfirmed:
		o.Confirmed++
	case feedback.ResultRefuted:
		o.Refuted++
	case feedback.ResultInconclusive:
		o.Inconclusive++
	}
	o.ConfirmedRate, o.RefutedRate = share(o.Confirmed, o.Total), share(o.Refuted, o.Total)
	o.InconclusiveRate = share(o.Inconclusive, o.Total)
}

// The runs that applied knowledge or none
// The wait and the edit width read like those of a week so the two cohorts compare on verdict time and edit size
type Cohort struct {
	Verdicts          Verdicts `json:"verdicts"`
	Outcomes          Outcomes `json:"outcomes"`
	MedianWaitSeconds *float64 `json:"median_wait_seconds"`
	MedianEditWidth   *float64 `json:"median_edit_width"`
}

// Nil when there is nothing to share
func share(part, total int) *float64 {
	if total == 0 {
		return nil
	}
	v := float64(part) / float64(total)
	return &v
}

// Every human verdict and outcome from since on
func (h *History) Report(since time.Time) Report {
	rep := Report{Since: since, Weeks: []Week{}}
	weeks := map[time.Time]*Week{}
	byWeek, byCohort := map[time.Time]*measures{}, map[*Cohort]*measures{}
	for _, r := range h.reviews {
		cohort := &rep.WithoutKnowledge
		if len(r.applied()) > 0 {
			cohort = &rep.KnowledgeApplied
		}
		if o, ok := h.outcomes[r.trace.ID]; ok && !o.Time.Before(since) {
			cohort.Outcomes.add(o.Result)
		}
		fb, ok := h.verdicts[r.trace.ID]
		if !ok || fb.Time.Before(since) {
			continue
		}
		cohort.Verdicts.add(fb.Verdict, fb.ReasonCode)
		start := weekOf(fb.Time)
		if weeks[start] == nil {
			weeks[start], byWeek[start] = &Week{Start: start}, &measures{}
		}
		if byCohort[cohort] == nil {
			byCohort[cohort] = &measures{}
		}
		weeks[start].Verdicts.add(fb.Verdict, fb.ReasonCode)
		if fb.Audit {
			weeks[start].Audit.add(fb.Verdict, fb.ReasonCode)
		}
		if fb.Verdict == feedback.VerdictEdit {
			width := float64(fb.EditWidth(r.trace.Output))
			byWeek[start].widths, byCohort[cohort].widths = append(byWeek[start].widths, width), append(byCohort[cohort].widths, width)
		}
		if fb.Time.Before(r.trace.Time) {
			rep.InvalidWaits++
			continue
		}
		wait := fb.Time.Sub(r.trace.Time).Seconds()
		byWeek[start].waits, byCohort[cohort].waits = append(byWeek[start].waits, wait), append(byCohort[cohort].waits, wait)
	}
	for start, week := range weeks {
		week.MedianWaitSeconds, week.MedianEditWidth = byWeek[start].waits.median(), byWeek[start].widths.median()
		rep.Weeks = append(rep.Weeks, *week)
	}
	for cohort, m := range byCohort {
		cohort.MedianWaitSeconds, cohort.MedianEditWidth = m.waits.median(), m.widths.median()
	}
	slices.SortFunc(rep.Weeks, Week.compare)
	return rep
}

// Orders weeks by start
func (w Week) compare(other Week) int {
	return w.Start.Compare(other.Start)
}

// Monday 00:00 UTC of the week that holds t
func weekOf(t time.Time) time.Time {
	t = t.UTC()
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	return day.AddDate(0, 0, -((int(day.Weekday()) + 6) % 7))
}

// The verdict waits in seconds and the edit widths of one week or one cohort
type measures struct {
	waits  samples
	widths samples
}

// Values of one measure such as wait seconds or edit widths
type samples []float64

// Nil without a sample
func (ss samples) median() *float64 {
	if len(ss) == 0 {
		return nil
	}
	sorted := slices.Sorted(slices.Values(ss))
	mid := len(sorted) / 2
	m := sorted[mid]
	if len(sorted)%2 == 0 {
		m = (sorted[mid-1] + sorted[mid]) / 2
	}
	return &m
}
