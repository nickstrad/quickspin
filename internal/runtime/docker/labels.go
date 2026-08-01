package docker

import "github.com/nickstrad/quickspin/internal/runtime"

// A wire format, not an implementation detail: renaming one of these orphans
// every container already labelled with the old value.
const (
	labelPrefix       = "quickspin."
	labelSandboxID    = labelPrefix + "id"
	labelManaged      = labelPrefix + "managed"
	labelManagedValue = "true"
)

// managedLabels returns the labels for container.Config. Docker cannot add
// labels after create, so omitting one here hides the container from List for
// the rest of its life. The map is fresh per call because the SDK keeps it.
func managedLabels(id string) map[string]string {
	return map[string]string{
		labelSandboxID: id,
		labelManaged:   labelManagedValue,
	}
}

// isManaged decides ownership on the managed marker alone, so a container
// labelled with a bad id is still ours and stays visible to List and the leak
// check. Rejecting the id is sandboxIDFromLabels' job.
func isManaged(m map[string]string) bool {
	return m[labelManaged] == labelManagedValue
}

// managedMarkerLabels returns the ownership marker alone, for the lookups that
// want every sandbox rather than a named one. It exists so the marker's key and
// value are still spelled only in this file.
func managedMarkerLabels() map[string]string {
	return map[string]string{labelManaged: labelManagedValue}
}

// managedSandboxID answers "is this container mine, and what is it called?" in
// one step, which is the only form either caller wants. A container we own whose
// id label is unreadable reports false: it cannot be named, so it cannot be
// acted on.
func managedSandboxID(labels map[string]string) (string, bool) {
	if !isManaged(labels) {
		return "", false
	}
	id, err := sandboxIDFromLabels(labels)
	if err != nil {
		return "", false
	}
	return id, true
}

func sandboxIDFromLabels(labels map[string]string) (string, error) {
	id := labels[labelSandboxID]
	if err := runtime.ValidateSandboxID(id); err != nil {
		return "", err
	}

	return id, nil
}
