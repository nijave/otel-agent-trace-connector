#!/usr/bin/env python3
"""Generate one short-lived, region-bound Amazon Bedrock bearer token."""

from datetime import timedelta
import os

from aws_bedrock_token_generator import provide_token


def main() -> None:
    region = os.environ.get("AWS_REGION", "")
    if not region:
        raise SystemExit("AWS_REGION is required")
    try:
        ttl_seconds = int(os.environ.get("E2E_BEDROCK_TOKEN_TTL_SECONDS", "900"))
    except ValueError as error:
        raise SystemExit("E2E_BEDROCK_TOKEN_TTL_SECONDS must be an integer") from error
    if not 900 <= ttl_seconds <= 43_200:
        raise SystemExit("E2E_BEDROCK_TOKEN_TTL_SECONDS must be between 900 and 43200")

    token = provide_token(region=region, expiry=timedelta(seconds=ttl_seconds))
    print(token, end="")


if __name__ == "__main__":
    main()
