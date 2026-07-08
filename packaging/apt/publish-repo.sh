#!/usr/bin/env bash
# Publish/refresh the Zester APT repository tree served at https://zester.cc/repo.
#
# Regenerates dists/ metadata from every .deb in pool/, signs the Release with
# the imported GPG key, and exports the public key. The tree is stateless apart
# from the pool: everything under dists/ is regenerated on each run, so the pool
# of .deb files (persisted on the `apt-repo` branch) is the sole source of
# truth. Safe to re-run — the same pool produces the same tree.
#
# Usage:
#   packaging/apt/publish-repo.sh <repo-dir> [<deb-dir>]
#     <repo-dir>  root of the served repo tree (holds pool/, dists/, keys)
#     <deb-dir>   optional dir of freshly built *.deb to stage into the pool
#
# Env:
#   GPG_KEY_ID     fingerprint/key id of the signing key           (required)
#   GPG_PASSPHRASE passphrase for the signing key, loopback-signed (optional)
#   APT_ORIGIN     Release Origin + Label                          (default: Zester)
#   APT_SUITE      suite + codename                                (default: stable)
#   APT_COMPONENT  component                                       (default: main)
#   APT_ARCHS      space-separated architectures                   (default: amd64)
#
# Requires: apt-utils (apt-ftparchive), gnupg, gzip.
set -euo pipefail

repo_dir=${1:?usage: publish-repo.sh <repo-dir> [<deb-dir>]}
deb_dir=${2:-}

: "${GPG_KEY_ID:?GPG_KEY_ID is required}"
origin=${APT_ORIGIN:-Zester}
suite=${APT_SUITE:-stable}
component=${APT_COMPONENT:-main}
archs=${APT_ARCHS:-amd64}

mkdir -p "$repo_dir/pool/$component"

# Stage newly built packages into the pool (idempotent — same filename overwrites).
if [ -n "$deb_dir" ]; then
  shopt -s nullglob
  debs=("$deb_dir"/*.deb)
  if [ ${#debs[@]} -gt 0 ]; then
    cp -f "${debs[@]}" "$repo_dir/pool/$component/"
    printf 'staged %d package(s) into pool/%s\n' "${#debs[@]}" "$component"
  else
    echo "warning: no .deb files found in $deb_dir" >&2
  fi
  shopt -u nullglob
fi

cd "$repo_dir"

# Per-arch Packages index. NOTE: this scans the whole pool per arch, which is
# correct only while the pool is single-architecture (amd64 today). Adding a
# second arch to the pool requires per-arch filtering (arch-suffixed pool
# subdirs or a switch to reprepro) — see packaging/apt/README.md.
for arch in $archs; do
  dir="dists/$suite/$component/binary-$arch"
  mkdir -p "$dir"
  apt-ftparchive packages "pool/$component" > "$dir/Packages"
  gzip -9 -kf "$dir/Packages"
  printf 'wrote %s (%s package entries)\n' "$dir/Packages" "$(grep -c '^Package:' "$dir/Packages" || echo 0)"
done

# Release file covering the suite, then clear-signed InRelease + detached Release.gpg.
apt-ftparchive release \
  -o "APT::FTPArchive::Release::Origin=$origin" \
  -o "APT::FTPArchive::Release::Label=$origin" \
  -o "APT::FTPArchive::Release::Suite=$suite" \
  -o "APT::FTPArchive::Release::Codename=$suite" \
  -o "APT::FTPArchive::Release::Components=$component" \
  -o "APT::FTPArchive::Release::Architectures=$archs" \
  -o "APT::FTPArchive::Release::Description=Zester APT repository (https://zester.cc/repo)" \
  "dists/$suite" > "dists/$suite/Release"

gpg_sign=(gpg --batch --yes --pinentry-mode loopback --local-user "$GPG_KEY_ID")
[ -n "${GPG_PASSPHRASE:-}" ] && gpg_sign+=(--passphrase "$GPG_PASSPHRASE")

"${gpg_sign[@]}" --clearsign   -o "dists/$suite/InRelease"   "dists/$suite/Release"
"${gpg_sign[@]}" --detach-sign -o "dists/$suite/Release.gpg" "dists/$suite/Release"

# Public key: dearmored for `signed-by=/usr/share/keyrings/zester.gpg`, plus an
# armored copy for humans / older workflows.
gpg --export         "$GPG_KEY_ID" > zester.gpg
gpg --armor --export "$GPG_KEY_ID" > zester.asc

echo "apt repository refreshed: suite=$suite component=$component archs='$archs'"
