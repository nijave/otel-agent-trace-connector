#!/usr/bin/env python3

from contextlib import redirect_stdout
from datetime import timedelta
import importlib.util
import io
import os
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import patch


class BedrockTokenTest(unittest.TestCase):
    @staticmethod
    def load_module(provide_token):
        dependency = types.ModuleType("aws_bedrock_token_generator")
        dependency.provide_token = provide_token
        with patch.dict(sys.modules, {"aws_bedrock_token_generator": dependency}):
            path = Path(__file__).with_name("bedrock-token.py")
            spec = importlib.util.spec_from_file_location("bedrock_token", path)
            module = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(module)
            return module

    def test_generates_region_bound_fifteen_minute_token(self):
        calls = []

        def provide_token(**kwargs):
            calls.append(kwargs)
            return "bedrock-api-key-test"

        module = self.load_module(provide_token)
        output = io.StringIO()
        with patch.dict(os.environ, {"AWS_REGION": "us-east-1"}, clear=True):
            with redirect_stdout(output):
                module.main()

        self.assertEqual("bedrock-api-key-test", output.getvalue())
        self.assertEqual(
            [{"region": "us-east-1", "expiry": timedelta(seconds=900)}], calls
        )

    def test_rejects_out_of_range_ttl_without_generating_token(self):
        module = self.load_module(lambda **_: self.fail("token should not be generated"))
        environment = {
            "AWS_REGION": "us-east-1",
            "E2E_BEDROCK_TOKEN_TTL_SECONDS": "899",
        }
        with patch.dict(os.environ, environment, clear=True):
            with self.assertRaisesRegex(SystemExit, "between 900 and 43200"):
                module.main()


if __name__ == "__main__":
    unittest.main()
