package mcp

import (
	"context"
	_ "embed"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The review steps and rules for an MCP client without the plugin skills
// go generate writes it with the review skill from one text so the two never drift
//
//go:embed review.md
var reviewText string

const reviewDescription = "Evidence-grounded review of an event with the nodloop tools: " +
	"the steps from finding the event to recording the user's verdict and the rules every review follows"

// The prompt reads no config so a client gets it before any setup
func addPrompts(srv *sdk.Server) {
	srv.AddPrompt(&sdk.Prompt{Name: "review", Description: reviewDescription},
		func(context.Context, *sdk.GetPromptRequest) (*sdk.GetPromptResult, error) {
			return &sdk.GetPromptResult{
				Description: reviewDescription,
				Messages:    []*sdk.PromptMessage{{Role: "user", Content: &sdk.TextContent{Text: reviewText}}},
			}, nil
		})
}
