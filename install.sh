#!/bin/sh
# Installs the inscribe CLI from a GitHub release. INSCRIBE_VERSION pins a tag; the
# archive and checksums.txt come from INSCRIBE_INSTALL_BASE_URL.
set -eu

REPO="a6n-ai/inscribe"
BASE_URL="${INSCRIBE_INSTALL_BASE_URL:-https://github.com/$REPO/releases/download}"
INSTALL_DIR="${INSCRIBE_INSTALL_DIR:-$HOME/.local/bin}"

case "$BASE_URL" in
	http://*) echo "install.sh: refusing plain http download from $BASE_URL; use https" >&2; exit 1 ;;
esac

fail() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
}

# say prints a step to stderr, so stdout keeps only the final result lines.
say() {
	printf '==> %s\n' "$*" >&2
}

os_name() {
	case "$(uname -s)" in
		Darwin) echo darwin ;;
		Linux) echo linux ;;
		*) fail "unsupported operating system: $(uname -s)" ;;
	esac
}

arch_name() {
	case "$(uname -m)" in
		x86_64 | amd64) echo amd64 ;;
		arm64 | aarch64) echo arm64 ;;
		*) fail "unsupported architecture: $(uname -m)" ;;
	esac
}

# fetch URL FILE downloads URL to FILE with curl or wget. Downloads stay on
# https (file is allowed for local fixtures); a redirect cannot downgrade them.
fetch() {
	# A progress bar only when a person is watching stderr.
	if [ -t 2 ]; then cprog=--progress-bar wprog=--show-progress; else cprog=-s wprog=; fi
	if command -v curl >/dev/null 2>&1; then
		curl --proto '=https,file' --tlsv1.2 --connect-timeout 20 --retry 2 -fSL $cprog "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		case "$1" in
			https://*) wget --https-only -q $wprog -O "$2" "$1" ;;
			*) wget -q $wprog -O "$2" "$1" ;;
		esac
	else
		fail "need curl or wget to download inscribe"
	fi
}

sha256_of() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		fail "need shasum or sha256sum to verify inscribe"
	fi
}

latest_tag() {
	fetch "https://api.github.com/repos/$REPO/releases/latest" "$1/latest.json"
	sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$1/latest.json" | head -n 1
}

verify() {
	want=$(awk -v f="$2" '$2 == f { print $1 }' "$1/checksums.txt")
	[ -n "$want" ] || fail "checksums.txt has no entry for $2"
	got=$(sha256_of "$1/$2")
	[ "$got" = "$want" ] || fail "checksum mismatch for $2"
}

# uninstall removes hooks and the MCP server through the binary itself, then the
# binary. purge (non-empty) also deletes all Inscribe data.
uninstall() {
	bin="$INSTALL_DIR/inscribe"
	[ -x "$bin" ] || bin=$(command -v inscribe 2>/dev/null || true)
	[ -n "$bin" ] || fail "inscribe is not installed in $INSTALL_DIR or on PATH"
	say "removing hooks and the MCP server"
	if [ -n "$1" ]; then "$bin" uninstall --purge; else "$bin" uninstall; fi
	rm -f "$bin"
	echo "removed $bin"
}

main() {
	purge=
	mode=install
	for arg in "$@"; do
		case "$arg" in
			--uninstall) mode=uninstall ;;
			--purge) purge=1 ;;
			*) fail "unknown option: $arg (use --uninstall [--purge])" ;;
		esac
	done
	[ -z "$purge" ] || [ "$mode" = uninstall ] || fail "--purge only goes with --uninstall"
	if [ "$mode" = uninstall ]; then
		uninstall "$purge"
		return
	fi

	os=$(os_name)
	arch=$(arch_name)
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	say "platform: $os/$arch"

	tag=${INSCRIBE_VERSION:-}
	if [ -z "$tag" ]; then
		say "finding the latest release"
		tag=$(latest_tag "$tmp")
	fi
	[ -n "$tag" ] || fail "could not find the latest inscribe release"
	say "version: $tag"

	if [ -x "$INSTALL_DIR/inscribe" ]; then
		case "$("$INSTALL_DIR/inscribe" version 2>/dev/null)" in
			"inscribe ${tag#v} "*)
				echo "inscribe $tag is already up to date in $INSTALL_DIR"
				return
				;;
		esac
	fi

	archive="inscribe_${tag#v}_${os}_${arch}.tar.gz"
	say "downloading checksums.txt"
	fetch "$BASE_URL/$tag/checksums.txt" "$tmp/checksums.txt"
	say "downloading $archive"
	fetch "$BASE_URL/$tag/$archive" "$tmp/$archive"
	say "verifying sha256"
	verify "$tmp" "$archive"

	say "installing to $INSTALL_DIR"
	mkdir -p "$tmp/x"
	tar -xzf "$tmp/$archive" -C "$tmp/x"
	mkdir -p "$INSTALL_DIR"
	# A rename gives the new binary a new inode, so a running inscribe is not overwritten.
	staged="$INSTALL_DIR/.inscribe.$$"
	cp "$tmp/x/inscribe" "$staged"
	chmod 0755 "$staged"
	mv -f "$staged" "$INSTALL_DIR/inscribe"

	echo "installed inscribe $tag to $INSTALL_DIR/inscribe"
	case ":$PATH:" in
		*":$INSTALL_DIR:"*) ;;
		*) echo "add $INSTALL_DIR to your PATH" ;;
	esac
	echo "run: inscribe setup"
}

main "$@"
