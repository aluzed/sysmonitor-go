#!/usr/bin/env bash
#
# Build and install sysmonitor. No privileges required: everything happens
# inside the user's home directory.
#
#   ./install.sh                      install into ~/.local/bin
#   PREFIX=/opt/sysmonitor ./install.sh  install into /opt/sysmonitor/bin
#
# If Go is not installed, it is downloaded into ~/.local/go and used only for
# this build. Nothing is added to the PATH.

set -euo pipefail

NAME=sysmonitor
GO_VERSION=1.26.5
PREFIX="${PREFIX:-$HOME/.local}"
BINDIR="$PREFIX/bin"
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

say()  { printf '\033[1;36m==\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# --- 1. find Go, or install it locally --------------------------------------

find_go() {
    if command -v go > /dev/null 2>&1; then
        command -v go
        return 0
    fi
    local c
    for c in "$HOME/.local/go/bin/go" /usr/local/go/bin/go /usr/lib/go/bin/go; do
        [[ -x $c ]] && { echo "$c"; return 0; }
    done
    return 1
}

install_go() {
    local arch
    case "$(uname -m)" in
        x86_64)          arch=amd64 ;;
        aarch64|arm64)   arch=arm64 ;;
        armv6l|armv7l)   arch=armv6l ;;
        *) die "unsupported architecture: $(uname -m) — install Go manually" ;;
    esac
    [[ $(uname -s) == Linux ]] || die "this script targets Linux"

    local tgz url tmp
    tgz="go${GO_VERSION}.linux-${arch}.tar.gz"
    url="https://go.dev/dl/${tgz}"
    tmp=$(mktemp -d)
    trap 'rm -rf "$tmp"' RETURN

    say "Go not found — downloading ${GO_VERSION} (${arch})"
    command -v curl > /dev/null || die "curl is required to download Go"
    curl -fSL --progress-bar -o "$tmp/$tgz" "$url" || die "download failed"

    mkdir -p "$HOME/.local"
    rm -rf "$HOME/.local/go"
    tar -C "$HOME/.local" -xzf "$tmp/$tgz" || die "cannot unpack the Go archive"
    echo "$HOME/.local/go/bin/go"
}

GO=$(find_go) || GO=$(install_go)
[[ -x $GO ]] || die "no Go toolchain found"
say "Go: $("$GO" version)"

# --- 2. build ---------------------------------------------------------------

VERSION=$(git -C "$SRC" describe --tags --always --dirty 2>/dev/null || echo dev)

say "building ($VERSION)"
cd "$SRC"
"$GO" build -trimpath -ldflags "-s -w -X main.version=$VERSION" \
    -o "$SRC/$NAME" ./cmd/"$NAME"

# --- 3. install -------------------------------------------------------------

mkdir -p "$BINDIR"
install -m 755 "$SRC/$NAME" "$BINDIR/$NAME"
say "installed: $BINDIR/$NAME"

case ":$PATH:" in
    *":$BINDIR:"*) ;;
    *) warn "$BINDIR is not in your PATH — add this to your ~/.bashrc:"
       printf '\n    export PATH="%s:$PATH"\n\n' "$BINDIR" ;;
esac

say "ready — run: $NAME"
