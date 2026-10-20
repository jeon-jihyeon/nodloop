package mcp_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/jeon-jihyeon/nodloop/internal/mcp"
)

// Each role holds the tools of the one before and only an approver key a person holds proves the person
func TestRole(t *testing.T) {
	type args struct {
		role mcp.Role
		// Whether the key was added with --person
		person bool
	}
	type want struct {
		valid  bool
		tools  int
		proves bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"a producer key a person holds proves nobody", args{mcp.RoleProducer, true}, want{true, 5, false}},
		{"a reviewer key a person holds proves nobody", args{mcp.RoleReviewer, true}, want{true, 16, false}},
		{"an approver key a person holds proves the person", args{mcp.RoleApprover, true}, want{true, 19, true}},
		{"an approver key of a service proves nobody", args{mcp.RoleApprover, false}, want{true, 19, false}},
		{"a role outside the set holds no tool", args{"admin", true}, want{false, 0, false}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := want{tc.args.role.Valid(), len(tc.args.role.Tools()), tc.args.role.Proves(tc.args.person)}

			assert.Equal(t, tc.want, got)
		})
	}
	assert.Equal(t, []mcp.Role{mcp.RoleProducer, mcp.RoleReviewer, mcp.RoleApprover}, mcp.Roles())
	assert.ElementsMatch(t, mcp.Tools(), mcp.RoleApprover.Tools())
}
