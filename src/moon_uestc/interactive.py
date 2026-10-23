"""Explicit terminal-operated CAPTCHA continuation for IDAS password login."""

from pydantic import SecretStr

from .captcha_terminal import solve_captcha
from .errors import CaptchaRequiredError
from .idas import IDASClient
from .models import CASCredentials


def login_interactive(
    client: IDASClient,
    username: str,
    password: str | SecretStr,
    *,
    browser_fingerprint: str | SecretStr | None = None,
    remember_me: bool = False,
) -> CASCredentials:
    """Let a human solve a requested CAPTCHA, then continue once in the same jar.

    A rejected password or CAPTCHA is not retried automatically. Secondary
    authentication remains an explicit SDK error, not a terminal CAPTCHA task.
    """
    try:
        return client.login(
            username,
            password,
            browser_fingerprint=browser_fingerprint,
            remember_me=remember_me,
        )
    except CaptchaRequiredError as challenge:
        answer = solve_captcha(client, challenge.kind)
    return client.login(
        username,
        password,
        browser_fingerprint=browser_fingerprint,
        remember_me=remember_me,
        captcha=answer,
    )
