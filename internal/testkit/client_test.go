package testkit_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/testkit"
)

func TestClientCall(t *testing.T) {
	t.Parallel()
	type echoInput struct {
		Text string `json:"text"`
	}
	c := testkit.Connect(t, func(ctx context.Context, transport sdk.Transport) error {
		srv := sdk.NewServer(&sdk.Implementation{Name: "echo", Version: "0"}, nil)
		sdk.AddTool(srv, &sdk.Tool{Name: "echo"}, func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, any, error) {
			return nil, map[string]any{"text": in.Text}, nil
		})
		sdk.AddTool(srv, &sdk.Tool{Name: "fail"}, func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, any, error) {
			return nil, nil, errors.New(in.Text)
		})
		return srv.Run(ctx, transport)
	})
	type want struct {
		out map[string]string
		err error
	}
	tcs := []struct {
		name string
		args string
		want want
	}{
		{
			name: "the structured content decodes into out",
			args: "echo",
			want: want{out: map[string]string{"text": "hello"}},
		},
		{
			name: "a tool error returns ErrTool with the server message and leaves out untouched",
			args: "fail",
			want: want{out: map[string]string{}, err: fmt.Errorf("%w: hello", testkit.ErrTool)},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			out := map[string]string{}
			err := c.Call(t, tc.args, map[string]any{"text": "hello"}, &out)
			assert.Equal(t, tc.want.err, err)
			assert.Equal(t, tc.want.out, out)
		})
	}
}

func TestClientRun(t *testing.T) {
	t.Parallel()
	type echoInput struct {
		Text string `json:"text"`
	}
	c := testkit.Connect(t, func(ctx context.Context, transport sdk.Transport) error {
		srv := sdk.NewServer(&sdk.Implementation{Name: "run", Version: "0"}, nil)
		sdk.AddTool(srv, &sdk.Tool{Name: "echo"}, func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, any, error) {
			return nil, map[string]any{"text": in.Text}, nil
		})
		sdk.AddTool(srv, &sdk.Tool{Name: "fail"}, func(_ context.Context, _ *sdk.CallToolRequest, in echoInput) (*sdk.CallToolResult, any, error) {
			return nil, nil, errors.New(in.Text)
		})
		return srv.Run(ctx, transport)
	})
	tcs := []struct {
		name string
		args string
		want error
	}{
		{name: "a successful tool returns no error and its answer is dropped", args: "echo"},
		{name: "a tool error returns ErrTool with the server message", args: "fail", want: fmt.Errorf("%w: hello", testkit.ErrTool)},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, c.Run(t, tc.args, map[string]any{"text": "hello"}))
		})
	}
}

func TestClientTools(t *testing.T) {
	t.Parallel()
	type emptyInput struct{}
	c := testkit.Connect(t, func(ctx context.Context, transport sdk.Transport) error {
		srv := sdk.NewServer(&sdk.Implementation{Name: "tools", Version: "0"}, nil)
		sdk.AddTool(srv, &sdk.Tool{Name: "first"}, func(context.Context, *sdk.CallToolRequest, emptyInput) (*sdk.CallToolResult, any, error) {
			return nil, nil, nil
		})
		sdk.AddTool(srv, &sdk.Tool{Name: "second"}, func(context.Context, *sdk.CallToolRequest, emptyInput) (*sdk.CallToolResult, any, error) {
			return nil, nil, nil
		})
		return srv.Run(ctx, transport)
	})
	assert.ElementsMatch(t, []string{"first", "second"}, c.Tools(t))
}
