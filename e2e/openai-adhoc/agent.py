"""Minimal ad-hoc agent: one chat completion against z.ai's OpenAI-compatible
endpoint, instrumented by opentelemetry-instrumentation-openai-v2. run.sh
executes this once per semconv mode; OTEL_SERVICE_NAME and
OTEL_SEMCONV_STABILITY_OPT_IN select the mode per process."""

import os

from openai import OpenAI
from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.instrumentation.openai_v2 import OpenAIInstrumentor
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor


def main() -> None:
    provider = TracerProvider()  # service.name comes from OTEL_SERVICE_NAME
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
    trace.set_tracer_provider(provider)
    OpenAIInstrumentor().instrument(tracer_provider=provider)

    client = OpenAI(base_url=os.environ["OPENAI_BASE_URL"])
    response = client.chat.completions.create(
        model=os.environ.get("E2E_OPENAI_MODEL", "glm-4.7"),
        messages=[{"role": "user", "content": "Reply with only: openai-otel-e2e"}],
        max_tokens=16,
    )
    print(response.choices[0].message.content)
    provider.force_flush()
    provider.shutdown()


if __name__ == "__main__":
    main()
