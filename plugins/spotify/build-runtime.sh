#!/usr/bin/env bash
# Builds service/runtime: the arm64 glibc that Spotify Soloist needs (the
# speaker has glibc 2.23, Soloist needs 2.38), the libpulse shim that hands
# Soloist's audio to the plugin, and a CA bundle. Soloist itself is not
# bundled: Spotify does not allow redistribution, so the plugin downloads it
# from Spotify on the speaker.
#
# Needs: curl, dpkg-deb, sha256sum and zig 0.15.1 (python3 -m ziglang).
set -euo pipefail
cd "$(dirname "$0")"
plugin=$PWD
out=$plugin/service/runtime
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Debian 13 (trixie) packages, pinned by version and SHA-256.
mirror=https://deb.debian.org/debian
packages=(
  "pool/main/g/glibc/libc6_2.41-12+deb13u4_arm64.deb 8784eda966b189c777a384dac5ce009e8fc9b52d006926c5a013e7fa8aa688cc"
  "pool/main/g/gcc-14/libatomic1_14.2.0-19_arm64.deb 7ccff90234484bf21ae5ecb79d25b6a84b2f4038f65dd139085b2e3a937cd8dc"
  "pool/main/g/gcc-14/libgcc-s1_14.2.0-19_arm64.deb 1108bc87879833d6d9a145f22a4a15cddb34e065b4b5f4b97bee586adbac2851"
  "pool/main/g/gcc-14/gcc-14-base_14.2.0-19_arm64.deb 34ee90679b018c0e64234747a4c4c0ae6b7f63541115037465a8627c2dfbc594"
  "pool/main/c/ca-certificates/ca-certificates_20250419_all.deb ef590f89563aa4b46c8260d49d1cea0fc1b181d19e8df3782694706adf05c184"
)
for entry in "${packages[@]}"; do
  read -r path sum <<<"$entry"
  file=$work/$(basename "$path")
  curl -fsSL --retry 3 -o "$file" "$mirror/$path"
  echo "$sum  $file" | sha256sum -c --quiet
  dpkg-deb -x "$file" "$work/root"
done

# Keep the committed README; everything else here is generated.
mkdir -p "$out" && find "$out" -mindepth 1 ! -name README.md -delete
mkdir -p "$out/lib" "$out/licenses" "$plugin/licenses/runtime"
libs=$work/root/usr/lib/aarch64-linux-gnu
for f in ld-linux-aarch64.so.1 libc.so.6 libm.so.6 libpthread.so.0 libdl.so.2 librt.so.1 \
         libresolv.so.2 libnss_dns.so.2 libnss_files.so.2 libatomic.so.1 libgcc_s.so.1; do
  cp -L "$libs/$f" "$out/lib/$f"
done
cat "$work"/root/usr/share/ca-certificates/mozilla/*.crt > "$out/ca-certificates.crt"

# The shim only uses symbols every glibc since 2.17 has, so it links against
# the oldest one; it is loaded by the bundled 2.41.
ZIG_LOCAL_CACHE_DIR=$work/zig python3 -m ziglang cc -target aarch64-linux-gnu.2.17 -shared -fPIC -O2 -Wall -Wextra -Werror \
  -fvisibility=hidden -Wl,-soname,libpulse.so.0 -s -o "$out/lib/libpulse.so.0" pulse-shim/pulse.c -lpthread -lm

# libatomic1 and libgcc-s1 share the GCC copyright file from gcc-14-base.
for p in libc6 gcc-14-base ca-certificates; do
  cp "$work/root/usr/share/doc/$p/copyright" "$plugin/licenses/runtime/$p-copyright"
  cp "$work/root/usr/share/doc/$p/copyright" "$out/licenses/$p-copyright"
done
(cd "$out" && find . -type f | sort | xargs sha256sum) > "$plugin/licenses/runtime/SHA256SUMS"
du -sh "$out"
