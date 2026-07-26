package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

func writeTable(w io.Writer, rows [][]string) error {
	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	for _, row := range rows {
		if _, err := fmt.Fprintln(table, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return table.Flush()
}
