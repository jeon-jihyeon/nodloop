package nodloop

import (
	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/feedback"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/loop"
	"github.com/jeon-jihyeon/nodloop/internal/record"
	"github.com/jeon-jihyeon/nodloop/internal/retain"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

var (
	// A proposal from a run whose latest verdict does not correct it
	ErrNotCorrected = knowledge.ErrNotCorrected
	// A scope that names a label value no recorded run carries while NewLabels is off
	ErrScopeUnobserved = knowledge.ErrScopeUnobserved
	// A knowledge id or version that was never recorded
	ErrNotFound = knowledge.ErrNotFound
	// A run that names both Applied and Withheld
	ErrWithheldApplied = knowledge.ErrWithheldApplied
	// A WithHoldout share outside 0 up to but not including 1
	ErrHoldoutInvalid = knowledge.ErrHoldoutInvalid
	// A WithRetention below the shortest retention of 7 days
	ErrRetentionTooShort = retain.ErrDaysTooShort
	// A Report name that names no report
	ErrReportUnknown = loop.ErrReportUnknown
	// An Outcome whose result is not one of the results
	ErrResultUnknown = feedback.ErrResultUnknown
	// A Judgment or an Outcome whose run id names no recorded run
	ErrRunNotFound = record.ErrTraceNotFound
	// A Run whose Duration is below zero
	ErrDurationNegative = trace.ErrDurationNegative
	// A Judgment or an Outcome that names no reviewer
	ErrReviewerRequired = feedback.ErrReviewerRequired
	// A Judgment or an Outcome under the name only the capture of the session records its verdicts under
	ErrReviewerReserved = feedback.ErrReviewerReserved
	// An Approve, ApproveIn or Retire that names no approver
	ErrApproverRequired = knowledge.ErrApproverRequired
	// An Approve, ApproveIn or Retire under the name only what nodloop carries out is recorded under
	ErrApproverReserved = knowledge.ErrApproverReserved
	// A Query whose kind is no kind of the published index
	ErrKindUnknown = schema.ErrKindUnknown
	// A Query whose Since is no Next a page answered
	ErrCursorInvalid = schema.ErrCursorInvalid
	// A Query of a kind kept outside the record directory such as the guard log of a machine
	ErrNotServed = schema.ErrNotServed
	// An Intervene whose target the records do not hold or whose knob value lies outside its range
	ErrInterventionRefused = curate.ErrTargetInvalid
)
