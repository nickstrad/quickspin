package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

type outputFormat string

const (
	outputTable outputFormat = "table"
	outputJSON  outputFormat = "json"
	outputYAML  outputFormat = "yaml"
)

func (f *outputFormat) Set(value string) error {
	switch outputFormat(value) {
	case outputTable, outputJSON, outputYAML:
		*f = outputFormat(value)
		return nil
	default:
		return fmt.Errorf("unsupported output format %q (valid formats: table, json, yaml)", value)
	}
}

func (f *outputFormat) String() string {
	return string(*f)
}

func (f *outputFormat) Type() string {
	return "format"
}

func completeOutputFormat(
	_ *cobra.Command,
	_ []string,
	_ string,
) ([]string, cobra.ShellCompDirective) {
	return []string{
		"table\taligned columns for people",
		"json\tJSON for programs",
		"yaml\tYAML for people or programs",
	}, cobra.ShellCompDirectiveNoFileComp
}
