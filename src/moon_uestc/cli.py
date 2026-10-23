"""Command-line access to an interactive IDAS login."""

from __future__ import annotations

import argparse
import getpass
import json
import os
import sys
import warnings
from collections.abc import Sequence
from typing import NoReturn

from pydantic import SecretStr

from .errors import MoonError
from .idas import IDASClient
from .interactive import login_interactive
from .models import CASCredentials


class _ArgumentParser(argparse.ArgumentParser):
    def error(self, message: str) -> NoReturn:
        del message
        print(f"{self.prog}: error: invalid command-line arguments", file=sys.stderr)
        raise SystemExit(2)


def _parser() -> argparse.ArgumentParser:
    parser = _ArgumentParser(prog="moon-uestc", description="Authenticate to IDAS interactively.")
    commands = parser.add_subparsers(dest="command", required=True)
    login = commands.add_parser("login", help="log in and save CAS credentials")
    login.add_argument("--username", help="IDAS username (prompted when omitted)")
    login.add_argument("--user-agent", default="moon-uestc/0.1", help="browser user-agent to use")
    login.add_argument("--browser-fingerprint", help="optional existing browser fingerprint")
    login.add_argument("--remember-me", action="store_true", help="request a persistent IDAS login")
    login.add_argument("--output", required=True, help="new file for the sensitive CAS credentials")
    return parser


def _write_credentials(path: str, credentials: CASCredentials) -> None:
    """Create a new owner-only JSON file containing the actual credential values."""
    if os.path.lexists(path):
        raise FileExistsError

    payload = json.dumps(
        {
            "castgc": credentials.castgc.get_secret_value(),
            "browser_fingerprint": credentials.browser_fingerprint.get_secret_value(),
            "user_agent": credentials.user_agent,
        },
        ensure_ascii=False,
        separators=(",", ":"),
    ) + "\n"

    flags = os.O_WRONLY | os.O_CREAT | os.O_EXCL
    if hasattr(os, "O_NOFOLLOW"):
        flags |= os.O_NOFOLLOW
    descriptor = os.open(path, flags, 0o600)
    identity = os.fstat(descriptor)
    try:
        os.fchmod(descriptor, 0o600)
        stream = os.fdopen(descriptor, "w", encoding="utf-8")
        descriptor = -1
        with stream:
            stream.write(payload)
    except BaseException:
        if descriptor >= 0:
            try:
                os.close(descriptor)
            except OSError:
                pass
        try:
            current = os.stat(path, follow_symlinks=False)
            if (current.st_dev, current.st_ino) == (identity.st_dev, identity.st_ino):
                os.unlink(path)
        except OSError:
            pass
        raise


def main(argv: Sequence[str] | None = None) -> int:
    """Run the login command and return a process exit status."""
    parser = _parser()
    arguments = parser.parse_args(argv)
    if arguments.command != "login":
        parser.error("a command is required")

    try:
        if os.path.lexists(arguments.output):
            raise FileExistsError

        username = arguments.username
        if username is None:
            username = input("Username: ")
        with warnings.catch_warnings():
            warnings.simplefilter("error", getpass.GetPassWarning)
            password = SecretStr(getpass.getpass("Password: "))

        with IDASClient(arguments.user_agent) as client:
            credentials = login_interactive(
                client,
                username,
                password,
                browser_fingerprint=arguments.browser_fingerprint,
                remember_me=arguments.remember_me,
            )

        _write_credentials(arguments.output, credentials)
    except KeyboardInterrupt:
        print("Login cancelled.", file=sys.stderr)
        return 130
    except (MoonError, ValueError, OSError, EOFError, getpass.GetPassWarning):
        print("Login failed.", file=sys.stderr)
        return 1

    print(arguments.output)
    return 0
