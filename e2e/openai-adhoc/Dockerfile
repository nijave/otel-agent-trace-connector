FROM python:3.13-slim

COPY requirements.txt /tmp/requirements.txt
RUN pip install --no-cache-dir --requirement /tmp/requirements.txt

RUN mkdir -p /work
COPY agent.py /work/agent.py
COPY run.sh /usr/local/bin/run-openai-e2e
RUN chmod 0555 /usr/local/bin/run-openai-e2e

WORKDIR /work
ENTRYPOINT ["/usr/local/bin/run-openai-e2e"]
