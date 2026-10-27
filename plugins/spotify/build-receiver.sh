#!/bin/bash
# Builds a static aarch64 musl librespot for the Mini 1. Runs on Linux (CI)
# with rustup, cargo-zigbuild and zig installed; does not access a speaker.
set -euo pipefail
BASE=$(cd "$(dirname "$0")/../.." && pwd)
WORK=${SPOTIFY_BUILD_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/alice-spotify-build}
VERSION=0.8.0
SHA256=030c5e98cc06f20283b5948ae23bdac1de0dec58dee2c5ec4c8dafc7aaa796ab
TOOLCHAIN=1.88.0
TARGET=aarch64-unknown-linux-musl
mkdir -p "$WORK"
crate="$WORK/librespot-$VERSION.crate"
test -s "$crate" || curl --fail --location --retry 3 -A "alice-homebrew-build" \
  "https://static.crates.io/crates/librespot/librespot-$VERSION.crate" -o "$crate"
echo "$SHA256  $crate" | sha256sum -c -
rm -rf "$WORK/src"
mkdir -p "$WORK/src"
tar xzf "$crate" -C "$WORK/src"
cd "$WORK/src/librespot-$VERSION"
rustup toolchain install "$TOOLCHAIN" --profile minimal --target "$TARGET"
# Pure-Rust TLS and mDNS: no OpenSSL, ALSA or Avahi on the speaker. Audio goes
# to stdout (pipe backend) and through Alice's mixer in the Go service.
cargo "+$TOOLCHAIN" zigbuild --release --locked --target "$TARGET" \
  --no-default-features --features with-libmdns,rustls-tls-webpki-roots
out="target/$TARGET/release/librespot"
file "$out" | tee /dev/stderr | grep -q 'ARM aarch64.*statically linked'
cp "$out" "$BASE/plugins/spotify/service/receiver-arm64"
# Record every Rust dependency and its license next to the plugin package.
cargo "+$TOOLCHAIN" tree --locked --target "$TARGET" --no-default-features \
  --features with-libmdns,rustls-tls-webpki-roots --edges normal --prefix none \
  --format '{p} {l}' | sort -u > "$BASE/plugins/spotify/licenses/rust-dependencies.txt"
cp LICENSE "$BASE/plugins/spotify/licenses/librespot-$VERSION-LICENSE"
# MIT/Apache/BSD require the license text to travel with the binary: copy each
# dependency's license files from the cargo registry sources used above.
rm -rf "$BASE/plugins/spotify/licenses/rust"
mkdir -p "$BASE/plugins/spotify/licenses/rust"
while read -r name version _; do
  src=$(ls -d "${CARGO_HOME:-$HOME/.cargo}"/registry/src/*/"$name-${version#v}" 2>/dev/null | head -1)
  test -n "$src" || continue
  for f in "$src"/LICENSE* "$src"/COPYING* "$src"/NOTICE*; do
    test -f "$f" && cp "$f" "$BASE/plugins/spotify/licenses/rust/$name-${version#v}-$(basename "$f")"
  done
done < "$BASE/plugins/spotify/licenses/rust-dependencies.txt"
echo SPOTIFY_RECEIVER_OK
