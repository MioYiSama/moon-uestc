"""Native wx.login acquisition through the user-authorized YYB protocol.

Protocol reference: 525815266/YYB-Go-Enhanced, commit
8a9497397abc7bf736be462f2e293f2bc6d4d0c0. No YYB service is required.
"""
from __future__ import annotations

import math
import re
import sys
from io import BytesIO
from time import monotonic, sleep
from types import TracebackType

import httpx2
from PIL import Image
from pydantic import SecretStr

from ._wechat_login import LoginClient
from ._wechat_mmtls import get_code
from .captcha_terminal import render_image
from .wechat_models import (
    WeChatAuthorizationError,
    WeChatCredentials,
    WeChatPollResult,
    WeChatQRCode,
)


class WeChatClient:
    """Independent WeChat connection pool; never receives CAS or Moon cookies.

    Authorization, refresh and code acquisition are explicit. Codes are returned
    as SecretStr and are neither cached nor retried by this client.
    """

    def __init__(
        self,
        *,
        timeout: float = 30.0,
        transport: httpx2.BaseTransport | None = None,
    ) -> None:
        if not math.isfinite(timeout) or timeout <= 0:
            raise ValueError("timeout must be positive and finite")
        self._timeout = timeout
        self._login = LoginClient(timeout=timeout, transport=transport)

    def create_qr(self) -> WeChatQRCode:
        return self._login.create_qr()

    def poll_qr(self, uuid: str) -> WeChatPollResult:
        return self._login.poll_qr(uuid)

    def exchange_code(self, code: SecretStr) -> WeChatCredentials:
        """Exchange the QR OAuth code, not a mini-program wx.login code."""
        return self._login.exchange_code(code)

    def refresh(self, credentials: WeChatCredentials) -> WeChatCredentials:
        """Explicitly refresh YYB credentials; caller persists any rotation."""
        return self._login.refresh(credentials)

    def get_code(self, *, app_id: str, credentials: WeChatCredentials) -> SecretStr:
        """Obtain one fresh code for the specified mini-program and account.

        The HTTP transport option covers QR/YYB requests only. The MMTLS phase
        uses direct TCP connections to WeChat, with this client's timeout.
        """
        if re.fullmatch(r"wx[0-9a-fA-F]{16}", app_id) is None:
            raise ValueError("app_id must be the target mini-program's wx AppID")
        login_buffer = self._login.fetch_login_buffer(credentials)
        return SecretStr(get_code(
            login_buffer.get_secret_value(), app_id, timeout=self._timeout,
        ))

    def close(self) -> None:
        self._login.close()

    def __enter__(self) -> WeChatClient:
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc_value: BaseException | None,
        traceback: TracebackType | None,
    ) -> None:
        self.close()


def login_wechat_interactive(
    client: WeChatClient, *, authorization_timeout: float = 180.0,
) -> WeChatCredentials:
    """Display a QR in the terminal, then wait for the user's phone approval."""
    if not sys.stdout.isatty() or not sys.stdin.isatty():
        raise WeChatAuthorizationError("微信扫码登录需要交互终端。")
    if not math.isfinite(authorization_timeout) or authorization_timeout <= 0:
        raise ValueError("authorization_timeout must be positive and finite")
    qr = client.create_qr()
    with Image.open(BytesIO(qr.image)) as image:
        # Keep the official QR's white margin; no dark terminal background bleed.
        print(render_image(image.convert("RGB"), columns=90, max_rows=45))
    print("请用本人微信扫码并确认应用宝授权；Ctrl-C 取消。", flush=True)
    deadline = monotonic() + authorization_timeout
    scanned = False
    while monotonic() < deadline:
        result = client.poll_qr(qr.uuid)
        if result.status == "authorized" and result.code is not None:
            return client.exchange_code(result.code)
        if result.status in ("cancelled", "expired"):
            raise WeChatAuthorizationError("微信扫码已取消或二维码过期，请重新运行。")
        if result.status == "scanned" and not scanned:
            print("已扫码，请在微信中确认授权。", flush=True)
            scanned = True
        sleep(min(1.0, max(0.0, deadline - monotonic())))
    raise WeChatAuthorizationError("微信扫码等待超时，请重新运行。")
