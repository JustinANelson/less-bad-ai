#!/bin/sh
set -eu

repository="JustinANelson/less-bad-ai"
version="${LBAI_VERSION:-latest}"
install_dir="${LBAI_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux) os="linux" ;;
  Darwin) os="darwin" ;;
  *) echo "Unsupported operating system: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  arm64|aarch64) arch="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

if [ "$version" = "latest" ]; then
  download_base="https://github.com/$repository/releases/latest/download"
  label="latest"
else
  case "$version" in v*) tag="$version" ;; *) tag="v$version" ;; esac
  download_base="https://github.com/$repository/releases/download/$tag"
  label="$tag"
fi

asset="lbai_${os}_${arch}.tar.gz"
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT INT TERM

echo "Installing less-bad-ai $label for $os/$arch..."
curl --fail --location --silent --show-error "$download_base/$asset" -o "$temporary/$asset"
curl --fail --location --silent --show-error "$download_base/checksums.txt" -o "$temporary/checksums.txt"
expected="$(awk -v asset="$asset" '$2 == asset { print $1 }' "$temporary/checksums.txt")"
[ -n "$expected" ] || { echo "Release checksums do not contain $asset" >&2; exit 1; }
if command -v sha256sum >/dev/null 2>&1; then
  actual="$(sha256sum "$temporary/$asset" | awk '{print $1}')"
else
  actual="$(shasum -a 256 "$temporary/$asset" | awk '{print $1}')"
fi
[ "$actual" = "$expected" ] || { echo "Checksum verification failed for $asset" >&2; exit 1; }

tar -xzf "$temporary/$asset" -C "$temporary"
mkdir -p "$install_dir"
install -m 0755 "$temporary/lbai" "$install_dir/lbai"
install -m 0755 "$temporary/less-bad-ai" "$install_dir/less-bad-ai"

echo "Installed lbai and less-bad-ai to $install_dir"
case ":$PATH:" in
  *":$install_dir:"*) echo "Run: lbai setup" ;;
  *) echo "Add $install_dir to PATH, then run: lbai setup" ;;
esac
