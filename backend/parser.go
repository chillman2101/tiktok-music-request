package main

import "strings"

type Command struct {
	Name string // "play", "skip", "clearqueue", or "" if not a command
	Arg  string
}

// ParseCommand reads a raw chat comment and figures out if it's a
// recognized command. Matching is case-insensitive and tolerant of
// extra spaces (e.g. "!Play ", "! play").
func ParseCommand(comment string) Command {
	trimmed := strings.TrimSpace(comment)
	if !strings.HasPrefix(trimmed, "!") {
		return Command{}
	}
	trimmed = strings.TrimPrefix(trimmed, "!")
	trimmed = strings.TrimSpace(trimmed)

	parts := strings.SplitN(trimmed, " ", 2)
	name := strings.ToLower(parts[0])
	arg := ""
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}

	switch name {
	case "play":
		if arg == "" {
			return Command{}
		}
		return Command{Name: "play", Arg: arg}
	case "skip":
		return Command{Name: "skip"}
	case "clearqueue":
		return Command{Name: "clearqueue"}
	default:
		return Command{}
	}
}
