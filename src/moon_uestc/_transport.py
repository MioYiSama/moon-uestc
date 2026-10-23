from __future__ import annotations

from collections.abc import Mapping, Sequence
from urllib.parse import urlsplit

import httpx2
from pydantic import JsonValue, SecretStr, TypeAdapter, ValidationError

from .errors import (
    APIError,
    AuthenticationError,
    AuthorizationError,
    CheckinTicketError,
    ProtocolError,
)
from .models import APIResponse, RequestModel

type QueryValue = str | int | float | bool | None

_ENVELOPE = TypeAdapter(dict[str, JsonValue])
_TICKET_CODES = frozenset({46001, 46002, 46003})


class Transport:
    def __init__(
        self,
        *,
        base_url: str,
        session: str | SecretStr | None,
        bearer_token: str | SecretStr | None,
        checkin_ticket: str | SecretStr | None,
        timeout: float | httpx2.Timeout,
        transport: httpx2.BaseTransport | None,
    ) -> None:
        self.base_url: str = base_url.rstrip("/")
        parts = urlsplit(self.base_url)
        if (
            parts.scheme not in {"https", "http"}
            or not parts.hostname
            or parts.query
            or parts.fragment
            or parts.username
        ):
            raise ValueError(
                "base_url must be an HTTP(S) URL without credentials, query or fragment"
            )
        self.origin: str = f"{parts.scheme}://{parts.netloc}"
        headers = {"platform": "h5"}
        if bearer_token is not None:
            headers["Authorization"] = f"Bearer {self._secret(bearer_token)}"
        cookies = httpx2.Cookies()
        if session is not None:
            cookies.set(
                "SESSION", self._secret(session), domain=parts.hostname, path="/"
            )
        self.checkin_ticket: SecretStr | None = (
            SecretStr(self._secret(checkin_ticket))
            if checkin_ticket is not None
            else None
        )
        self.client: httpx2.Client = httpx2.Client(
            headers=headers,
            cookies=cookies,
            timeout=timeout,
            follow_redirects=False,
            transport=transport,
        )

    @staticmethod
    def _secret(value: str | SecretStr) -> str:
        return value.get_secret_value() if isinstance(value, SecretStr) else value

    def request[T](
        self,
        method: str,
        path: str,
        schema: type[APIResponse[T]],
        *,
        params: Mapping[str, QueryValue] | None = None,
        body: RequestModel | Sequence[RequestModel] | None = None,
        signed: bool = False,
        platform_app: bool = False,
    ) -> APIResponse[T]:
        headers: dict[str, str] = {"platform": "mp-weixin"} if platform_app else {}
        if signed:
            if (
                self.checkin_ticket is None
                or not self.checkin_ticket.get_secret_value()
            ):
                raise CheckinTicketError(
                    "This operation requires a checkinTicket from the official mini-program entry"
                )
            headers["X-Checkin-Ticket"] = self.checkin_ticket.get_secret_value()
        data = (
            body.model_dump(mode="json", by_alias=True, exclude_none=True)
            if isinstance(body, RequestModel)
            else [
                item.model_dump(mode="json", by_alias=True, exclude_none=True)
                for item in body
            ]
            if body is not None
            else None
        )
        response = self.client.request(
            method,
            (self.origin + "/platform-app" if platform_app else self.base_url) + path,
            params=params,
            json=data,
            headers=headers,
        )
        payload = self._envelope(response, require_ok=platform_app)
        # Write handlers may omit data; non-null read schemas still reject it.
        payload.setdefault("data", None)
        try:
            return schema.model_validate(payload)
        except ValidationError as exc:
            raise ProtocolError(
                f"Response data does not match the student API contract ({exc.error_count()} validation errors)"
            ) from None

    @staticmethod
    def _envelope(
        response: httpx2.Response, *, require_ok: bool = False
    ) -> dict[str, JsonValue]:
        status = response.status_code
        if 300 <= status < 400:
            raise ProtocolError(
                "Unexpected redirect; log in through the official entry to renew the session"
            )
        try:
            payload = _ENVELOPE.validate_json(response.content)
        except ValidationError:
            if response.is_success:
                raise ProtocolError(
                    "Expected a JSON object; the server may have returned a login page"
                ) from None
            payload = {}
        raw_code = payload.get("code")
        code = (
            raw_code
            if isinstance(raw_code, int) and not isinstance(raw_code, bool)
            else None
        )
        raw_message = payload.get("msg") or payload.get("message")
        message = raw_message if isinstance(raw_message, str) else "API request failed"
        if code in _TICKET_CODES:
            raise CheckinTicketError(message, code=code, status_code=status)
        if status == 401:
            raise AuthenticationError(
                "HTTP 401: login session rejected", status_code=status
            )
        if status == 403:
            raise AuthorizationError(
                "HTTP 403: operation forbidden", status_code=status
            )
        if code == 4003:
            raise AuthorizationError(message, code=code, status_code=status)
        if code == 5000 and ("未登录" in message or "会话已失效" in message):
            raise AuthenticationError(message, code=code, status_code=status)
        if not response.is_success:
            response.raise_for_status()
        # The mini-program ticket endpoint checks raw ok, not H5 code/status.
        if require_ok:
            if payload.get("ok") is not True:
                raise APIError(message, code=code, status_code=status)
            return payload
        if code != 0 and payload.get("status") is not True:
            if code is None and "status" not in payload:
                raise ProtocolError("JSON response has no recognized business status")
            raise APIError(message, code=code, status_code=status)
        return payload

    def avatar(self, file_path: str) -> bytes:
        response = self.client.get(
            self.origin + "/fastdfs-util/api/fastdfs/download/image",
            params={"filePath": file_path},
        )
        # FastDFS returns image bytes without a Content-Type header in production.
        media_type = (
            response.headers.get("content-type", "").split(";", 1)[0].strip().lower()
        )
        if response.is_success and media_type not in {"application/json", "text/html"}:
            return response.content
        self._envelope(response)
        raise ProtocolError("Avatar response was not an image")
