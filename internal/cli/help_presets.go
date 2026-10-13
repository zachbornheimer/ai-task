package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/zachbornheimer/ai-task/internal/proof"
)

// presetSources returns the built-in and custom registries. Custom presets
// come from config.toml; until proof.LoadConfig exists the custom registry
// is empty. This is the single call site to change when it lands.
var presetSources = func() (builtin, custom *proof.Registry, err error) {
	return proof.DefaultRegistry(), proof.NewRegistry(), nil
}

// subjected is implemented by presets that describe their SUBJECT.
type subjected interface{ Subject() string }

type presetRow struct {
	Name    string   `json:"name"`
	Subject string   `json:"subject"`
	Options []string `json:"options"`
	Size    string   `json:"size"`
	Custom  bool     `json:"custom"`
}

// addHelpPresets is the presets section of `atask help add`: built-ins,
// then custom, straight from the registries.
func addHelpPresets() ([]presetRow, error) {
	builtin, custom, err := presetSources()
	if err != nil {
		return nil, err
	}
	return append(presetRows(builtin, false), presetRows(custom, true)...), nil
}

func presetRows(r *proof.Registry, custom bool) []presetRow {
	names := r.List()
	rows := make([]presetRow, 0, len(names))
	for _, n := range names {
		p, _ := r.Lookup(n)
		row := presetRow{Name: n, Subject: "-", Options: append([]string{}, p.Options()...), Size: string(p.DefaultSize()), Custom: custom}
		if s, ok := p.(subjected); ok {
			row.Subject = s.Subject()
		}
		rows = append(rows, row)
	}
	return rows
}

func renderPresets(w io.Writer, rows []presetRow) {
	fmt.Fprintln(w, "\nPresets (--proof / --regression \"PRESET: SUBJECT [k=v]\"):")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSUBJECT\tOPTIONS\tSIZE")
	for _, r := range rows {
		opts := "-"
		if len(r.Options) > 0 {
			opts = strings.Join(r.Options, ",")
		}
		name := r.Name
		if r.Custom {
			name += " (custom)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", name, r.Subject, opts, r.Size)
	}
	tw.Flush()
}
