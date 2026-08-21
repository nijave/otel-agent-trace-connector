"""Minimal Strands agent with one tool, exporting Strands' native OTel traces
to the shared collector. Runs in the default (legacy semconv) mode, which is
what ad-hoc Strands agents emit unless they opt in to experimental semconv."""

import os

from strands import Agent, tool
from strands.models.openai import OpenAIModel
from strands.telemetry import StrandsTelemetry


@tool
def get_marker() -> str:
    """Return the fixed e2e marker string."""
    return "strands-otel-e2e"


def main() -> None:
    StrandsTelemetry().setup_otlp_exporter()  # reads OTEL_EXPORTER_OTLP_ENDPOINT
    model = OpenAIModel(
        client_args={
            "api_key": os.environ["OPENAI_API_KEY"],
            "base_url": os.environ["OPENAI_BASE_URL"],
        },
        model_id=os.environ.get("E2E_STRANDS_MODEL", "glm-4.7"),
        params={"max_tokens": 250},
    )
    agent = Agent(
        name="strands-e2e",
        model=model,
        tools=[get_marker],
        callback_handler=None,
    )
    result = agent("Call the get_marker tool exactly once, then reply with only: done.")
    print(result)


if __name__ == "__main__":
    main()
