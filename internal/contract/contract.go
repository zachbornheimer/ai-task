// Package contract reads the public surface out of docs/contract.md.
package contract

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
)

const (
	cliHeading        = "# CLI"
	taskFlagsMarker   = "**Task flags**"
	projectFlagMarker = "**Project flags**"
	globalFlagsMarker = "**Global flags**"
	engineDeclaration = "type Engine interface"
	commandPrefix     = "atask"
	flagPrefix        = "--"
	fenceMarker       = "```"
)

var (
	verbWord        = regexp.MustCompile(`^[a-z]+$`)
	methodSignature = regexp.MustCompile(`^\s+([A-Z]\w*)\(`)
)

// Surface is the public CLI and Go API named by the contract.
type Surface struct {
	Verbs         []string
	TaskFlags     []string
	ProjectFlags  []string
	GlobalFlags   []string
	EngineMethods []string
}

// Parse reads the contract document at path into a Surface.
func Parse(path string) (Surface, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Surface{}, fmt.Errorf("read contract %q: %w", path, err)
	}
	return ParseText(string(raw))
}

// ParseText reads contract markdown into a Surface.
func ParseText(text string) (Surface, error) {
	lines := strings.Split(text, "\n")
	var s Surface
	sections := []struct {
		name   string
		marker string
		take   func(block []string) []string
		dst    *[]string
	}{
		{"CLI", cliHeading, verbs, &s.Verbs},
		{"task flags", taskFlagsMarker, flagNames, &s.TaskFlags},
		{"project flags", projectFlagMarker, flagNames, &s.ProjectFlags},
		{"global flags", globalFlagsMarker, flagNames, &s.GlobalFlags},
		{"Engine", engineDeclaration, engineMethods, &s.EngineMethods},
	}
	for _, sec := range sections {
		block, ok := fencedBlockAt(lines, sec.marker)
		if !ok {
			return Surface{}, fmt.Errorf("contract has no fenced block for %s (marker %q)", sec.name, sec.marker)
		}
		*sec.dst = sec.take(block)
	}
	return s, nil
}

// fencedBlockAt returns the fenced block that contains marker, or else the
// first fenced block after the line holding marker.
func fencedBlockAt(lines []string, marker string) ([]string, bool) {
	at := indexContaining(lines, marker)
	if at < 0 {
		return nil, false
	}
	if insideFence(lines, at) {
		start := at
		for !strings.HasPrefix(lines[start-1], fenceMarker) {
			start--
		}
		return blockFrom(lines, start), true
	}
	for i := at; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], fenceMarker) {
			return blockFrom(lines, i+1), true
		}
	}
	return nil, false
}

func indexContaining(lines []string, marker string) int {
	for i, l := range lines {
		if strings.Contains(l, marker) {
			return i
		}
	}
	return -1
}

func insideFence(lines []string, idx int) bool {
	open := false
	for _, l := range lines[:idx] {
		if strings.HasPrefix(l, fenceMarker) {
			open = !open
		}
	}
	return open
}

func blockFrom(lines []string, start int) []string {
	for end := start; end < len(lines); end++ {
		if strings.HasPrefix(lines[end], fenceMarker) {
			return lines[start:end]
		}
	}
	return lines[start:]
}

func verbs(block []string) []string {
	var out []string
	for _, line := range block {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] != commandPrefix {
			continue
		}
		var words []string
		for _, w := range fields[1:] {
			if !verbWord.MatchString(w) {
				break
			}
			words = append(words, w)
		}
		if len(words) > 0 {
			out = appendUnique(out, strings.Join(words, " "))
		}
	}
	return out
}

func flagNames(block []string) []string {
	var out []string
	for _, line := range block {
		fields := strings.Fields(line)
		if len(fields) > 0 && strings.HasPrefix(fields[0], flagPrefix) {
			out = appendUnique(out, fields[0])
		}
	}
	return out
}

func engineMethods(block []string) []string {
	var out []string
	for _, line := range block {
		if m := methodSignature.FindStringSubmatch(line); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

func appendUnique(list []string, item string) []string {
	if slices.Contains(list, item) {
		return list
	}
	return append(list, item)
}
