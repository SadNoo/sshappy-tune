#!/usr/bin/env bash
set -Eeuo pipefail

readonly VERSION="0.2.0"
readonly REPOSITORY="SadNoo/sshappy-tune"
readonly INSTALL_PATH="/usr/local/sbin/sshappy-tune"

bandwidth=""
rtt=""
confirmed=0

usage() {
  cat >&2 <<'EOF'
Usage:
  install.sh --bandwidth <Mbps> --rtt <ms> --confirm

The script downloads the version-pinned Linux binary, verifies its SHA-256,
installs it to /usr/local/sbin, performs the initial reconciliation, and
enables boot reconciliation plus the read-only verification timer.
EOF
}

while (($# > 0)); do
  case "$1" in
    --bandwidth)
      (($# >= 2)) || { usage; exit 2; }
      bandwidth="$2"
      shift 2
      ;;
    --bandwidth=*)
      bandwidth="${1#*=}"
      shift
      ;;
    --rtt)
      (($# >= 2)) || { usage; exit 2; }
      rtt="$2"
      shift 2
      ;;
    --rtt=*)
      rtt="${1#*=}"
      shift
      ;;
    --confirm)
      confirmed=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'Unknown argument: %s\n' "$1" >&2
      usage
      exit 2
      ;;
  esac
done

[[ "${EUID}" -eq 0 ]] || { echo "Run this installer as root." >&2; exit 1; }
[[ "${confirmed}" -eq 1 ]] || { echo "Refusing installation without --confirm." >&2; exit 1; }
[[ "${bandwidth}" =~ ^[0-9]+$ ]] && ((10#${bandwidth} >= 1 && 10#${bandwidth} <= 100000)) \
  || { echo "--bandwidth must be an integer between 1 and 100000 Mbps." >&2; exit 2; }
[[ "${rtt}" =~ ^[0-9]+$ ]] && ((10#${rtt} >= 1 && 10#${rtt} <= 2000)) \
  || { echo "--rtt must be an integer between 1 and 2000 ms." >&2; exit 2; }
[[ "$(uname -s)" == "Linux" ]] || { echo "sshappy-tune supports Linux only." >&2; exit 1; }
[[ -d /run/systemd/system ]] || { echo "systemd is required." >&2; exit 1; }

for command in curl sha256sum install ip tc sysctl modprobe systemctl; do
  command -v "${command}" >/dev/null 2>&1 || {
    printf 'Required command is missing: %s\n' "${command}" >&2
    exit 1
  }
done

case "$(uname -m)" in
  x86_64|amd64) architecture="amd64" ;;
  aarch64|arm64) architecture="arm64" ;;
  *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
esac

readonly asset="sshappy-tune-linux-${architecture}"
readonly base_url="https://github.com/${REPOSITORY}/releases/download/v${VERSION}"
temp_dir="$(mktemp -d)"
trap 'rm -rf "${temp_dir}"' EXIT
umask 077

curl --fail --silent --show-error --location --retry 3 \
  --proto '=https' --proto-redir '=https' --tlsv1.2 \
  --output "${temp_dir}/${asset}" "${base_url}/${asset}"
curl --fail --silent --show-error --location --retry 3 \
  --proto '=https' --proto-redir '=https' --tlsv1.2 \
  --output "${temp_dir}/checksums.txt" "${base_url}/checksums.txt"

checksum_line="$(awk -v name="${asset}" '$2 == name {print; found=1} END {if (!found) exit 1}' "${temp_dir}/checksums.txt")" \
  || { echo "Release checksum does not contain ${asset}." >&2; exit 1; }
(
  cd "${temp_dir}"
  printf '%s\n' "${checksum_line}" | sha256sum --check --strict -
)

install -m 0755 "${temp_dir}/${asset}" "${INSTALL_PATH}"
"${INSTALL_PATH}" apply \
  --bandwidth "${bandwidth}" \
  --rtt "${rtt}" \
  --dry-run
"${INSTALL_PATH}" service install \
  --bandwidth "${bandwidth}" \
  --rtt "${rtt}" \
  --confirm
