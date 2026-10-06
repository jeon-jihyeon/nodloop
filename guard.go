package nodloop

import (
	"context"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// What a tool call may do
type Action string

const (
	ActionAllow Action = "allow" // no approved veto matches
	ActionBlock Action = "block" // a veto forbids it and its reason says why
	ActionAsk   Action = "ask"   // a veto hands it to a person with its reason
)

// The answer for one tool call
type Decision struct {
	Action Action
	// The id of the approved knowledge whose veto matched
	Veto   string
	Reason string
}

// Checks a tool call of the producer against the vetoes approved for it before the agent runs it
// 1. a veto that blocks wins over one that asks
// 2. input holds the arguments of the call as the tool receives them
// 3. a Bash call names its command under the key command so every simple command in it is matched
// 4. an empty producer checks against the vetoes of every producer
func (c *Client) CheckCall(ctx context.Context, producer, tool string, input map[string]any) (Decision, error) {
	all, err := c.ledger.All(ctx)
	if err != nil {
		return Decision{}, err
	}
	vetoes, err := all.Guard(producer)
	if err != nil {
		return Decision{}, err
	}
	matched := vetoes.Match(tool, input)
	if matched == nil {
		return Decision{Action: ActionAllow}, nil
	}
	action := ActionBlock
	if matched.Action() == veto.ActionAsk {
		action = ActionAsk
	}
	return Decision{Action: action, Veto: matched.ID(), Reason: matched.Reason()}, nil
}
