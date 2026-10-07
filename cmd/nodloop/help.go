package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"
)

// The usage text as one block per command line
// A block starts at a line indented by two spaces and runs through the deeper lines under it
type usageText string

// The blocks of the command and the notes after them
// 1. a name with an action such as knowledge for keeps the blocks of that action
// 2. an action no block names falls back to every block of the command
// 3. a command no block names gets the whole text
func (u usageText) of(name string) string {
	head, rest, _ := strings.Cut(string(u), "commands:\n")
	commands, notes, _ := strings.Cut(rest, "\n\n")
	blocks := u.blocks(commands)
	command, _, _ := strings.Cut(name, " ")
	for _, prefix := range []string{name, command} {
		var b strings.Builder
		for _, block := range blocks {
			if block.names(prefix) {
				b.WriteString(string(block))
			}
		}
		if b.Len() > 0 {
			return "usage:\n" + b.String() + "\n" + notes
		}
	}
	return head + "commands:\n" + rest
}

func (usageText) blocks(commands string) []usageBlock {
	var blocks []usageBlock
	for line := range strings.Lines(commands) {
		starts := strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ")
		if starts || len(blocks) == 0 {
			blocks = append(blocks, "")
		}
		blocks[len(blocks)-1] += usageBlock(line)
	}
	return blocks
}

// One command line of the usage text with the lines under it
type usageBlock string

// Whether the block is the command or an action of it
func (b usageBlock) names(prefix string) bool {
	line := strings.TrimSpace(string(b))
	return line == prefix || strings.HasPrefix(line, prefix+" ")
}

// A flag set whose -h and errors print the usage of its command alone
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprint(stderr, usageText(usage).of(name)) }
	return fs
}

// The exit code of a command whose flags did not parse
// -h printed the usage of the command and succeeds while any other error printed what was wrong
func parseFailed(err error) int {
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	return 1
}

// help, -h and --help print the whole usage and help <command> the usage of that command
func runHelp(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprint(stdout, usageText(usage).of(strings.Join(args, " ")))
	return 0
}
