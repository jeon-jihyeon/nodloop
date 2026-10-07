package veto

import (
	"maps"
	"path"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// The tool_input key derived from command that holds its simple commands one per line
const FieldCommands = "commands"

// The tool_input key a shell running tool such as Bash or Monitor carries its command line in
const fieldCommand = "command"

// A tool_input as the hook reads it
type Input map[string]any

// A copy with commands derived from command when command is a string and the input brings no commands of its own
// An input without a command string comes back as it is so a tool with its own commands key keeps it
func (in Input) withCommands() Input {
	command, ok := in[fieldCommand].(string)
	if _, own := in[FieldCommands]; own || !ok {
		return in
	}
	out := maps.Clone(in)
	out[FieldCommands] = Command(command).Lines()
	return out
}

// A shell command line
type Command string

// One line per simple command in the order the shell runs them
// 1. parsed as bash then as zsh and returned as written when neither parses
// 2. a subshell or a substitution renders a line `(` before its commands and a line `)` after them
// 3. a command renders after the substitutions in its words because they run first
// 4. a line holds the name and the arguments without leading assignments or redirections
// 5. the command find runs through an exec action is a line of its own
func (c Command) Lines() string {
	for _, lang := range []syntax.LangVariant{syntax.LangBash, syntax.LangZsh} {
		file, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(string(c)), "")
		if err != nil {
			continue
		}
		var w walker
		syntax.Walk(file, w.visit)
		return strings.Join(w.lines, "\n")
	}
	return string(c)
}

// The nodes entered and not yet left so a scope closes after its last command
type walker struct {
	open  []syntax.Node
	lines []string
}

// Walk calls visit with nil after the children of every node it entered
func (w *walker) visit(n syntax.Node) bool {
	if n == nil {
		last := w.open[len(w.open)-1]
		w.open = w.open[:len(w.open)-1]
		w.leave(last)
		return true
	}
	w.open = append(w.open, n)
	if scope(n) {
		w.lines = append(w.lines, "(")
	}
	return true
}

func (w *walker) leave(n syntax.Node) {
	if scope(n) {
		w.lines = append(w.lines, ")")
		return
	}
	call, ok := n.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 {
		return
	}
	ws := make(words, len(call.Args))
	for i, a := range call.Args {
		ws[i] = newWord(a)
	}
	w.lines = append(w.lines, ws.line())
	if path.Base(ws[0].value) == "find" {
		w.lines = append(w.lines, ws[1:].executed()...)
	}
}

// A subshell or a substitution runs its commands apart from the commands around it
func scope(n syntax.Node) bool {
	switch n.(type) {
	case *syntax.Subshell, *syntax.CmdSubst, *syntax.ProcSubst:
		return true
	}
	return false
}

// One argument with its literal value and the text it renders as
type word struct {
	value string
	text  string
}

// 1. quotes and escapes are removed
// 2. a part that expands at run time renders as `$`
// 3. a word that holds a space or a quote or nothing renders quoted so the boundaries of words survive
func newWord(w *syntax.Word) word {
	var b strings.Builder
	for _, p := range w.Parts {
		switch p := p.(type) {
		case *syntax.Lit:
			b.WriteString(unescape(p.Value, ""))
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			for _, q := range p.Parts {
				if lit, ok := q.(*syntax.Lit); ok {
					b.WriteString(unescape(lit.Value, "$`\"\\\n"))
					continue
				}
				b.WriteString("$")
			}
		default:
			b.WriteString("$")
		}
	}
	v := word{value: b.String(), text: b.String()}
	if v.value == "" || strings.ContainsAny(v.value, " \t\n'\"") {
		if q, err := syntax.Quote(v.value, syntax.LangBash); err == nil {
			v.text = q
		}
	}
	return v
}

// Drops the backslash of an escape and a line continuation
// Inside double quotes a backslash escapes only the characters of only and stays before any other
func unescape(s, only string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (only == "" || strings.IndexByte(only, s[i+1]) >= 0) {
			i++
			if s[i] != '\n' {
				b.WriteByte(s[i])
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// The words of one simple command
type words []word

// The command as one line
// A command named ( or ) alone renders quoted so it never reads as the line that opens or closes a scope
func (ws words) line() string {
	texts := make([]string, len(ws))
	for i, w := range ws {
		texts[i] = w.text
	}
	line := strings.Join(texts, " ")
	if line == "(" || line == ")" {
		return "'" + line + "'"
	}
	return line
}

// The actions of find that run a command up to `;` or `+`
var execActions = []string{"-exec", "-execdir", "-ok", "-okdir"}

// The commands the exec actions of find run with the arguments after its name
func (ws words) executed() []string {
	var lines []string
	for i := 0; i < len(ws); i++ {
		if !slices.Contains(execActions, ws[i].value) {
			continue
		}
		end := i + 1
		for end < len(ws) && ws[end].value != ";" && ws[end].value != "+" {
			end++
		}
		if end > i+1 {
			lines = append(lines, ws[i+1:end].line())
		}
		i = end
	}
	return lines
}
