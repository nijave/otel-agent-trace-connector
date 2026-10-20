#!/bin/sh
set -eu

if [ -z "${LLM_API_KEY:-}" ]; then
  echo "LLM_API_KEY is required" >&2
  exit 2
fi

exec timeout --signal=TERM "${E2E_AGENT_TIMEOUT:-10m}" \
  python - <<'PY'
import os

from pydantic import SecretStr

from openhands.sdk import Agent, Conversation, LLM, Tool
from openhands.tools.terminal import TerminalTool

llm = LLM(
    usage_id="agent",
    model=os.environ["LLM_MODEL"],
    api_key=SecretStr(os.environ["LLM_API_KEY"]),
)
agent = Agent(llm=llm, tools=[Tool(name=TerminalTool.name)])
conversation = Conversation(agent=agent, workspace="/work")
conversation.send_message(
    "Use the bash tool exactly once to run 'printf openhands-otel-e2e'. "
    "Then reply with only: done."
)
conversation.run()
print("openhands e2e conversation finished")
PY
