from __future__ import annotations

import getpass
import warnings
import io
import json
import stat
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout
from pathlib import Path
from unittest.mock import patch

from pydantic import SecretStr

from moon_uestc import AuthenticationError
from moon_uestc.cli import main
from moon_uestc.models import CASCredentials


class LoginCLITests(unittest.TestCase):
    def test_login_saves_actual_credentials_with_owner_only_permissions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output_path = Path(directory) / "credentials.json"
            credentials = CASCredentials(
                castgc=SecretStr("actual-castgc-secret"),
                browser_fingerprint=SecretStr("actual-browser-secret"),
                user_agent="Browser/7.0",
            )
            stdout = io.StringIO()
            stderr = io.StringIO()
            with (
                patch("moon_uestc.cli.IDASClient"),
                patch("moon_uestc.cli.login_interactive", return_value=credentials),
                patch("moon_uestc.cli.getpass.getpass", return_value="entered-password"),
                patch("builtins.input", return_value="student-17"),
                redirect_stdout(stdout),
                redirect_stderr(stderr),
            ):
                result = main([
                    "login",
                    "--output", str(output_path),
                    "--browser-fingerprint", "0123456789ABCDEF0123456789ABCDEF",
                    "--remember-me",
                ])

            self.assertEqual(result, 0)
            self.assertEqual(stdout.getvalue(), f"{output_path}\n")
            self.assertEqual(stderr.getvalue(), "")
            for secret in (
                "actual-castgc-secret",
                "actual-browser-secret",
                "entered-password",
            ):
                self.assertNotIn(secret, stdout.getvalue() + stderr.getvalue())
            self.assertEqual(
                json.loads(output_path.read_text(encoding="utf-8")),
                {
                    "castgc": "actual-castgc-secret",
                    "browser_fingerprint": "actual-browser-secret",
                    "user_agent": "Browser/7.0",
                },
            )
            self.assertEqual(stat.S_IMODE(output_path.stat().st_mode), 0o600)

    def test_existing_output_is_rejected_before_prompting_or_authentication(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output_path = Path(directory) / "credentials.json"
            output_path.write_text("keep existing contents", encoding="utf-8")
            stdout = io.StringIO()
            stderr = io.StringIO()
            with (
                patch("moon_uestc.cli.IDASClient") as client_factory,
                patch("moon_uestc.cli.login_interactive") as login,
                patch("moon_uestc.cli.getpass.getpass") as password_prompt,
                patch("builtins.input") as username_prompt,
                redirect_stdout(stdout),
                redirect_stderr(stderr),
            ):
                result = main(["login", "--output", str(output_path)])

            self.assertEqual(result, 1)
            self.assertEqual(output_path.read_text(encoding="utf-8"), "keep existing contents")
            self.assertEqual(stdout.getvalue(), "")
            client_factory.assert_not_called()
            login.assert_not_called()
            password_prompt.assert_not_called()
            username_prompt.assert_not_called()

    def test_dangling_output_symlink_is_not_followed(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            output_path = Path(directory) / "credentials.json"
            target_path = Path(directory) / "missing-target"
            output_path.symlink_to(target_path)
            with (
                patch("moon_uestc.cli.IDASClient") as client_factory,
                redirect_stderr(io.StringIO()),
            ):
                result = main(["login", "--username", "student-17", "--output", str(output_path)])

            self.assertEqual(result, 1)
            self.assertTrue(output_path.is_symlink())
            self.assertFalse(target_path.exists())
            client_factory.assert_not_called()

    def test_login_failure_does_not_echo_exception_or_secret(self) -> None:
        secret = "sensitive-server-response"
        stdout = io.StringIO()
        stderr = io.StringIO()
        with tempfile.TemporaryDirectory() as directory:
            output_path = Path(directory) / "credentials.json"
            with (
                patch("moon_uestc.cli.IDASClient"),
                patch("moon_uestc.cli.login_interactive", side_effect=AuthenticationError(secret)),
                patch("moon_uestc.cli.getpass.getpass", return_value="private-password"),
                redirect_stdout(stdout),
                redirect_stderr(stderr),
            ):
                result = main([
                    "login", "--username", "student-17", "--output", str(output_path),
                ])

            self.assertEqual(result, 1)
            self.assertFalse(output_path.exists())
            self.assertNotIn(secret, stdout.getvalue() + stderr.getvalue())
            self.assertNotIn("private-password", stdout.getvalue() + stderr.getvalue())

    def test_keyboard_interrupt_returns_cancel_status(self) -> None:
        stdout = io.StringIO()
        stderr = io.StringIO()
        with tempfile.TemporaryDirectory() as directory:
            output_path = Path(directory) / "credentials.json"
            with (
                patch("moon_uestc.cli.getpass.getpass", side_effect=KeyboardInterrupt),
                patch("moon_uestc.cli.IDASClient") as client_factory,
                redirect_stdout(stdout),
                redirect_stderr(stderr),
            ):
                result = main([
                    "login", "--username", "student-17", "--output", str(output_path),
                ])

            self.assertEqual(result, 130)
            self.assertFalse(output_path.exists())
            self.assertEqual(stdout.getvalue(), "")
            self.assertNotIn("Traceback", stderr.getvalue())
            client_factory.assert_not_called()

    def test_insecure_password_echo_fallback_aborts_before_reading(self) -> None:
        def insecure_prompt(prompt: str) -> str:
            del prompt
            warnings.warn("echo would be enabled", getpass.GetPassWarning)
            raise AssertionError("must abort before reading an echoed password")

        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "credentials.json"
            with (
                patch("moon_uestc.cli.getpass.getpass", side_effect=insecure_prompt),
                patch("moon_uestc.cli.IDASClient") as client_factory,
                redirect_stderr(io.StringIO()),
            ):
                result = main(["login", "--username", "student", "--output", str(destination)])
            self.assertEqual(result, 1)
            self.assertFalse(destination.exists())
            client_factory.assert_not_called()

    def test_help_exits_without_authentication(self) -> None:
        stdout = io.StringIO()
        with (
            patch("moon_uestc.cli.IDASClient") as client_factory,
            patch("moon_uestc.cli.login_interactive") as login,
            patch("moon_uestc.cli.getpass.getpass") as password_prompt,
            redirect_stdout(stdout),
        ):
            with self.assertRaises(SystemExit) as exited:
                main(["login", "--help"])

        self.assertEqual(exited.exception.code, 0)
        self.assertNotIn("--password", stdout.getvalue())
        client_factory.assert_not_called()
        login.assert_not_called()
        password_prompt.assert_not_called()

    def test_password_option_is_rejected_without_echoing_its_value(self) -> None:
        secret = "command-line-password"
        stderr = io.StringIO()
        with (
            patch("moon_uestc.cli.IDASClient") as client_factory,
            patch("moon_uestc.cli.getpass.getpass") as password_prompt,
            redirect_stderr(stderr),
        ):
            with self.assertRaises(SystemExit) as exited:
                main([
                    "login", "--username", "student-17", "--output", "credentials.json",
                    f"--password={secret}",
                ])

        self.assertEqual(exited.exception.code, 2)
        self.assertNotIn(secret, stderr.getvalue())
        client_factory.assert_not_called()
        password_prompt.assert_not_called()
