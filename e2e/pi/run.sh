#!/bin/sh
set -eu

if [ -z "${ANTHROPIC_AUTH_TOKEN:-}" ]; then
  echo "ANTHROPIC_AUTH_TOKEN is required (z.ai endpoint + key)" >&2
  exit 2
fi

# Workaround for the @amaster.ai/pi-telemetry 0.1.9 packaging bug (same failure
# and fix as TGYD-helige/pi issue #86): the published manifest points at a
# barrel file that crashes under pi's loader ("undefined is not an object
# (evaluating '_extension.default')"). pi materializes the package from the
# settings packages list at startup, so patch it here, idempotently. Remove
# once upstream ships a fixed manifest.
PKG="/root/.pi/agent/npm/node_modules/@amaster.ai/pi-telemetry/package.json"
if [ -f "$PKG" ]; then
  node -e '
    const fs = require("fs");
    const pkg = JSON.parse(fs.readFileSync(process.argv[1], "utf8"));
    const ext = pkg.pi && pkg.pi.extensions;
    if (typeof ext === "string" && ext.endsWith("index.js")) {
      pkg.pi.extensions = ext.replace(/index\.js$/, "extension.js");
      fs.writeFileSync(process.argv[1], JSON.stringify(pkg, null, 2));
    }
  ' "$PKG"
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  pi --no-session \
    --model "${E2E_PI_MODEL:-zai/glm-4.7}" \
    --tools bash \
    -p "Use the bash tool exactly once to run 'printf pi-otel-e2e'. Then reply with only: done."
