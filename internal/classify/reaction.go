package classify

import "github.com/jeon-jihyeon/nodloop/internal/feedback"

// The questions of the reaction point
var ReactionQuestions = Questions{
	"corrects": "The user's message says the previous answer was wrong or asks to change what it did.",
	"approves": "The user's message says the previous answer was right.",
}

// The verdict the answers to ReactionQuestions call
// Empty when they say both or neither
func (as Answers) Verdict() feedback.Verdict {
	corrects, approves := as["corrects"].True(), as["approves"].True()
	switch {
	case corrects && !approves:
		return feedback.VerdictReject
	case approves && !corrects:
		return feedback.VerdictApprove
	}
	return ""
}
