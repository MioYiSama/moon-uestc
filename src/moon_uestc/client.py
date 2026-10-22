from __future__ import annotations

from types import TracebackType

import httpx2
from pydantic import SecretStr

from ._transport import Transport
from .models import APIResponse, UserInfo
from .student import ClassCadreService, StudentService


DEFAULT_BASE_URL = "https://jzsz.uestc.edu.cn/daily-checkin"


class AuthService:
    def __init__(self, transport: Transport) -> None:
        self._http = transport

    def user_info(self, *, session_id: str | None = None) -> APIResponse[UserInfo]:
        """Optional sessionId bootstrap mirrors H5; valid bootstrap not live-verified."""
        return self._http.request(
            "GET", "/api/auth/user-info", APIResponse[UserInfo],
            params={"sessionId": session_id} if session_id is not None else None,
        )

    def service_account_subscribed(self) -> APIResponse[bool]:
        return self._http.request("GET", "/api/user/service-account-subscribed", APIResponse[bool])


class MoonClient:
    """Synchronous typed client. Owns its HTTP connection pool.

    Supply only your own SESSION cookie. A checkin ticket is independent of the
    login session and must come from the official mini-program entry. No login,
    ticket acquisition, refresh or business-write retry is performed implicitly.
    """

    def __init__(
        self,
        *,
        session: str | SecretStr | None = None,
        checkin_ticket: str | SecretStr | None = None,
        bearer_token: str | SecretStr | None = None,
        base_url: str = DEFAULT_BASE_URL,
        timeout: float = 30.0,
        transport: httpx2.BaseTransport | None = None,
    ) -> None:
        self._http = Transport(
            base_url=base_url,
            session=session,
            bearer_token=bearer_token,
            checkin_ticket=checkin_ticket,
            timeout=timeout,
            transport=transport,
        )
        self.auth: AuthService = AuthService(self._http)
        self.student: StudentService = StudentService(self._http)
        self.class_cadre: ClassCadreService = ClassCadreService(self._http)

    def set_checkin_ticket(self, ticket: str | SecretStr | None) -> None:
        """Replace an operation ticket after official reauthorization; None clears it."""
        self._http.checkin_ticket = (
            SecretStr(ticket.get_secret_value() if isinstance(ticket, SecretStr) else ticket)
            if ticket is not None else None
        )

    def close(self) -> None:
        self._http.client.close()

    def __enter__(self) -> MoonClient:
        return self

    def __exit__(
        self,
        exc_type: type[BaseException] | None,
        exc_value: BaseException | None,
        traceback: TracebackType | None,
    ) -> None:
        self.close()
