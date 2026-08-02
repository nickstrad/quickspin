package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/nickstrad/quickspin/internal/events"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"gopkg.in/yaml.v3"
)

type renderer struct {
	format outputFormat
}

func (r renderer) writeInfo(w io.Writer, info runtime.Info) error {
	return r.write(w, info, infoTableRows([]runtime.Info{info}))
}

func (r renderer) writeSandbox(w io.Writer, sbx *sandbox.Sandbox) error {
	return r.write(w, sbx, sandboxTableRows([]*sandbox.Sandbox{sbx}))
}

func (r renderer) writeSandboxes(w io.Writer, sandboxes []*sandbox.Sandbox) error {
	return r.write(w, sandboxes, sandboxTableRows(sandboxes))
}

func (r renderer) writeEvents(w io.Writer, evts []*events.Event) error {
	if evts == nil {
		evts = []*events.Event{}
	}
	return r.write(w, evts, eventTableRows(evts))
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

func sandboxTableRows(sandboxes []*sandbox.Sandbox) [][]string {
	rows := make([][]string, 1, len(sandboxes)+1)
	rows[0] = []string{"ID", "STATE", "IMAGE", "CREATED AT", "EXPIRES AT"}
	for _, sbx := range sandboxes {
		image := ""
		if sbx.Spec.Image != nil {
			image = *sbx.Spec.Image
		}
		rows = append(rows, []string{
			sbx.SandboxID,
			string(sbx.State),
			image,
			sbx.CreatedAt.Format(time.RFC3339),
			sbx.ExpiresAt.Format(time.RFC3339),
		})
	}
	return rows
}

func eventTableRows(evts []*events.Event) [][]string {
	rows := make([][]string, 1, len(evts)+1)
	rows[0] = []string{"SANDBOX ID", "FROM", "TO", "AT", "REASON"}
	for _, event := range evts {
		rows = append(rows, []string{
			event.SandboxID,
			string(event.FromState),
			string(event.ToState),
			event.At.Format(time.RFC3339),
			event.Reason,
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
