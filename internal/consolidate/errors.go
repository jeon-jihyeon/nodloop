package consolidate

import "errors"

var (
	// The model answered outside the schema
	ErrAnswerInvalid = errors.New("consolidate: answer does not match the schema")
	// A group names a candidate the cluster did not send at that version
	ErrMemberUnknown = errors.New("consolidate: a member is no candidate of the cluster")
	// One candidate alone says nothing twice
	ErrGroupSmall = errors.New("consolidate: a group holds fewer than two members")
	// A candidate sits in one group at most
	ErrMemberRepeated = errors.New("consolidate: a candidate sits in two groups")
	// A meaning and a judgment never say the same lesson
	ErrKindsDiffer = errors.New("consolidate: the kinds within a group differ")
	ErrMark        = errors.New("consolidate: mark failed")
)
