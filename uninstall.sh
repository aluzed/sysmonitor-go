#!/usr/bin/env bash
#
# Uninstall sysmonitor. Touches nothing else, Go included.
#
#   ./uninstall.sh
#   PREFIX=/opt/sysmonitor ./uninstall.sh

set -euo pipefail

NAME=sysmonitor
PREFIX="${PREFIX:-$HOME/.local}"
BINDIR="$PREFIX/bin"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

if [[ -e $BINDIR/$NAME || -L $BINDIR/$NAME ]]; then
    rm -f "$BINDIR/$NAME"
    echo "removed: $BINDIR/$NAME"
else
    echo "nothing to remove in $BINDIR"
fi

if [[ -f $SRC/$NAME ]]; then
    rm -f "$SRC/$NAME"
    echo "removed: $SRC/$NAME (built binary)"
fi

echo "If install.sh downloaded Go, it stays in ~/.local/go — delete it by hand if you no longer want it."
