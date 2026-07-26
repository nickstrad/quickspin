package runtime

import (
	"maps"
	"slices"
)

// Docker reports seven statuses — created, running, paused, restarting,
// removing, exited, dead — and quickspin exposes two, so only the started ones
// need naming. "running" is spelled literally rather than derived from
// StateRunning: the two vocabularies share the string by coincidence, and
// renaming quickspin's state must not change which Docker status we match.
const (
	dockerRunning    = "running"
	dockerRestarting = "restarting"
)

var startedStates = []string{dockerRunning, dockerRestarting}

// stateFromContainerState treats an unrecognized status as stopped: a caller who
// believes a sandbox is stopped recreates or destroys it, where one who believes
// a dead sandbox is running waits forever.
func stateFromContainerState(s string) State {
	if slices.Contains(startedStates, s) {
		return StateRunning
	}
	return StateStopped
}

// envToArgs sorts the keys, not the joined strings, which are different orders:
// "=" (0x3D) sorts after the digits and before "_", so joining first yields
// FOO2= < FOO= < FOO_A=, putting FOO between its own prefixes.
func envToArgs(env map[string]string) []string {
	args := make([]string, 0, len(env))
	for _, k := range slices.Sorted(maps.Keys(env)) {
		args = append(args, k+"="+env[k])
	}
	return args
}
