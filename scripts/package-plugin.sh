#!/usr/bin/env bash
set -euo pipefail

lib_dir="${1:?library directory is required}"
lib_name="${2:?library name is required}"
archive_name="${3:?archive name is required}"
repo_root="${GITHUB_WORKSPACE:-$(pwd)}"
plugin_id="${PLUGIN_ID:-cpa-key-quota}"

rm -f "${lib_dir}/${plugin_id}.h"

if [[ "${RUNNER_OS:-}" == "Windows" ]]; then
	powershell -NoProfile -Command "Compress-Archive -Path '${lib_dir}/${lib_name}' -DestinationPath '${archive_name}'"
	# Write the checksum as plain ASCII: Windows PowerShell 5.1 redirection can
	# emit UTF-16, which breaks the release job's checksum validation.
	hash="$(powershell -NoProfile -Command "(Get-FileHash -Algorithm SHA256 '${archive_name}').Hash.ToLower()")"
	printf '%s  %s\n' "${hash}" "${archive_name}" > "${archive_name}.sha256"
else
	(
		cd "${lib_dir}"
		zip -r "${repo_root}/${archive_name}" "${lib_name}"
	)
	sha256sum "${archive_name}" > "${archive_name}.sha256"
fi
