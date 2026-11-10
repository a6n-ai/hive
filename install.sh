#!/bin/sh
# Installs the hive CLI from a GitHub release. HIVE_VERSION pins a tag; the
# archive and checksums.txt come from HIVE_INSTALL_BASE_URL.
set -eu

REPO="a6n-ai/hive"
BASE_URL="${HIVE_INSTALL_BASE_URL:-https://github.com/$REPO/releases/download}"
INSTALL_DIR="${HIVE_INSTALL_DIR:-$HOME/.local/bin}"

fail() {
	printf 'install.sh: %s\n' "$*" >&2
	exit 1
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

# fetch URL FILE downloads URL to FILE with curl or wget.
fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL "$1" -o "$2"
	elif command -v wget >/dev/null 2>&1; then
		wget -qO "$2" "$1"
	else
		fail "need curl or wget to download hive"
	fi
}

sha256_of() {
	if command -v shasum >/dev/null 2>&1; then
		shasum -a 256 "$1" | cut -d' ' -f1
	elif command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | cut -d' ' -f1
	else
		fail "need shasum or sha256sum to verify hive"
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

main() {
	os=$(os_name)
	arch=$(arch_name)
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT

	tag=${HIVE_VERSION:-}
	[ -n "$tag" ] || tag=$(latest_tag "$tmp")
	[ -n "$tag" ] || fail "could not find the latest hive release"

	archive="hive_${tag#v}_${os}_${arch}.tar.gz"
	fetch "$BASE_URL/$tag/checksums.txt" "$tmp/checksums.txt"
	fetch "$BASE_URL/$tag/$archive" "$tmp/$archive"
	verify "$tmp" "$archive"

	mkdir -p "$tmp/x"
	tar -xzf "$tmp/$archive" -C "$tmp/x"
	mkdir -p "$INSTALL_DIR"
	# A rename gives the new binary a new inode, so a running hive is not overwritten.
	staged="$INSTALL_DIR/.hive.$$"
	cp "$tmp/x/hive" "$staged"
	chmod 0755 "$staged"
	mv -f "$staged" "$INSTALL_DIR/hive"

	echo "installed hive $tag to $INSTALL_DIR/hive"
	case ":$PATH:" in
		*":$INSTALL_DIR:"*) ;;
		*) echo "add $INSTALL_DIR to your PATH" ;;
	esac
	echo "run: hive setup"
}

main "$@"
