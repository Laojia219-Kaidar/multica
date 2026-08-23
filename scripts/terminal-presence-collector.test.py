#!/usr/bin/env python3
import contextlib
import importlib.util
import io
import json
import os
import stat
import subprocess
import sys
import tempfile
import unittest
import unittest.mock as mock

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
            "authorization: bearer eyJhbGciOiJIUzI1NiJ9.lower",
            "task_token=mul_0123456789abcdef",
            "daemon_token=mdt_0123456789abcdef",
            "agent_token=mat_0123456789abcdef",
            "password=supersecret",
            "aws_key=AKIAIOSFODNN7EXAMPLE",
        ):
            with self.subTest(value=value):
                self.assertIn("[REDACTED]", collector.sanitize(value))

    def test_strips_ansi_and_control_characters(self):
        self.assertEqual(collector.sanitize("\x1b[31ma\x1b[0m\x00b\nc\td"), "ab\nc\td")

    def test_strips_osc_sequences(self):
        self.assertEqual(
            collector.sanitize("before\x1b]0;secret-title\x07middle\x1b]2;other\x1b\\after"),
            "beforemiddleafter",
        )

    def test_truncates_tail(self):
        self.assertEqual(len(collector.sanitize("x" * 30000)), 20000)


# --- Prime observation (opt-in, read-only projection) ----------------------

SAMPLE_SESSION = {
    "sessionId": "agent-run-6fce4bd4bb2a",
    "model": "qwen3.8-max",
    "cwd": "/Users/jiawei/work/hivecosm",
    "taskState": "working",
    "workerState": "streaming",
    "isSessionActive": False,
    "isRunningTools": True,
    "isStreaming": False,
    "unfinishedActionCount": 2,
    "lastActivityAt": "2026-08-23T07:35:47Z",
}

SAMPLE_PRIME_PANE = {
    "session_name": "prime-6fce4bd4bb2a",
    "window_index": 0,
    "pane_index": 0,
    "pane_pid": 0,
    "current_command": "qwen3.8-max /Users/jiawei/work/hivecosm",
    "agent_hint": "carrier=prime|non-authoritative",
    "tail_text": "",
}

SAMPLE_TMUX_PANE = {
    "session_name": "agent-kai-codex",
    "window_index": 1,
    "pane_index": 0,
    "pane_pid": 1234,
    "current_command": "python3",
    "agent_hint": "carrier=codex|emp=kai",
    "tail_text": "ok",
}

EXPECTED_PANE_KEYS = {
    "session_name", "window_index", "pane_index", "pane_pid",
    "current_command", "agent_hint", "tail_text",
}


def payload_bytes(sessions=None, active=None):
    body = {"sessions": [dict(SAMPLE_SESSION)] if sessions is None else sessions}
    if active is not None:
        body["activeSessionId"] = active
    return json.dumps(body).encode("utf-8")


def prime_env(extra=None):
    env = {
        "PATH": "/nonexistent-prime-path",
        collector.PRIME_SESSIONS_ENV: "6fce4bd4bb2a",
    }
    if extra:
        env.update(extra)
    return env


class TestPrimeConfigParsing(unittest.TestCase):
    def test_unset_and_blank_mean_no_prime(self):
        self.assertEqual(collector.configured_prime_session_ids({}), [])
        self.assertEqual(
            collector.configured_prime_session_ids({collector.PRIME_SESSIONS_ENV: " ,  "}),
            [],
        )

    def test_parses_normalizes_and_dedupes(self):
        env = {collector.PRIME_SESSIONS_ENV: " 6fce4bd4bb2a ,ABCdef123456\tdeadbeef9999 6fce4bd4bb2a"}
        self.assertEqual(
            collector.configured_prime_session_ids(env),
            ["6fce4bd4bb2a", "abcdef123456", "deadbeef9999"],
        )

    def test_invalid_entries_are_rejected(self):
        raw = ",".join(["ab", "x" * 100, "bad!id", "-lead1234", "trail1234-", "ok123456"])
        env = {collector.PRIME_SESSIONS_ENV: raw}
        self.assertEqual(collector.configured_prime_session_ids(env), ["ok123456"])

    def test_non_string_input_is_rejected(self):
        self.assertEqual(collector.normalize_prime_session_id(None), "")
        self.assertEqual(collector.normalize_prime_session_id(12345), "")

    def test_empty_default_never_invokes_cli(self):
        with mock.patch.object(collector, "run_prime_cli", side_effect=AssertionError("must not call")):
            with mock.patch.object(collector, "find_prime_binary", return_value="/x/prime-agent"):
                self.assertEqual(collector.collect_prime_sessions({}), [])


class TestPrimeBinaryDiscovery(unittest.TestCase):
    def test_candidates_order_override_path_common(self):
        env = {collector.PRIME_BIN_ENV: "/custom/prime-agent", "PATH": "/only/here"}
        with mock.patch.object(collector.shutil, "which", return_value="/only/here/prime-agent") as which:
            candidates = collector.prime_binary_candidates(env)
        self.assertEqual(candidates[0], "/custom/prime-agent")
        self.assertIn("/only/here/prime-agent", candidates)
        self.assertIn("/opt/homebrew/bin/prime-agent", candidates)
        which.assert_called_once_with("prime-agent", path="/only/here")

    def test_find_returns_first_existing_executable(self):
        with tempfile.TemporaryDirectory() as tmp:
            real = os.path.join(tmp, "prime-agent")
            with open(real, "w") as handle:
                handle.write("#!/bin/sh\n")
            os.chmod(real, 0o755)
            ghost = os.path.join(tmp, "ghost")
            with mock.patch.object(collector, "prime_binary_candidates", return_value=[ghost, real]):
                self.assertEqual(collector.find_prime_binary({}), real)

    def test_non_executable_candidate_is_skipped(self):
        with tempfile.TemporaryDirectory() as tmp:
            blocked = os.path.join(tmp, "prime-agent")
            with open(blocked, "w") as handle:
                handle.write("x")
            with mock.patch.object(collector, "prime_binary_candidates", return_value=[blocked]):
                self.assertIsNone(collector.find_prime_binary({}))

    def test_absent_cli_yields_none(self):
        with mock.patch.object(collector, "prime_binary_candidates", return_value=["/no/such/prime-agent"]):
            self.assertIsNone(collector.find_prime_binary({}))


class TestPrimeTimeoutBound(unittest.TestCase):
    def test_default_when_unset(self):
        self.assertEqual(collector.prime_timeout_seconds({}), collector.PRIME_TIMEOUT_DEFAULT)

    def test_invalid_values_fall_back_to_default(self):
        for raw in ("", "abc", "nan"):
            with self.subTest(raw=raw):
                env = {collector.PRIME_TIMEOUT_ENV: raw}
                self.assertEqual(collector.prime_timeout_seconds(env), collector.PRIME_TIMEOUT_DEFAULT)

    def test_out_of_range_values_are_clamped(self):
        cases = [("0", 1.0), ("-42", 1.0), ("9999", 30.0), ("inf", 30.0), ("7.5", 7.5)]
        for raw, expected in cases:
            with self.subTest(raw=raw):
                env = {collector.PRIME_TIMEOUT_ENV: raw}
                self.assertEqual(collector.prime_timeout_seconds(env), expected)


class TestPrimeSuffixMatching(unittest.TestCase):
    def test_short_id_matches_full_official_id_by_suffix(self):
        sessions = [{"sessionId": "sess-2026-6fce4bd4bb2a"}]
        matched = collector.match_prime_sessions(sessions, ["6fce4bd4bb2a"])
        self.assertEqual(list(matched), ["sess-2026-6fce4bd4bb2a"])
        _, short_id = matched["sess-2026-6fce4bd4bb2a"]
        self.assertEqual(short_id, "6fce4bd4bb2a")

    def test_exact_full_id_matches(self):
        sessions = [{"id": "6fce4bd4bb2a"}]
        matched = collector.match_prime_sessions(sessions, ["6fce4bd4bb2a"])
        self.assertEqual(list(matched), ["6fce4bd4bb2a"])

    def test_ambiguous_suffix_matches_nothing(self):
        sessions = [
            {"sessionId": "run-one-6fce4bd4bb2a"},
            {"sessionId": "run-two-6fce4bd4bb2a"},
        ]
        self.assertEqual(collector.match_prime_sessions(sessions, ["6fce4bd4bb2a"]), {})

    def test_unmatched_short_id_yields_nothing(self):
        sessions = [{"sessionId": "sess-aaaa1111"}]
        self.assertEqual(collector.match_prime_sessions(sessions, ["6fce4bd4bb2a"]), {})

    def test_session_without_id_never_matches(self):
        self.assertEqual(collector.match_prime_sessions([{"model": "x"}], ["6fce4bd4bb2a"]), {})

    def test_matching_is_case_insensitive(self):
        sessions = [{"sessionId": "SESS-6FCE4BD4BB2A"}]
        matched = collector.match_prime_sessions(sessions, ["6fce4bd4bb2a"])
        self.assertEqual(list(matched), ["sess-6fce4bd4bb2a"])

    def test_one_session_never_projected_twice(self):
        sessions = [{"sessionId": "sess-6fce4bd4bb2a"}]
        matched = collector.match_prime_sessions(sessions, ["4bd4bb2a", "6fce4bd4bb2a"])
        self.assertEqual(len(matched), 1)


class TestPrimeResolutionTriState(unittest.TestCase):
    """Each configured id resolves to exactly matched, missing or ambiguous."""

    def test_exact_unique_match_is_matched(self):
        id_lists = [["agent-run-aaa1111"], ["sess-bbb2222"]]
        self.assertEqual(
            collector.resolve_prime_session(id_lists, "agent-run-aaa1111"),
            (collector.PRIME_MATCHED, 0),
        )

    def test_unique_suffix_match_is_matched(self):
        id_lists = [["sess-2026-aaa1111"], ["sess-other-bbb2222"]]
        self.assertEqual(
            collector.resolve_prime_session(id_lists, "aaa1111"),
            (collector.PRIME_MATCHED, 0),
        )

    def test_zero_matches_is_missing(self):
        id_lists = [["sess-aaa1111"], ["sess-bbb2222"]]
        self.assertEqual(
            collector.resolve_prime_session(id_lists, "ccc3333"),
            (collector.PRIME_MISSING, None),
        )

    def test_ambiguous_suffix_is_ambiguous(self):
        id_lists = [["run-one-aaa1111"], ["run-two-aaa1111"]]
        self.assertEqual(
            collector.resolve_prime_session(id_lists, "aaa1111"),
            (collector.PRIME_AMBIGUOUS, None),
        )

    def test_ambiguous_exact_match_is_ambiguous(self):
        id_lists = [["aaa1111"], ["aaa1111"]]
        self.assertEqual(
            collector.resolve_prime_session(id_lists, "aaa1111"),
            (collector.PRIME_AMBIGUOUS, None),
        )

    def test_exact_wins_over_suffix_collision(self):
        id_lists = [["sess-other-01", "aaa1111"], ["sess-2026-aaa1111"]]
        self.assertEqual(
            collector.resolve_prime_session(id_lists, "aaa1111"),
            (collector.PRIME_MATCHED, 0),
        )

    def test_unique_prime_session_index_still_hides_missing_and_ambiguous(self):
        id_lists = [["sess-aaa1111"], ["run-one-bbb2222"], ["run-two-bbb2222"]]
        self.assertEqual(collector.unique_prime_session_index(id_lists, "sess-aaa1111"), 0)
        self.assertIsNone(collector.unique_prime_session_index(id_lists, "ccc3333"))
        self.assertIsNone(collector.unique_prime_session_index(id_lists, "bbb2222"))


def resolve_active(session, sessions, active_session_id):
    """prime_session_is_active with the payload-wide id lists precomputed."""
    id_lists = [collector.prime_session_ids(entry) for entry in sessions]
    return collector.prime_session_is_active(session, sessions, id_lists, active_session_id)


class TestPrimeActiveSessionId(unittest.TestCase):
    def test_top_level_full_active_id(self):
        session = {"sessionId": "sess-6fce4bd4bb2a"}
        self.assertTrue(resolve_active(session, [session], "sess-6fce4bd4bb2a"))

    def test_top_level_short_active_id_uses_suffix_rule(self):
        session = {"sessionId": "sess-6fce4bd4bb2a"}
        self.assertTrue(resolve_active(session, [session], "6fce4bd4bb2a"))

    def test_per_session_bool(self):
        session = {"isSessionActive": True}
        self.assertTrue(resolve_active(session, [session], None))

    def test_per_session_active_session_id_self_reference(self):
        session = {"sessionId": "sess-6fce4bd4bb2a", "activeSessionId": "sess-6fce4bd4bb2a"}
        self.assertTrue(resolve_active(session, [session], None))

    def test_inactive_by_default(self):
        session = {"sessionId": "a-1234"}
        self.assertFalse(resolve_active(session, [session], "b-5678"))

    def test_non_bool_truthy_is_not_active(self):
        session = {"isSessionActive": "yes"}
        self.assertFalse(resolve_active(session, [session], None))

    def test_ambiguous_active_session_id_suffix_activates_nothing(self):
        sessions = [
            {"sessionId": "run-one-6fce4bd4bb2a"},
            {"sessionId": "run-two-6fce4bd4bb2a"},
        ]
        for session in sessions:
            with self.subTest(session=session["sessionId"]):
                self.assertFalse(resolve_active(session, sessions, "6fce4bd4bb2a"))
                self.assertFalse(resolve_active(session, sessions, "4bd4bb2a"))

    def test_unambiguous_active_session_id_suffix_activates_only_its_session(self):
        sessions = [
            {"sessionId": "run-one-6fce4bd4bb2a"},
            {"sessionId": "sess-other-aaaa1111"},
        ]
        self.assertTrue(resolve_active(sessions[0], sessions, "6fce4bd4bb2a"))
        self.assertFalse(resolve_active(sessions[1], sessions, "6fce4bd4bb2a"))

    def test_short_per_session_active_session_id_resolves_exactly_to_itself(self):
        # A session's own activeSessionId equals one of its id fields, so it
        # resolves exactly — never through the suffix path — to that session
        # alone, mirroring the official guarantee that an entry carrying
        # activeSessionId is live. The sibling sharing the longer suffix
        # stays inactive.
        sessions = [
            {"sessionId": "run-one-6fce4bd4bb2a", "activeSessionId": "6fce4bd4bb2a"},
            {"sessionId": "run-two-6fce4bd4bb2a"},
        ]
        self.assertTrue(resolve_active(sessions[0], sessions, None))
        self.assertFalse(resolve_active(sessions[1], sessions, None))

    def test_active_session_id_resolving_elsewhere_activates_nothing_here(self):
        sessions = [
            {"sessionId": "sess-other-aaaa1111"},
            {"sessionId": "run-one-6fce4bd4bb2a"},
        ]
        self.assertFalse(resolve_active(sessions[0], sessions, "6fce4bd4bb2a"))


class TestPrimeR5LiveContract(unittest.TestCase):
    """Deterministic regressions for the three Luna P1 live-contract gaps."""

    def collect_prime(self, payload, env=None):
        """collect_prime_sessions with the CLI mocked to return payload."""
        with mock.patch.object(collector, "find_prime_binary", return_value="/x/prime-agent"):
            with mock.patch.object(
                collector, "run_prime_cli",
                return_value=(0, json.dumps(payload).encode("utf-8")),
            ):
                return collector.collect_prime_sessions(env or prime_env())

    def test_active_session_id_only_matching_projects_the_session(self):
        # Official active entry: activeSessionId/id live agent id plus an
        # unrelated persisted sessionId. The configured short id is a suffix
        # of activeSessionId only — Atlas matched sessionId alone and missed.
        session = {
            "activeSessionId": "agent-run-6fce4bd4bb2a",
            "sessionId": "01j8-unrelated-999aaa",
            "id": "01j8-unrelated-999aaa",
        }
        panes = self.collect_prime({"sessions": [session]})
        self.assertEqual([p["session_name"] for p in panes], ["prime-6fce4bd4bb2a"])

    def test_matching_spans_all_three_official_id_fields(self):
        configured = ["6fce4bd4bb2a"]
        for key in ("activeSessionId", "sessionId", "id"):
            with self.subTest(key=key):
                session = {key: "agent-run-6fce4bd4bb2a"}
                matched = collector.match_prime_sessions([session], configured)
                self.assertEqual(len(matched), 1)
                self.assertIs(next(iter(matched.values()))[0], session)

    def test_exact_match_wins_over_suffix_collision(self):
        sessions = [
            {"sessionId": "sess-other-01", "id": "6fce4bd4bb2a"},
            {"sessionId": "sess-2026-6fce4bd4bb2a"},
        ]
        # Pure suffix rules would be ambiguous; exact-first resolves to the
        # session whose own id field equals the configured short id.
        matched = collector.match_prime_sessions(sessions, ["6fce4bd4bb2a"])
        self.assertEqual(len(matched), 1)
        session, short_id = next(iter(matched.values()))
        self.assertIs(session, sessions[0])
        self.assertEqual(short_id, "6fce4bd4bb2a")

    def test_exact_first_end_to_end_projects_exactly_one_pane(self):
        sessions = [
            {"sessionId": "sess-other-01", "id": "6fce4bd4bb2a", "cwd": "/a"},
            {"sessionId": "sess-2026-6fce4bd4bb2a", "cwd": "/b"},
        ]
        panes = self.collect_prime({"sessions": sessions})
        self.assertEqual([p["session_name"] for p in panes], ["prime-6fce4bd4bb2a"])

    def test_ambiguous_active_session_id_suffix_marks_no_session_active(self):
        sessions = [
            dict(SAMPLE_SESSION, sessionId="run-one-6fce4bd4bb2a", isSessionActive=False),
            dict(SAMPLE_SESSION, sessionId="run-two-6fce4bd4bb2a", isSessionActive=False),
        ]
        env = prime_env({collector.PRIME_SESSIONS_ENV: "run-one-6fce4bd4bb2a,run-two-6fce4bd4bb2a"})
        panes = self.collect_prime({"sessions": sessions, "activeSessionId": "6fce4bd4bb2a"}, env)
        self.assertEqual(len(panes), 2)
        for pane in panes:
            with self.subTest(pane=pane["session_name"]):
                self.assertIn("active=0", pane["agent_hint"])

    def test_unambiguous_active_session_id_suffix_still_marks_active(self):
        sessions = [
            dict(SAMPLE_SESSION, sessionId="run-one-6fce4bd4bb2a"),
            dict(SAMPLE_SESSION, sessionId="sess-other-aaaa1111"),
        ]
        env = prime_env({collector.PRIME_SESSIONS_ENV: "run-one-6fce4bd4bb2a,sess-other-aaaa1111"})
        panes = self.collect_prime({"sessions": sessions, "activeSessionId": "6fce4bd4bb2a"}, env)
        hints = {p["session_name"]: p["agent_hint"] for p in panes}
        self.assertIn("active=1", hints["prime-run-one-6fce4bd4bb2a"])
        self.assertIn("active=0", hints["prime-sess-other-aaaa1111"])

    def test_model_object_id_is_projected(self):
        session = dict(SAMPLE_SESSION, model={"provider": "zai", "id": "glm-5.3"})
        pane = collector.project_prime_session(session, "6fce4bd4bb2a", False)
        self.assertIn("glm-5.3", pane["current_command"])

    def test_model_object_provider_is_never_projected(self):
        session = dict(SAMPLE_SESSION, model={"provider": "PROVIDER-MARKER-77", "id": "glm-5.3"})
        blob = json.dumps(collector.project_prime_session(session, "6fce4bd4bb2a", False))
        self.assertNotIn("PROVIDER-MARKER-77", blob)
        self.assertNotIn("provider", blob)

    def test_model_object_end_to_end_projection(self):
        session = dict(SAMPLE_SESSION, model={"provider": "zai", "id": "glm-5.3"})
        panes = self.collect_prime({"sessions": [session]})
        self.assertEqual(len(panes), 1)
        self.assertIn("glm-5.3", panes[0]["current_command"])

    def test_legacy_string_model_shapes_still_supported(self):
        for session in (dict(SAMPLE_SESSION, modelId="kimi-k3"), dict(SAMPLE_SESSION, model="qwen3.8-max")):
            with self.subTest(session=session.get("modelId") or session.get("model")):
                pane = collector.project_prime_session(session, "6fce4bd4bb2a", False)
                self.assertIn(session.get("modelId") or session.get("model"), pane["current_command"])

    def test_model_object_without_string_id_is_dropped(self):
        for model in ({"id": 123}, {"provider": "zai"}, {}, {"id": None}):
            with self.subTest(model=model):
                pane = collector.project_prime_session(dict(SAMPLE_SESSION, model=model), "6fce4bd4bb2a", False)
                self.assertNotIn("zai", pane["current_command"])
                self.assertTrue(pane["current_command"].startswith("/"))


class TestPrimeFailOpen(unittest.TestCase):
    def collect_with_cli_result(self, result, env=None, exc=None):
        env = env or prime_env()
        with mock.patch.object(collector, "find_prime_binary", return_value="/x/prime-agent"):
            if exc is not None:
                with mock.patch.object(collector, "run_prime_cli", side_effect=exc):
                    return collector.collect_prime_sessions(env)
            with mock.patch.object(collector, "run_prime_cli", return_value=result):
                return collector.collect_prime_sessions(env)

    def test_finding1_tmux_none_does_not_suppress_prime_only(self):
        with mock.patch.object(collector, "collect", return_value=None):
            with mock.patch.object(collector, "collect_prime_sessions", return_value=[SAMPLE_PRIME_PANE]):
                self.assertEqual(collector.collect_all(), [SAMPLE_PRIME_PANE])

    def test_finding1_tmux_none_and_no_prime_keeps_legacy_skip(self):
        with mock.patch.object(collector, "collect", return_value=None):
            with mock.patch.object(collector, "collect_prime_sessions", return_value=[]):
                self.assertIsNone(collector.collect_all())

    def test_finding3_non_zero_exit_with_valid_json_yields_nothing(self):
        self.assertEqual(self.collect_with_cli_result((1, payload_bytes())), [])

    def test_finding4_bad_utf8_yields_nothing(self):
        self.assertEqual(self.collect_with_cli_result((0, b"\xff\xfe{\"sessions\": []}")), [])

    def test_timeout_yields_nothing(self):
        exc = subprocess.TimeoutExpired(cmd="prime-agent", timeout=1)
        self.assertEqual(self.collect_with_cli_result(None, exc=exc), [])

    def test_spawn_errors_yield_nothing(self):
        for exc in (FileNotFoundError("prime-agent"), PermissionError("denied"), OSError("boom")):
            with self.subTest(exc=type(exc).__name__):
                self.assertEqual(self.collect_with_cli_result(None, exc=exc), [])

    def test_malformed_json_yields_nothing(self):
        self.assertEqual(self.collect_with_cli_result((0, b"not json at all")), [])

    def test_unexpected_payload_shapes_yield_nothing(self):
        for raw in (b'"just a string"', b'{"unexpected": true}', b'{"sessions": {"a": 1}}', b"null"):
            with self.subTest(raw=raw[:24]):
                self.assertEqual(self.collect_with_cli_result((0, raw)), [])

    def test_oversized_output_yields_nothing(self):
        raw = b" " * (collector.PRIME_OUTPUT_MAX_BYTES + 1)
        self.assertEqual(self.collect_with_cli_result((0, raw)), [])

    def test_non_object_entries_invalidate_listing(self):
        sessions = ["junk", 42, dict(SAMPLE_SESSION)]
        panes = self.collect_with_cli_result((0, payload_bytes(sessions)))
        self.assertEqual(panes, [])

    def test_idless_object_invalidates_listing(self):
        panes = self.collect_with_cli_result((0, payload_bytes([{"model": "glm-5.3"}])))
        self.assertEqual(panes, [])

    def test_wrong_typed_active_session_id_invalidates_listing(self):
        raw = json.dumps({"sessions": [], "activeSessionId": 42}).encode("utf-8")
        self.assertEqual(self.collect_with_cli_result((0, raw)), [])

    def test_failures_log_no_raw_json(self):
        buffer = io.StringIO()
        with contextlib.redirect_stderr(buffer), contextlib.redirect_stdout(buffer):
            self.collect_with_cli_result((1, payload_bytes()))
            self.collect_with_cli_result((0, b"\xff\xfe"))
            self.collect_with_cli_result((0, b"{broken"))
        self.assertEqual(buffer.getvalue(), "")

    def test_absent_cli_keeps_tmux_panes(self):
        with mock.patch.object(collector, "collect", return_value=[SAMPLE_TMUX_PANE]):
            with mock.patch.object(collector, "find_prime_binary", return_value=None):
                self.assertEqual(collector.collect_all(), [SAMPLE_TMUX_PANE])


OFFLINE_SENTINEL_PANE = {
    "session_name": "prime-6fce4bd4bb2a",
    "window_index": 0,
    "pane_index": 0,
    "pane_pid": 0,
    "current_command": "offline",
    "agent_hint": "carrier=prime|non-authoritative|presence=offline",
    "tail_text": "",
}


class TestPrimeOfflineSentinel(unittest.TestCase):
    """Confirmed-absent configured sessions project a bounded offline pane."""

    def collect_prime(self, payload, env=None):
        with mock.patch.object(collector, "find_prime_binary", return_value="/x/prime-agent"):
            with mock.patch.object(
                collector, "run_prime_cli",
                return_value=(0, json.dumps(payload).encode("utf-8")),
            ):
                return collector.collect_prime_sessions(env or prime_env())

    def test_sentinel_shape_is_exact(self):
        self.assertEqual(collector.prime_offline_sentinel("6fce4bd4bb2a"), OFFLINE_SENTINEL_PANE)

    def test_confirmed_absent_session_projects_exactly_one_sentinel(self):
        payload = {"sessions": [{"sessionId": "sess-other-aaaa1111"}]}
        self.assertEqual(self.collect_prime(payload), [OFFLINE_SENTINEL_PANE])

    def test_empty_but_valid_listing_confirms_absence(self):
        self.assertEqual(self.collect_prime({"sessions": []}), [OFFLINE_SENTINEL_PANE])

    def test_matched_and_missing_ids_project_pane_and_sentinel(self):
        env = prime_env({collector.PRIME_SESSIONS_ENV: "aaaa1111,6fce4bd4bb2a"})
        panes = self.collect_prime({"sessions": [dict(SAMPLE_SESSION)]}, env)
        self.assertEqual(
            [p["session_name"] for p in panes],
            ["prime-aaaa1111", "prime-6fce4bd4bb2a"],
        )
        by_name = {p["session_name"]: p for p in panes}
        self.assertEqual(by_name["prime-aaaa1111"]["current_command"], "offline")
        self.assertIn("qwen3.8-max", by_name["prime-6fce4bd4bb2a"]["current_command"])

    def test_two_missing_ids_project_two_distinct_sentinels(self):
        env = prime_env({collector.PRIME_SESSIONS_ENV: "6fce4bd4bb2a,aaaa1111"})
        panes = self.collect_prime({"sessions": []}, env)
        self.assertEqual(
            [p["session_name"] for p in panes],
            ["prime-6fce4bd4bb2a", "prime-aaaa1111"],
        )
        for pane in panes:
            self.assertEqual(pane["current_command"], "offline")

    def test_ambiguous_id_projects_no_pane_and_no_sentinel(self):
        sessions = [
            {"sessionId": "run-one-6fce4bd4bb2a"},
            {"sessionId": "run-two-6fce4bd4bb2a"},
        ]
        self.assertEqual(self.collect_prime({"sessions": sessions}), [])

    def test_ambiguous_id_emits_no_sentinel_even_next_to_a_match(self):
        sessions = [
            {"sessionId": "run-one-6fce4bd4bb2a"},
            {"sessionId": "run-two-6fce4bd4bb2a"},
            {"sessionId": "sess-other-aaaa1111"},
        ]
        env = prime_env({collector.PRIME_SESSIONS_ENV: "6fce4bd4bb2a,aaaa1111"})
        panes = self.collect_prime({"sessions": sessions}, env)
        self.assertEqual([p["session_name"] for p in panes], ["prime-aaaa1111"])

    def test_sentinel_carries_no_payload_data(self):
        session = {"sessionId": "sess-marker-aaaa1111", "prompt": "MARKER-PROMPT-77"}
        panes = self.collect_prime({"sessions": [session]})
        self.assertNotIn("MARKER-PROMPT-77", json.dumps(panes))
        self.assertEqual(panes, [OFFLINE_SENTINEL_PANE])

    def test_failure_modes_never_emit_offline_sentinel(self):
        # A would-be-missing id must not look offline when the CLI cannot
        # prove absence: every failure mode keeps zero Prime panes.
        absent = payload_bytes(sessions=[{"sessionId": "sess-other-aaaa1111"}])
        cases = [
            ("nonzero exit", (3, absent), None),
            ("invalid json", (0, b"not json"), None),
            ("invalid schema", (0, b'{"unexpected": true}'), None),
            ("non-object session", (0, b'{"sessions": [null]}'), None),
            ("id-less session", (0, b'{"sessions": [{"model": "glm-5.3"}]}'), None),
            ("oversized output", (0, b" " * (collector.PRIME_OUTPUT_MAX_BYTES + 1)), None),
            ("bad utf-8", (0, b"\xff\xfe{}"), None),
            ("timeout", None, subprocess.TimeoutExpired(cmd="prime-agent", timeout=1)),
            ("spawn error", None, OSError("boom")),
        ]
        for label, result, exc in cases:
            with self.subTest(label=label):
                with mock.patch.object(collector, "find_prime_binary", return_value="/x/prime-agent"):
                    if exc is not None:
                        with mock.patch.object(collector, "run_prime_cli", side_effect=exc):
                            panes = collector.collect_prime_sessions(prime_env())
                    else:
                        with mock.patch.object(collector, "run_prime_cli", return_value=result):
                            panes = collector.collect_prime_sessions(prime_env())
                self.assertEqual(panes, [])

    def test_absent_cli_never_emits_offline_sentinel(self):
        with mock.patch.object(collector, "find_prime_binary", return_value=None):
            self.assertEqual(collector.collect_prime_sessions(prime_env()), [])

    def test_collect_all_keeps_tmux_panes_and_offline_sentinel(self):
        sentinel = collector.prime_offline_sentinel("6fce4bd4bb2a")
        with mock.patch.object(collector, "collect", return_value=[SAMPLE_TMUX_PANE]):
            with mock.patch.object(collector, "collect_prime_sessions", return_value=[sentinel]):
                self.assertEqual(collector.collect_all(), [SAMPLE_TMUX_PANE, sentinel])


class TestCollectAllMerge(unittest.TestCase):
    def test_tmux_panes_come_first_and_prime_appended(self):
        with mock.patch.object(collector, "collect", return_value=[SAMPLE_TMUX_PANE]):
            with mock.patch.object(collector, "collect_prime_sessions", return_value=[SAMPLE_PRIME_PANE]):
                self.assertEqual(collector.collect_all(), [SAMPLE_TMUX_PANE, SAMPLE_PRIME_PANE])

    def test_empty_tmux_with_prime_still_reports(self):
        with mock.patch.object(collector, "collect", return_value=[]):
            with mock.patch.object(collector, "collect_prime_sessions", return_value=[SAMPLE_PRIME_PANE]):
                self.assertEqual(collector.collect_all(), [SAMPLE_PRIME_PANE])


class TestPrimeProjection(unittest.TestCase):
    def test_reuses_existing_pane_shape(self):
        pane = collector.project_prime_session(SAMPLE_SESSION, "6fce4bd4bb2a", True)
        self.assertEqual(set(pane), EXPECTED_PANE_KEYS)
        self.assertEqual(pane["session_name"], "prime-6fce4bd4bb2a")
        self.assertEqual((pane["window_index"], pane["pane_index"], pane["pane_pid"]), (0, 0, 0))
        self.assertEqual(pane["tail_text"], "")

    def test_projects_only_whitelisted_fields(self):
        pane = collector.project_prime_session(SAMPLE_SESSION, "6fce4bd4bb2a", True)
        command = pane["current_command"]
        self.assertIn("qwen3.8-max", command)
        self.assertIn("/Users/jiawei/work/hivecosm", command)
        self.assertIn("2026-08-23T07:35:47Z", command)
        hint = pane["agent_hint"]
        for part in (
            "carrier=prime", "non-authoritative", "task=working", "worker=streaming",
            "active=1", "tools=1", "stream=0", "pending=2",
        ):
            self.assertIn(part, hint)
        self.assertLessEqual(len(hint), 120)

    def test_forbidden_fields_never_reach_the_pane(self):
        session = dict(SAMPLE_SESSION)
        session.update({
            "prompt": "MARKER-PROMPT-77",
            "summary": "MARKER-SUMMARY-77",
            "firstMessage": "MARKER-FIRST-77",
            "diagnostics": ["MARKER-DIAG-77"],
            "endpoint": "http://MARKER-ENDPOINT-77",
            "config": {"key": "MARKER-CONFIG-77"},
            "sessionFile": "/home/u/MARKER-FILE-77.json",
            "environment": {"MARKER-ENV-77": "1"},
            "toolArguments": {"command": "MARKER-TOOL-77"},
            "terminalTail": "MARKER-TAIL-77",
            "credentials": "MARKER-CRED-77",
            "thoughts": "MARKER-COT-77",
        })
        blob = json.dumps(collector.project_prime_session(session, "6fce4bd4bb2a", False))
        for marker in (
            "MARKER-PROMPT-77", "MARKER-SUMMARY-77", "MARKER-FIRST-77", "MARKER-DIAG-77",
            "MARKER-ENDPOINT-77", "MARKER-CONFIG-77", "MARKER-FILE-77", "MARKER-ENV-77",
            "MARKER-TOOL-77", "MARKER-TAIL-77", "MARKER-CRED-77", "MARKER-COT-77",
        ):
            self.assertNotIn(marker, blob)

    def test_no_employee_claim_in_prime_hint(self):
        pane = collector.project_prime_session(SAMPLE_SESSION, "6fce4bd4bb2a", False)
        self.assertNotIn("emp=", pane["agent_hint"])
        self.assertIn("non-authoritative", pane["agent_hint"])

    def test_bounds_long_values(self):
        session = dict(
            SAMPLE_SESSION, model="m" * 500, cwd="c" * 500,
            taskState="t" * 500, workerState="w" * 500, lastActivityAt="2" * 500,
        )
        pane = collector.project_prime_session(session, "6fce4bd4bb2a", False)
        self.assertLessEqual(len(pane["current_command"]), 120)
        self.assertLessEqual(len(pane["agent_hint"]), 120)
        self.assertLessEqual(len(pane["session_name"]), 255)

    def test_worst_case_state_hint_is_never_truncated(self):
        session = dict(
            SAMPLE_SESSION, taskState="t" * 500, workerState="w" * 500,
            unfinishedActionCount=10 ** 12,
        )
        pane = collector.project_prime_session(session, "6fce4bd4bb2a", False)
        hint = pane["agent_hint"]
        self.assertLessEqual(len(hint), 120)
        self.assertIn("pending=999999", hint)
        self.assertIn(f"task={'t' * collector.PRIME_STATE_MAX}", hint)
        self.assertIn(f"worker={'w' * collector.PRIME_STATE_MAX}", hint)

    def test_pending_count_is_bounded_and_strictly_typed(self):
        cases = ((-5, 0), (10 ** 12, 999999), ("many", 0), (None, 0), (True, 0), (3.7, 0), (4, 4))
        for value, expected in cases:
            with self.subTest(value=value):
                session = dict(SAMPLE_SESSION, unfinishedActionCount=value)
                pane = collector.project_prime_session(session, "6fce4bd4bb2a", False)
                self.assertIn(f"pending={expected}", pane["agent_hint"])

    def test_projected_values_are_sanitized(self):
        session = dict(SAMPLE_SESSION, cwd="/x token=sk-abcdefgh12345678 y")
        pane = collector.project_prime_session(session, "6fce4bd4bb2a", False)
        self.assertNotIn("sk-abcdefgh12345678", json.dumps(pane))


class TestPrimeEndToEnd(unittest.TestCase):
    """Real-subprocess path with executable fixtures; no network involved."""

    def write_cli(self, tmp, body, exit_code):
        path = os.path.join(tmp, "prime-agent")
        script = (
            f"#!{sys.executable}\n"
            "import sys\n"
            f"sys.stdout.write({body!r})\n"
            f"sys.exit({exit_code})\n"
        )
        with open(path, "w") as handle:
            handle.write(script)
        os.chmod(path, stat.S_IRWXU)
        return path

    def test_success_path_projects_only_configured_session(self):
        payload = {"sessions": [dict(SAMPLE_SESSION), {"sessionId": "other-aaaa1111"}]}
        with tempfile.TemporaryDirectory() as tmp:
            binary = self.write_cli(tmp, json.dumps(payload), 0)
            env = {collector.PRIME_BIN_ENV: binary, collector.PRIME_SESSIONS_ENV: "6fce4bd4bb2a"}
            panes = collector.collect_prime_sessions(env)
        self.assertEqual([p["session_name"] for p in panes], ["prime-6fce4bd4bb2a"])

    def test_non_zero_exit_fails_open_even_with_valid_json(self):
        payload = {"sessions": [dict(SAMPLE_SESSION)]}
        with tempfile.TemporaryDirectory() as tmp:
            binary = self.write_cli(tmp, json.dumps(payload), 3)
            env = {collector.PRIME_BIN_ENV: binary, collector.PRIME_SESSIONS_ENV: "6fce4bd4bb2a"}
            self.assertEqual(collector.collect_prime_sessions(env), [])


if __name__ == "__main__":
    unittest.main()
