// Package call shapes a tool call into the few words a lesson may name without its arguments
package call

import (
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"mvdan.cc/sh/v3/syntax"

	"github.com/jeon-jihyeon/nodloop/internal/veto"
)

// One calls trace keeps no more shapes than this so a turn of many calls stays a short record
const Kept = 64

// What a shape may keep of one program
type program struct {
	// The subcommand paths the program defines such as pr create
	// A path the program lacks is the user's own name or an argument so a shape never keeps it
	commands []string
	// The flags before a subcommand that take the next argument as their value
	values []string
}

// The programs whose name a shape keeps with the subcommands they define
// A program left out keeps no word so an argument value never lands in a shape
var programs = map[string]program{
	"git": {commands: split("add am apply bisect blame branch checkout cherry-pick clean clone commit config describe diff fetch format-patch grep " +
		"init log ls-files merge mv notes pull push rebase reflog remote reset restore rev-parse revert rm show stash status submodule switch tag worktree"),
		values: split("-C -c --git-dir --work-tree")},
	"gh": {commands: slices.Concat(split("api pr issue release run repo workflow auth"),
		under("pr", "create view list merge review comment edit checkout diff ready close checks"),
		under("issue", "create view list comment edit close"), under("release", "create view list"),
		under("run", "view list watch rerun"), under("repo", "view clone fork"), under("workflow", "run view"), under("auth", "status")),
		values: split("-R --repo")},
	"glab": {commands: slices.Concat(split("mr issue ci"), under("mr", "create view list merge note"), under("issue", "create view list"),
		under("ci", "view status"))},
	"go":            {commands: split("build test run vet fmt mod generate install get list work env doc version tool")},
	"gofmt":         {},
	"cargo":         {commands: split("build test run check clippy fmt add new init publish bench doc update install")},
	"rustc":         {},
	"npm":           {commands: split("install ci test run publish init exec audit update uninstall pack version")},
	"npx":           {},
	"pnpm":          {commands: split("add install remove run test update exec")},
	"yarn":          {commands: split("add install remove run test upgrade why workspace")},
	"bun":           {commands: split("install add remove run test build x")},
	"deno":          {commands: split("run test fmt lint task check compile")},
	"node":          {},
	"python":        {},
	"pip":           {commands: split("install uninstall freeze list show")},
	"uv":            {commands: split("run sync add remove lock pip venv tool init build publish")},
	"poetry":        {commands: split("install add remove run lock build publish update")},
	"pytest":        {},
	"ruff":          {commands: split("check format")},
	"mypy":          {},
	"make":          {},
	"docker":        {commands: split("build run exec compose push pull ps images logs stop rm tag login")},
	"podman":        {commands: split("build run exec compose push pull ps images logs stop rm tag login")},
	"kubectl":       {commands: split("get describe apply delete logs exec rollout scale edit create port-forward"), values: split("-n --namespace --context --kubeconfig")},
	"helm":          {commands: split("install upgrade uninstall template list repo lint package"), values: split("-n --namespace --kube-context")},
	"terraform":     {commands: split("init plan apply destroy validate fmt import state output")},
	"aws":           {commands: split("s3 ec2 lambda iam sts ecr ecs eks logs cloudformation dynamodb sqs sns rds athena ssm secretsmanager"), values: split("--profile --region")},
	"gcloud":        {commands: split("compute container run functions iam auth config projects storage sql builds app"), values: split("--project")},
	"az":            {commands: split("login account group vm aks acr storage webapp functionapp")},
	"mvn":           {commands: split("clean compile test package install verify deploy")},
	"gradle":        {commands: split("build test clean check assemble")},
	"dotnet":        {commands: split("build test run restore publish new add pack")},
	"bundle":        {commands: split("install exec update add")},
	"rake":          {},
	"rails":         {commands: split("server console generate db test routes new")},
	"mix":           {},
	"swift":         {commands: split("build test run package")},
	"xcodebuild":    {},
	"flutter":       {commands: split("build test run pub analyze create")},
	"golangci-lint": {commands: split("run fmt")},
}

// The words of a list separated by spaces
func split(list string) []string {
	return strings.Fields(list)
}

// The two word paths of a subcommand and the words after it
func under(first, list string) []string {
	var out []string
	for _, w := range strings.Fields(list) {
		out = append(out, first+" "+w)
	}
	return out
}

var (
	toolName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	word     = regexp.MustCompile(`^[a-z](?:[a-z]|-[a-z]){0,23}$`)
	ext      = `[a-z][a-z0-9]{0,7}`
	// The shapes a file takes with a test shape before the plain one
	fileShapes = []struct {
		pattern *regexp.Regexp
		shape   string
	}{
		{regexp.MustCompile(`^.+_test\.(` + ext + `)$`), "*_test."},
		{regexp.MustCompile(`^test_.+\.(` + ext + `)$`), "test_*."},
		{regexp.MustCompile(`^.+\.test\.(` + ext + `)$`), "*.test."},
		{regexp.MustCompile(`^.+\.spec\.(` + ext + `)$`), "*.spec."},
		{regexp.MustCompile(`^.+\.(` + ext + `)$`), "*."},
	}
	fileShape = regexp.MustCompile(`^(?:\*_test|test_\*|\*\.test|\*\.spec|\*)\.` + ext + `$`)
)

// The tool_input keys a file tool names its file in
var fileFields = []string{"file_path", "notebook_path"}

// One kind of tool call with no argument text
type Shape struct {
	Tool string `json:"tool"`
	// Up to three lowercase words a command line starts with
	Command string `json:"command,omitempty"`
	// The shape of the base name of the file the call names
	File string `json:"file,omitempty"`
}

// Distinct shapes in first seen order
type Shapes []Shape

// The input of a calls trace with the shapes of calls no stop of a run, start of a turn or typed message falls between
type Input struct {
	// When the newest of the calls was made
	At time.Time `json:"at"`
	// When the turn the calls belong to began and zero when no turn was recorded before them
	// A reader joins calls to a run by it once retention removed the turn traces between them
	Turn  time.Time `json:"turn,omitzero"`
	Calls Shapes    `json:"calls"`
}

// How far a turn start read from a duration in whole milliseconds may lie from the turn a calls trace names
const TurnSlack = 2 * time.Millisecond

// Whether the calls belong to the turn that began then
// Calls or a turn with no time recorded belong to any
func (in Input) InTurn(turn time.Time) bool {
	return in.Turn.IsZero() || turn.IsZero() || !in.Turn.Before(turn.Add(-TurnSlack)) && !in.Turn.After(turn.Add(TurnSlack))
}

// The inputs of calls traces
type Inputs []Input

// The first Kept shapes
func (ss Shapes) Cut() Shapes {
	if len(ss) > Kept {
		return ss[:Kept]
	}
	return ss
}

// The shapes of a call by code alone so a recorded call and a live one shape the same
// 1. a string command gives one shape per command line and a scope line none
// 2. any other call gives one shape with the file it names
func Of(tool string, input map[string]any) Shapes {
	if !Tool(tool) {
		return nil
	}
	file := shapeOf(input)
	command, ok := input["command"].(string)
	if !ok {
		return Shapes{{Tool: tool, File: file}}
	}
	var out Shapes
	for _, line := range strings.Split(veto.Command(command).Lines(), "\n") {
		if line == "(" || line == ")" {
			continue
		}
		out = out.With(Shape{Tool: tool, Command: head(line), File: file})
	}
	return out
}

// The shapes with s added once
func (ss Shapes) With(s Shape) Shapes {
	if slices.Contains(ss, s) {
		return ss
	}
	return append(ss, s)
}

// The program of a line and the longest subcommand path it defines that the line starts with
// 1. the line is parsed again so a quoted value stays one argument and never splits into words
// 2. a program the list lacks keeps nothing
func head(line string) string {
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		return ""
	}
	var args []*syntax.Word
	syntax.Walk(file, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.CallExpr); ok && args == nil {
			args = c.Args
		}
		return args == nil
	})
	if len(args) == 0 {
		return ""
	}
	name := path.Base(args[0].Lit())
	p, known := programs[name]
	if !known {
		return ""
	}
	if sub := p.path(args[1:]); sub != "" {
		return name + " " + sub
	}
	return name
}

// The longest subcommand path the program defines that the arguments start with and empty for none
// 1. before the subcommand a flag holding its value after = is skipped alone and a value flag of the program with its value
// Any other flag ends it since a boolean flag would make the next value read as the subcommand
// 2. only a path the program lists is kept so an argument value never lands in a shape
func (p program) path(args []*syntax.Word) string {
	var said []string
	for i := 0; i < len(args) && len(said) < 2; i++ {
		lit := args[i].Lit()
		switch {
		case len(said) == 0 && strings.HasPrefix(lit, "-") && strings.Contains(lit, "="):
		case len(said) == 0 && slices.Contains(p.values, lit):
			i++
		case word.MatchString(lit):
			said = append(said, lit)
		default:
			i = len(args)
		}
	}
	for n := len(said); n > 0; n-- {
		if sub := strings.Join(said[:n], " "); slices.Contains(p.commands, sub) {
			return sub
		}
	}
	return ""
}

// The shape of the file a call names and empty when it names none a shape holds
func shapeOf(input map[string]any) string {
	for _, field := range fileFields {
		name, ok := input[field].(string)
		if !ok || name == "" {
			continue
		}
		base := path.Base(strings.ReplaceAll(name, `\`, "/"))
		dot := strings.LastIndexByte(base, '.')
		if dot < 0 {
			return ""
		}
		base = base[:dot] + strings.ToLower(base[dot:])
		for _, f := range fileShapes {
			if m := f.pattern.FindStringSubmatch(base); m != nil {
				return f.shape + m[1]
			}
		}
		return ""
	}
	return ""
}

// Whether the name is a tool name a shape keeps
func Tool(name string) bool {
	return toolName.MatchString(name)
}

// Whether the words are a listed program alone or with a subcommand path it defines
func Command(words string) bool {
	name, sub, spaced := strings.Cut(words, " ")
	p, known := programs[name]
	return known && (!spaced || slices.Contains(p.commands, sub))
}

// Whether the shape is one a file takes
func File(shape string) bool {
	return fileShape.MatchString(shape)
}

// Whether the command of the shape starts with the words
func (s Shape) Starts(words string) bool {
	return s.Command == words || strings.HasPrefix(s.Command, words+" ")
}

// Whether the file of the shape matches the file shape
// A test shape matches its plain shape so *.go fits a *_test.go call
func (s Shape) Matches(file string) bool {
	matched, err := path.Match(file, s.File)
	return err == nil && matched && s.File != ""
}
