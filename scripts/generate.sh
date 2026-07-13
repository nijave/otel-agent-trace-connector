#!/usr/bin/env bash
# Regenerate the connector's mdatagen artifacts using the version pinned in
# internal/tools. Run after editing connector/codingagentconnector/metadata.yaml.
#
# mdatagen is pinned as a dependency of the internal/tools module (its `tool`
# directive) rather than run via `go run pkg@version`: the tagged mdatagen module
# carries replace directives that only `go run pkg@version` tries (and fails) to
# honor, whereas a dependency's replace directives are ignored, so it builds.
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}/internal/tools" || exit 1
go tool mdatagen "${repo_root}/connector/codingagentconnector/metadata.yaml"
