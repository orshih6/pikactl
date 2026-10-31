// Package output renders command results as aligned text or JSON.
package output

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode/utf8"
)

// JSON writes v as indented JSON.
func JSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}

// Table writes rows under headers in aligned columns.
func Table(w io.Writer, headers []string, rows [][]string) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(headers, "\t"))
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

// Field is one line of a Fields listing.
type Field struct {
	Name, Value string
}

// Fields writes name/value pairs one per line, skipping empty values. A
// multi-line value is indented under its name.
func Fields(w io.Writer, fields []Field) error {
	width := 0
	for _, f := range fields {
		if f.Value != "" && len(f.Name) > width {
			width = len(f.Name)
		}
	}
	for _, f := range fields {
		if f.Value == "" {
			continue
		}
		if strings.Contains(f.Value, "\n") {
			indented := "  " + strings.ReplaceAll(f.Value, "\n", "\n  ")
			if _, err := fmt.Fprintf(w, "%s:\n%s\n", f.Name, indented); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "%-*s  %s\n", width+1, f.Name+":", f.Value); err != nil {
			return err
		}
	}
	return nil
}

// Truncate shortens s to at most max characters on one line, marking a cut
// with an ellipsis.
func Truncate(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}

// USD renders a micro-USD amount in dollars with no rounding: at least two
// decimals, and as many of the six as the amount needs ($0.20, $0.0002).
func USD(micro int64) string {
	sign := ""
	if micro < 0 {
		sign, micro = "-", -micro
	}
	frac := strings.TrimRight(fmt.Sprintf("%06d", micro%1_000_000), "0")
	for len(frac) < 2 {
		frac += "0"
	}
	return sign + "$" + strconv.FormatInt(micro/1_000_000, 10) + "." + frac
}
