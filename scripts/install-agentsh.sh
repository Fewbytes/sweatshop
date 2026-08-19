#!/bin/bash
# Installs the agentsh and agentshd binaries from a GitHub Release
# (github.com/Fewbytes/sweatshop). This is the "prebuilt" install path — see
# `just install` for the build-from-source path, and
# packages/agentsh/hooks/check-agentshd.sh for where this gets suggested.
#
# Usage:
#   ./install-agentsh.sh [--dest DIR] [--version TAG]
#   curl -fsSL https://raw.githubusercontent.com/Fewbytes/sweatshop/master/scripts/install-agentsh.sh | bash
#
# Env vars (equivalent to the flags, for the curl|bash form):
#   AGENTSH_INSTALL_DIR   default: $HOME/.local/bin
#   AGENTSH_VERSION       default: latest
set -euo pipefail

repo="Fewbytes/sweatshop"
dest="${AGENTSH_INSTALL_DIR:-$HOME/.local/bin}"
version="${AGENTSH_VERSION:-latest}"

while [ $# -gt 0 ]; do
	case "$1" in
	--dest)
		dest="$2"
		shift 2
		;;
	--version)
		version="$2"
		shift 2
		;;
	*)
		echo "unknown argument: $1" >&2
		exit 1
		;;
	esac
done

goos="$(uname -s)"
goarch="$(uname -m)"
case "$goos" in
Linux) goos=linux ;;
Darwin) goos=darwin ;;
*)
	echo "agentsh: unsupported OS $goos (supported: Linux, Darwin/macOS)" >&2
	exit 1
	;;
esac
case "$goarch" in
x86_64 | amd64) goarch=amd64 ;;
arm64 | aarch64) goarch=arm64 ;;
*)
	echo "agentsh: unsupported architecture $goarch" >&2
	exit 1
	;;
esac
# Matches the release matrix in .github/workflows/release.yml: go-libsql (the
# storage layer's CGO dependency) has no prebuilt static lib for darwin/amd64.
if [ "$goos" = "darwin" ] && [ "$goarch" = "amd64" ]; then
	echo "agentsh: darwin/amd64 (Intel Mac) is not a supported platform — see packages/agentsh/README.md" >&2
	exit 1
fi

if [ "$version" = "latest" ]; then
	api_url="https://api.github.com/repos/${repo}/releases/latest"
else
	api_url="https://api.github.com/repos/${repo}/releases/tags/${version}"
fi
tag="$(curl -fsSL "$api_url" | grep -m1 '"tag_name"' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"
if [ -z "$tag" ]; then
	echo "agentsh: could not resolve release tag from $api_url" >&2
	exit 1
fi

archive="agentsh_${tag}_${goos}_${goarch}.tar.gz"
base_url="https://github.com/${repo}/releases/download/${tag}"

work_dir="$(mktemp -d)"
trap 'rm -rf "$work_dir"' EXIT

echo "agentsh: downloading ${archive} (${tag})..."
curl -fsSL -o "${work_dir}/${archive}" "${base_url}/${archive}"
curl -fsSL -o "${work_dir}/${archive}.sha256" "${base_url}/${archive}.sha256"

echo "agentsh: verifying checksum..."
(cd "$work_dir" && shasum -a 256 -c "${archive}.sha256")

tar -xzf "${work_dir}/${archive}" -C "$work_dir"
extracted_dir="${work_dir}/agentsh_${tag}_${goos}_${goarch}"

mkdir -p "$dest"
install -m 0755 "${extracted_dir}/agentsh" "${dest}/agentsh"
install -m 0755 "${extracted_dir}/agentshd" "${dest}/agentshd"

echo "agentsh: installed agentsh and agentshd ${tag} to ${dest}"
case ":$PATH:" in
*":${dest}:"*) ;;
*) echo "agentsh: ${dest} is not on PATH — add it, or set AGENTSHD_PATH=${dest}/agentshd" ;;
esac
