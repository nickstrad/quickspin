package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
	"gopkg.in/yaml.v3"
)

type renderer struct {
	format outputFormat
}

func (r renderer) writeInfo(w io.Writer, info runtime.Info) error {
	return r.write(w, info, infoTableRows([]runtime.Info{info}))
}

func (r renderer) writeInfos(w io.Writer, infos []runtime.Info) error {
	return r.write(w, infos, infoTableRows(infos))
}

func (r renderer) writeDestroyResult(w io.Writer, result destroyResult) error {
	return r.write(w, result, [][]string{
		{"ID", "STATUS"},
		{result.ID, result.Status},
	})
}

func (r renderer) writeFileInfos(w io.Writer, infos []runtime.FileInfo) error {
	return r.write(w, infos, fileInfoTableRows(infos))
}

func (r renderer) write(w io.Writer, value any, tableRows [][]string) error {
	switch r.format {
	case outputTable:
		return writeTable(w, tableRows)
	case outputJSON:
		return writeJSON(w, value)
	case outputYAML:
		return writeYAML(w, value)
	default:
		return fmt.Errorf("unsupported output format %q", r.format)
	}
}

func infoTableRows(infos []runtime.Info) [][]string {
	rows := make([][]string, 1, len(infos)+1)
	rows[0] = []string{"ID", "STATE", "CREATED AT"}
	for _, info := range infos {
		rows = append(rows, []string{
			info.ID,
			string(info.State),
			info.CreatedAt.Format(time.RFC3339),
		})
	}
	return rows
}

func fileInfoTableRows(infos []runtime.FileInfo) [][]string {
	rows := make([][]string, 1, len(infos)+1)
	rows[0] = []string{"PATH", "SIZE", "MODE", "IS DIR"}
	for _, info := range infos {
		rows = append(rows, []string{
			info.Path,
			fmt.Sprintf("%d", info.Size),
			info.Mode.String(),
			fmt.Sprintf("%t", info.IsDir),
		})
	}
	return rows
}

func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeYAML(w io.Writer, value any) error {
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return err
	}
	return encoder.Close()
}
