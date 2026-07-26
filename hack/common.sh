# Shared reporting helpers for the hack/ scripts. Sourced, never executed, so it
# sets no options of its own: `set -euo pipefail` belongs to the script that
# sources this, where a reader can see it.
#
# hack/validate-01.sh predates this file and still carries its own copies.

# Non-interactive shells (make, CI) do not always get a TTY, so only colorize
# when stdout really is a terminal.
if [[ -t 1 ]]; then
    GREEN=$'\033[32m'
    RED=$'\033[31m'
    RESET=$'\033[0m'
else
    GREEN=""
    RED=""
    RESET=""
fi

pass() {
    printf '%s✔%s %s\n' "$GREEN" "$RESET" "$1"
}

# fail prints to stderr so a caller can capture the checkmarks separately, then
# ends the script. Every check funnels through here, which is what keeps the
# output a clear message instead of a stack of raw command errors.
fail() {
    printf '%s✘%s %s\n' "$RED" "$RESET" "$1" >&2
    exit 1
}

require_command() {
    command -v "$1" >/dev/null 2>&1 || fail "${1} is not installed or not on PATH.${2:+ $2}"
}
