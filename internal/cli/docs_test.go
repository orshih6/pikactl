package cli

import (
	"os"
	"strings"
	"testing"
)

// The skill tells AI assistants what to run, and an assistant will trust it
// over checking. These tests fail when it, or the README, mentions a command
// or flag that does not exist.
func TestDocumentedCommandsExist(t *testing.T) {
	for _, path := range []string{"../../skills/pikactl/SKILL.md", "../../README.md"} {
		commands := documentedCommands(t, path)
		if len(commands) < 5 {
			t.Fatalf("%s: found only %d commands, so this test has stopped reading it", path, len(commands))
		}
		root := newRootCommand()
		for _, line := range commands {
			args := strings.Fields(line)[1:]
			found, rest, err := root.Find(args)
			if err != nil || found == root {
				t.Errorf("%s: %q is not a command", path, line)
				continue
			}
			// run and quote take flags from the live schema, so only theirs can't be checked.
			if found.DisableFlagParsing {
				continue
			}
			for _, arg := range rest {
				name, ok := strings.CutPrefix(arg, "--")
				if !ok {
					continue
				}
				name, _, _ = strings.Cut(name, "=")
				if found.Flags().Lookup(name) == nil && found.InheritedFlags().Lookup(name) == nil {
					t.Errorf("%s: %q uses --%s, which %s does not have", path, line, name, found.CommandPath())
				}
			}
		}
	}
}

// documentedCommands reads the pikactl lines out of a Markdown file's fenced
// code blocks, joining continuations and dropping comments.
func documentedCommands(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	inCode, continuing := false, false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
			continue
		}
		if !inCode {
			continue
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case continuing:
			commands[len(commands)-1] += " " + strings.TrimSuffix(trimmed, `\`)
		case strings.HasPrefix(trimmed, "pikactl "):
			text, _, _ := strings.Cut(trimmed, " # ")
			commands = append(commands, strings.TrimSuffix(strings.TrimSpace(text), `\`))
		default:
			continue
		}
		continuing = strings.HasSuffix(trimmed, `\`)
	}
	return commands
}
