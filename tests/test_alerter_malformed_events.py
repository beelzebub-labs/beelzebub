import importlib.util
import logging
import os
from pathlib import Path
import shutil
import tempfile
import unittest
from unittest.mock import patch


ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "Beekeeper scripts" / "Slack bot" / "Slack_alerter.py"


class StopTest(BaseException):
    """Controlled exit from the infinite tailing loop in tests."""


class FakeLogStream:
    def __init__(self):
        self.lines = iter(["bad event\n", "good event\n"])
        self.closed = False

    def __enter__(self):
        return self

    def __exit__(self, *args):
        self.closed = True
        return False

    def seek(self, *args):
        pass

    def readline(self):
        try:
            return next(self.lines)
        except StopIteration:
            raise StopTest()


class AlerterMalformedEventTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.temp = tempfile.TemporaryDirectory()
        test_dir = Path(cls.temp.name)
        shutil.copy2(SCRIPT, test_dir / "Slack_alerter.py")
        (test_dir / ".env").write_text(
            "WEBHOOK_TYPE=discord\nWEBHOOK_URL=http://127.0.0.1:9/not-real\n",
            encoding="utf-8",
        )
        spec = importlib.util.spec_from_file_location("beekeepers_alerter_under_test", test_dir / "Slack_alerter.py")
        cls.alerter = importlib.util.module_from_spec(spec)
        with patch.dict(os.environ, {"WEBHOOK_TYPE": "discord", "WEBHOOK_URL": "http://127.0.0.1:9/not-real"}):
            spec.loader.exec_module(cls.alerter)

    @classmethod
    def tearDownClass(cls):
        cls.temp.cleanup()

    def test_valid_json_scalar_and_array_are_skipped(self):
        with patch.object(self.alerter, "send_alert") as send:
            for raw in ("123", "[1]", '"a string"', "null", "true"):
                with self.subTest(value=raw):
                    self.alerter.handle_line(raw)
            send.assert_not_called()

    def test_new_event_with_non_dict_payload_is_skipped(self):
        with patch.object(self.alerter, "send_alert") as send:
            for payload in ("null", "[]", '"string"', "123"):
                with self.subTest(payload=payload):
                    self.alerter.handle_line('{"msg":"New Event","event":' + payload + '}')
            send.assert_not_called()

    def test_malformed_json_and_startup_messages_are_skipped(self):
        with patch.object(self.alerter, "send_alert") as send:
            self.alerter.handle_line("not json")
            self.alerter.handle_line('{"msg":"Init service: Apache 401"}')
            send.assert_not_called()

    def test_one_processing_exception_does_not_stop_follow_log(self):
        captured = []

        def process(line):
            captured.append(line)
            if line == "bad event\n":
                raise ValueError("deliberate parsing failure")

        fake_file = FakeLogStream()
        with patch.object(self.alerter, "LOG_FILE", "placeholder.log"), \
             patch.object(self.alerter.os.path, "exists", return_value=True), \
             patch("builtins.open", return_value=fake_file), \
             patch.object(self.alerter, "handle_line", side_effect=process), \
             patch.object(logging, "exception") as report:
            with self.assertRaises(StopTest):
                self.alerter.follow_log()
            report.assert_called_once()

        self.assertEqual(captured, ["bad event\n", "good event\n"])
        self.assertTrue(fake_file.closed)


if __name__ == "__main__":
    unittest.main()
