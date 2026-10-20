package curate_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jeon-jihyeon/nodloop/internal/curate"
	"github.com/jeon-jihyeon/nodloop/internal/knowledge"
	"github.com/jeon-jihyeon/nodloop/internal/schema"
	"github.com/jeon-jihyeon/nodloop/internal/trace"
)

func TestAnchorVersionTrace(t *testing.T) {
	refs := []knowledge.Ref{{ID: "k-1", Version: 2}}
	files := []curate.AnchorFile{{Path: "plan.md", HMAC: hmac}, {Path: "CLAUDE.md", HMAC: hmac, MemoryType: "Project"}}
	type want struct {
		input string
		err   error
	}
	tcs := []struct {
		name string
		args curate.AnchorVersion
		want want
	}{
		{
			"a version names its refs, its files and its cause",
			curate.AnchorVersion{Version: 2, Refs: refs, Files: files, Cause: "a-1"},
			want{`{"version":2,"refs":[{"id":"k-1","version":2}],"files":[{"path":"plan.md","hmac":"` + hmac + `"},` +
				`{"path":"CLAUDE.md","hmac":"` + hmac + `","memory_type":"Project"}],"cause":"a-1"}`, nil},
		},
		{
			"an empty anchor keeps empty lists",
			curate.AnchorVersion{Version: 1, Cause: curate.CauseApproval},
			want{`{"version":1,"refs":[],"files":[],"cause":"approval"}`, nil},
		},
		{"version 0 is refused", curate.AnchorVersion{Cause: "a-1"}, want{err: curate.ErrAnchorInvalid}},
		{"no cause is refused", curate.AnchorVersion{Version: 1}, want{err: curate.ErrAnchorInvalid}},
		{"a ref without its version is refused", curate.AnchorVersion{Version: 1, Cause: "a-1", Refs: []knowledge.Ref{{ID: "k-1"}}}, want{err: curate.ErrAnchorInvalid}},
		{
			"a file without its path is refused",
			curate.AnchorVersion{Version: 1, Cause: "a-1", Files: []curate.AnchorFile{{HMAC: hmac}}},
			want{err: curate.ErrAnchorInvalid},
		},
		{
			"a file whose HMAC is no 64 hex is refused",
			curate.AnchorVersion{Version: 1, Cause: "a-1", Files: []curate.AnchorFile{{Path: "plan.md", HMAC: "plan text"}}},
			want{err: curate.ErrAnchorInvalid},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := tc.args.Trace(at)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.input, string(got.Input))
		})
	}
}

func TestAnchorVersionTraceFields(t *testing.T) {
	got, err := curate.AnchorVersion{Version: 3, Cause: curate.CauseApproval}.Trace(at)
	require.NoError(t, err)

	assert.Equal(t, []any{trace.NameAnchor, "v3", at}, []any{got.Name, got.Subject, got.Time})
	assert.NoError(t, schema.Validate(string(trace.NameAnchor), got.Record()))
}

func TestRestoreAnchor(t *testing.T) {
	kept, err := curate.AnchorVersion{Version: 2, Refs: []knowledge.Ref{{ID: "k-1", Version: 1}}, Cause: "a-1"}.Trace(at)
	require.NoError(t, err)
	type want struct {
		version anchorWant
		err     error
	}
	tcs := []struct {
		name string
		args trace.Trace
		want want
	}{
		{"no trace is version 0", trace.Trace{}, want{anchorWant{}, nil}},
		{"a trace gives the version it holds", kept, want{anchorWant{2, []knowledge.Ref{{ID: "k-1", Version: 1}}, "a-1"}, nil}},
		{"an input that does not decode is refused", trace.Trace{ID: "t-1", Input: []byte(`{"version":"two"}`)}, want{anchorWant{}, curate.ErrAnchorInvalid}},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := curate.RestoreAnchor(tc.args)

			assert.ErrorIs(t, err, tc.want.err)
			assert.Equal(t, tc.want.version, anchorWant{got.Version, got.Refs, got.Cause})
		})
	}
}

// What a test reads of an anchor version
type anchorWant struct {
	Version int
	Refs    []knowledge.Ref
	Cause   string
}

// An approved record of the repo nodloop under the approver with the decision behind it
func anchorItem(id string, version int, status knowledge.Status, approver, decision string) knowledge.Knowledge {
	return knowledge.Knowledge{
		ID: id, Version: version, Kind: knowledge.KindMeaning, Content: id, Status: status, Approver: approver, Decision: decision,
		Run: &knowledge.RunScope{Producer: "session", Labels: trace.Labels{"repo": {"nodloop"}}}, Time: at,
	}
}

func TestAnchorNext(t *testing.T) {
	other := strings.Repeat("cd", 32)
	approved, retired := knowledge.StatusApproved, knowledge.StatusRetired
	person := anchorItem("k-1", 1, approved, "ann", "")
	plan := curate.AnchorFile{Path: "/acme/plan.md", HMAC: hmac}
	rules := curate.Loaded{Session: "s1", File: curate.AnchorFile{Path: "/acme/CLAUDE.md", HMAC: hmac, MemoryType: "instructions"}}
	v1 := curate.AnchorVersion{Version: 1, Refs: []knowledge.Ref{{ID: "k-1", Version: 1}}, Files: []curate.AnchorFile{plan, rules.File}, Cause: "a-1"}
	type args struct {
		newest     curate.AnchorVersion
		all        knowledge.Set
		designated []curate.AnchorFile
		loaded     []curate.Loaded
	}
	type want struct {
		version anchorWant
		files   []string
		changed bool
	}
	tcs := []struct {
		name string
		args args
		want want
	}{
		{"nothing on record keeps version 0", args{curate.AnchorVersion{}, nil, nil, nil}, want{anchorWant{}, nil, false}},
		{
			"a person's approval through a command starts version 1",
			args{curate.AnchorVersion{}, knowledge.Set{person}, nil, nil},
			want{anchorWant{1, []knowledge.Ref{{ID: "k-1", Version: 1}}, curate.CauseApproval}, nil, true},
		},
		{
			"a version nodloop approved stays out",
			args{curate.AnchorVersion{}, knowledge.Set{anchorItem("k-2", 1, approved, knowledge.ApproverNodloop, "d-1")}, nil, nil},
			want{anchorWant{}, nil, false},
		},
		{
			"the same refs and files keep the newest version",
			args{v1, knowledge.Set{person}, []curate.AnchorFile{plan}, []curate.Loaded{rules}},
			want{anchorWant{1, []knowledge.Ref{{ID: "k-1", Version: 1}}, "a-1"}, []string{"/acme/plan.md", "/acme/CLAUDE.md"}, false},
		},
		{
			"a retire under the person's answer names the answer",
			args{v1, knowledge.Set{anchorItem("k-1", 1, retired, "ann", "answer-1"), person}, []curate.AnchorFile{plan}, []curate.Loaded{rules}},
			want{anchorWant{2, []knowledge.Ref{}, "answer-1"}, []string{"/acme/plan.md", "/acme/CLAUDE.md"}, true},
		},
		{
			"a file whose content changed names the files",
			args{v1, knowledge.Set{person}, []curate.AnchorFile{{Path: plan.Path, HMAC: other}}, []curate.Loaded{rules}},
			want{anchorWant{2, []knowledge.Ref{{ID: "k-1", Version: 1}}, curate.CauseFiles}, []string{"/acme/plan.md", "/acme/CLAUDE.md"}, true},
		},
		{
			"a memory type reported another way changes nothing",
			args{v1, knowledge.Set{person}, []curate.AnchorFile{plan}, []curate.Loaded{{Session: "s2", File: curate.AnchorFile{Path: rules.File.Path, HMAC: hmac}}}},
			want{anchorWant{1, []knowledge.Ref{{ID: "k-1", Version: 1}}, "a-1"}, []string{"/acme/plan.md", "/acme/CLAUDE.md"}, false},
		},
		{
			"each path once with the designated file first and the newest rules file of a path",
			args{
				curate.AnchorVersion{}, nil, []curate.AnchorFile{plan},
				[]curate.Loaded{
					{Session: "s2", File: curate.AnchorFile{Path: "/acme/z/CLAUDE.md", HMAC: other}}, rules,
					{Session: "s1", File: curate.AnchorFile{Path: "/acme/z/CLAUDE.md", HMAC: hmac}}, {Session: "s1", File: plan},
				},
			},
			want{anchorWant{1, []knowledge.Ref{}, curate.CauseFiles}, []string{"/acme/plan.md", "/acme/CLAUDE.md", "/acme/z/CLAUDE.md"}, true},
		},
	}
	for _, tc := range tcs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, changed := curate.NewAnchor(tc.args.all, tc.args.designated, tc.args.loaded).Next(tc.args.newest)

			assert.Equal(t, tc.want.changed, changed)
			assert.Equal(t, tc.want.version, anchorWant{got.Version, got.Refs, got.Cause})
			assert.Equal(t, tc.want.files, filePaths(got.Files))
		})
	}
}

// The newest rules file of a path keeps its own HMAC
func TestAnchorNextNewestFile(t *testing.T) {
	newer := strings.Repeat("cd", 32)
	loaded := []curate.Loaded{
		{Session: "s2", File: curate.AnchorFile{Path: "/acme/CLAUDE.md", HMAC: newer}},
		{Session: "s1", File: curate.AnchorFile{Path: "/acme/CLAUDE.md", HMAC: hmac}},
	}

	got, changed := curate.NewAnchor(nil, nil, loaded).Next(curate.AnchorVersion{})

	require.True(t, changed)
	assert.Equal(t, []curate.AnchorFile{{Path: "/acme/CLAUDE.md", HMAC: newer}}, got.Files)
	tr, err := got.Trace(at)
	require.NoError(t, err)
	assert.NoError(t, schema.Validate(string(trace.NameAnchor), tr.Record()))
}

func filePaths(files []curate.AnchorFile) []string {
	if files == nil {
		return nil
	}
	out := []string{}
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestAnchorPlaces(t *testing.T) {
	run := func(session string, labels trace.Labels) trace.Trace {
		return trace.Trace{ID: "r-" + session, Name: trace.NameRun, Producer: "session", SessionID: session, Labels: labels}
	}
	codexOnly := anchorItem("k-codex", 1, knowledge.StatusApproved, "ann", "")
	codexOnly.Run.Labels["host"] = []string{"codex"}
	all := knowledge.Set{
		anchorItem("k-repo", 1, knowledge.StatusApproved, "ann", ""), codexOnly,
		anchorItem("k-nodloop", 1, knowledge.StatusApproved, knowledge.ApproverNodloop, ""),
	}
	plan := curate.AnchorFile{Path: "/acme/plan.md", HMAC: hmac}
	loaded := []curate.Loaded{
		{Session: "s1", File: curate.AnchorFile{Path: "/nodloop/CLAUDE.md", HMAC: hmac, MemoryType: "instructions"}},
		{Session: "s3", File: curate.AnchorFile{Path: "/acme/CLAUDE.md", HMAC: hmac, MemoryType: "instructions"}},
		{Session: "s9", File: curate.AnchorFile{Path: "/gone/CLAUDE.md", HMAC: hmac}},
	}
	runs := trace.Traces{
		run("s1", trace.Labels{"repo": {"nodloop"}, "dir": {"."}, "host": {"claude-code"}}),
		run("s2", trace.Labels{"repo": {"nodloop"}, "dir": {"."}, "host": {"codex"}}),
		run("s3", trace.Labels{"repo": {"acme"}, "dir": {"."}, "host": {"claude-code"}}),
		run("", trace.Labels{"repo": {"nodloop"}, "dir": {"."}}),
		{ID: "t-1", Name: trace.NameTurn, SessionID: "s9"},
	}
	type want struct {
		place  string
		items  []knowledge.Ref
		loaded []string
	}

	got := curate.NewAnchor(all, []curate.AnchorFile{plan}, loaded).Places(runs)

	out := []want{}
	for _, p := range got {
		out = append(out, want{p.Place.String(), p.Items.Refs(), filePaths(p.Loaded)})
	}
	assert.Equal(t, []want{
		{"runs of session. dir=.. repo=acme", []knowledge.Ref{}, []string{"/acme/CLAUDE.md"}},
		{"runs of session. dir=.. repo=nodloop", []knowledge.Ref{{ID: "k-codex", Version: 1}, {ID: "k-repo", Version: 1}}, []string{"/nodloop/CLAUDE.md"}},
	}, out)
	assert.Equal(t, []curate.AnchorFile{plan}, curate.NewAnchor(all, []curate.AnchorFile{plan}, loaded).Designated())
}
