package extract

import (
	"context"
	"encoding/json"

	"github.com/jeon-jihyeon/nodloop/internal/classify"
	"github.com/jeon-jihyeon/nodloop/internal/llm"
)

// The questions of the critic point
// The same three questions CriticRules asks so a classifier and the claude critic judge one thing
var CriticQuestions = classify.Questions{
	"states": "The draft lesson states what the edit changed or what the reject named and nothing the person did not correct.",
	"holds":  "The draft lesson would apply to the next run in this place and not only to this one output.",
	"fits": "The relation of the draft is right against the approved items shown: add only when no item says it, " +
		"duplicate only when one already does, conflict only when one says the opposite, update only when one says part of it.",
}

// The critic call as the built in member of the critic point
// A true answer is a yes of 1 and a false one a yes of 0 with why as the reason of each
type ClaudeCritic struct {
	client llm.Client
	model  string
}

func NewClaudeCritic(client llm.Client, model string) ClaudeCritic {
	return ClaudeCritic{client: client, model: model}
}

func (c ClaudeCritic) Classify(ctx context.Context, req classify.Request) (classify.Answers, error) {
	critique, err := complete[Critique](ctx, c.client, llm.Request{
		System: CriticRules, Prompt: req.State, Schema: json.RawMessage(CriticSchema), Model: c.model,
	})
	if err != nil {
		return nil, err
	}
	answer := func(ok bool) classify.Answer {
		if ok {
			return classify.Answer{Yes: 1, Reason: critique.Why}
		}
		return classify.Answer{Yes: 0, Reason: critique.Why}
	}
	return classify.Answers{"states": answer(critique.States), "holds": answer(critique.Holds), "fits": answer(critique.Fits)}, nil
}

// The critique the answers make
// A member without a reason leaves the probabilities as why so a refusal still says what the classifier answered
func newCritique(answers classify.Answers) Critique {
	why := answers["states"].Reason
	if why == "" {
		why = answers.String()
	}
	return Critique{States: answers["states"].True(), Holds: answers["holds"].True(), Fits: answers["fits"].True(), Why: why}
}
