#!/usr/bin/env python3
import importlib.util
import os
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
SPEC = importlib.util.spec_from_file_location(
    "terminal_presence_collector",
    os.path.join(HERE, "terminal-presence-collector.py"),
)
collector = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(collector)


class TestAgentHint(unittest.TestCase):
    def test_full_registered_shape(self):
        self.assertEqual(
            collector.build_agent_hint("agent-kai-codex", "python3", "working on HIV-789"),
            "carrier=codex|emp=kai|task=HIV-789",
        )

    def test_employee_first_with_known_carrier(self):
        self.assertEqual(collector.detect_employee("raven-qwen"), "raven")

    def test_agent_slash_shape(self):
        self.assertEqual(collector.detect_employee("agent/raven/api/ecfa67a6"), "raven")

    def test_employee_substrings_do_not_match(self):
        for session in ("atlas-beetle", "ravenous-build", "employee-williamson"):
            with self.subTest(session=session):
                self.assertEqual(collector.detect_employee(session), "")

    def test_carrier_substrings_do_not_match(self):
        self.assertEqual(collector.detect_carrier("codexium-work", "bash", ""), "")

    def test_admitted_task_prefixes(self):
        self.assertEqual(collector.detect_task_clue("work-HIV-789", ""), "HIV-789")
        self.assertEqual(collector.detect_task_clue("work", "MUL-1234"), "MUL-1234")
        self.assertEqual(collector.detect_task_clue("hdeo-HDEO-42", ""), "HDEO-42")

    def test_http_and_error_codes_are_not_tasks(self):
        self.assertEqual(collector.detect_task_clue("work", "HTTP-500 ERROR-404"), "")

    def test_hint_never_contains_raw_secret(self):
        hint = collector.build_agent_hint(
            "agent-kai-codex", "python3", "token=sk-abcdefgh12345678 HIV-100"
        )
        self.assertNotIn("sk-", hint)
        self.assertNotIn("token=", hint)


class TestSanitize(unittest.TestCase):
    def test_redacts_common_secret_shapes(self):
        for value in (
            "key=sk-abcdefgh12345678",
            "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc",
            "password=supersecret",
            "aws_key=AKIAIOSFODNN7EXAMPLE",
        ):
            with self.subTest(value=value):
                self.assertIn("[REDACTED]", collector.sanitize(value))

    def test_strips_ansi_and_control_characters(self):
        self.assertEqual(collector.sanitize("\x1b[31ma\x1b[0m\x00b\nc\td"), "ab\nc\td")

    def test_truncates_tail(self):
        self.assertEqual(len(collector.sanitize("x" * 30000)), 20000)


if __name__ == "__main__":
    unittest.main()
