from __future__ import annotations

from types import TracebackType
from typing import Literal

import httpx2
from pydantic import ConfigDict, Field, SecretStr

from ._cas import exchange_session
from ._transport import Transport
from .models import APIResponse, CASSession, CheckinTicket, RequestModel, UserInfo, WeChatBindResult
from .student import ClassCadreService, StudentService


DEFAULT_BASE_URL = "https://jzsz.uestc.edu.cn/daily-checkin"


class _WechatCodeRequest(RequestModel):
    model_config = ConfigDict(hide_input_in_errors=True)
    code: str = Field(min_length=1, repr=False)


class _WechatBindRequest(_WechatCodeRequest):
    sn: Literal[""] = ""


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

    def check_wechat_bind(self, *, code: str | SecretStr) -> APIResponse[WeChatBindResult]:
        """Log into the mini-program with one fresh wx.login code.

        Call on an independent client, then use data.session_id as SESSION on
        another mini-program client. Does not install the returned session ID.
        An unbound account must complete data.authorize_url in the official flow.
        """
        request = _WechatBindRequest(
            code=code.get_secret_value() if isinstance(code, SecretStr) else code,
        )
        return self._http.request(
            "POST", "/api/auth/check-bind", APIResponse[WeChatBindResult],
            body=request, platform_app=True,
        )

    def mini_program_user_info(self) -> APIResponse[UserInfo]:
        """Verify the mini-program SESSION and its bound school identity."""
        return self._http.request(
            "GET", "/api/auth/user-info", APIResponse[UserInfo], platform_app=True,
        )

    def request_checkin_ticket(self, *, code: str | SecretStr) -> APIResponse[CheckinTicket]:
        """Exchange a caller-provided uni.login code; does not install the ticket.

        Requires the SESSION returned by check_wechat_bind, not merely a valid
        H5 SESSION. The code must be fresh, not reused from the binding check.
        No WeChat login, renewal or retry is performed.
        """
        request = _WechatCodeRequest(
            code=code.get_secret_value() if isinstance(code, SecretStr) else code,
        )
        return self._http.request(
            "POST", "/api/auth/checkin-ticket", APIResponse[CheckinTicket],
            body=request, platform_app=True,
        )

    def exchange_session(
        self,
        *,
        castgc: str | SecretStr,
        browser_fingerprint: str | SecretStr,
        user_agent: str,
        transport: httpx2.BaseTransport | None = None,
    ) -> CASSession:
        """Exchange valid CAS cookies for a verified SESSION without changing this client.

        A separate connection pool and cookie jar are used. ``transport`` applies
        only to this exchange, independently of MoonClient's business transport.
        Returned credentials retain CAS cookie rotation for the next exchange.
        """
        return exchange_session(
            self._http,
            castgc=castgc,
            browser_fingerprint=browser_fingerprint,
            user_agent=user_agent,
            transport=transport,
        )


class MoonClient:
    """Synchronous typed client. Owns its HTTP connection pool.

    Supply only your own SESSION cookie. A checkin ticket is independent of the
    login session; explicitly exchange an official mini-program login code via
    auth.request_checkin_ticket, then set_checkin_ticket. No authentication,
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
