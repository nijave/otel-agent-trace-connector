#!/usr/bin/env bash
# Regenerate the connector's mdatagen artifacts using the version pinned in
# internal/tools. Run after editing connector/codingagentconnector/metadata.yaml.
#
# GOWORK=off: the tools module is intentionally outside the repo go.work, and the
# tagged mdatagen module's own replace directives are ignored only when it is a
# dependency of the tools module (not the main module of a `go run pkg@version`).
set -euo pipefail
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${repo_root}/internal/tools" || exit 1
GOWORK=off go tool mdatagen "${repo_root}/connector/codingagentconnector/metadata.yaml"
