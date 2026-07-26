package runtime

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

func sandboxIDFromLabels(labels map[string]string) (string, error) {
	id := labels[labelSandboxID]
	if err := validateSandboxID(id); err != nil {
		return "", err
	}

	return id, nil
}
