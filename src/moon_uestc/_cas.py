"""Explicit CAS session exchange; never shares the student's credential jar."""

from urllib.parse import urlsplit

import httpx2
from pydantic import BaseModel, ConfigDict, SecretStr, ValidationError

from ._transport import Transport
from .errors import AuthenticationError, ProtocolError, ReauthenticationRequiredError
from .models import APIResponse, CASCredentials, CASSession, UserInfo

_CAS_ORIGIN = "https://idas.uestc.edu.cn"
_CAS_LOGIN = _CAS_ORIGIN + "/authserver/login"
_REDIRECT_CODES = frozenset({301, 302, 303, 307, 308})


class _LoginState(BaseModel):
    model_config = ConfigDict(strict=True)
    status: bool
    code: int
    data: bool


def _get(client: httpx2.Client, url: str | httpx2.URL) -> httpx2.Response:
    try:
        return client.get(url)
    except httpx2.RequestError:
        # Redirect URLs can carry one-time CAS tickets. Do not expose them in errors.
        raise AuthenticationError("CAS session exchange failed due to a network error") from None


def _check_status(response: httpx2.Response) -> None:
    if not response.is_success:
        raise AuthenticationError(
            "CAS session exchange received an unsuccessful HTTP response",
            status_code=response.status_code,
        )


def _cookie(client: httpx2.Client, name: str) -> str:
    try:
        value = client.cookies.get(name)
    except httpx2.CookieConflict:
        raise ProtocolError("CAS exchange received ambiguous authentication cookies") from None
    if not value:
        raise AuthenticationError("CAS exchange did not retain the required authentication cookie")
    return value


def exchange_session(
    current: Transport,
    *,
    castgc: str | SecretStr,
    browser_fingerprint: str | SecretStr,
    user_agent: str,
    transport: httpx2.BaseTransport | None = None,
) -> CASSession:
    credentials = CASCredentials(
        castgc=castgc if isinstance(castgc, SecretStr) else SecretStr(castgc),
        browser_fingerprint=(
            browser_fingerprint if isinstance(browser_fingerprint, SecretStr)
            else SecretStr(browser_fingerprint)
        ),
        user_agent=user_agent,
    )
    isolated = Transport(
        base_url=current.base_url,
        session=None,
        bearer_token=None,
        checkin_ticket=None,
        timeout=current.client.timeout,
        transport=transport,
    )
    with isolated.client as client:
        client.headers["User-Agent"] = credentials.user_agent
        client.cookies.set(
            "CASTGC", credentials.castgc.get_secret_value(),
            domain="idas.uestc.edu.cn", path="/authserver",
        )
        client.cookies.set(
            "MULTIFACTOR_BROWSER_FINGERPRINT",
            credentials.browser_fingerprint.get_secret_value(),
            domain="idas.uestc.edu.cn", path="/",
        )
        url = httpx2.URL(_CAS_LOGIN, params={"service": isolated.origin + "/"})
        response = _get(client, url)
        for hop in range(9):
            if response.status_code not in _REDIRECT_CODES:
                break
            location = response.headers.get("location")
            if not location:
                raise ProtocolError("CAS exchange redirect has no Location header")
            try:
                destination = response.url.join(location)
                parts = urlsplit(str(destination))
            except (ValueError, httpx2.InvalidURL):
                raise ProtocolError("CAS exchange returned an invalid redirect URL") from None
            if parts.username is not None or parts.password is not None or (
                f"{parts.scheme}://{parts.netloc}" not in {_CAS_ORIGIN, isolated.origin}
            ):
                raise ProtocolError("CAS exchange redirected outside the allowed authentication origins")
            if hop == 8:
                raise ProtocolError("CAS exchange exceeded the redirect limit")
            response = _get(client, destination)
        _check_status(response)
        final = urlsplit(str(response.url))
        if f"{final.scheme}://{final.netloc}" == _CAS_ORIGIN:
            raise ReauthenticationRequiredError(
                "Complete login or secondary authentication on the official CAS page"
            )

        bootstrap = _get(client, isolated.origin + "/api/isLoginFailure")
        if bootstrap.status_code in _REDIRECT_CODES:
            raise ReauthenticationRequiredError("Platform login must be completed on the official CAS page")
        _check_status(bootstrap)
        try:
            state = _LoginState.model_validate_json(bootstrap.content)
        except ValidationError:
            raise ProtocolError("Platform login check returned an invalid JSON contract") from None
        if state.code != 200 or not state.status or not state.data:
            raise ReauthenticationRequiredError("The platform did not accept the CAS login session")

        # The platform check can initialize identity state required by this API.
        # Verification uses only this new jar, never the current client's SESSION.
        identity = isolated.request("GET", "/api/auth/user-info", APIResponse[UserInfo]).data
        return CASSession(
            session=SecretStr(_cookie(client, "SESSION")),
            credentials=CASCredentials(
                castgc=SecretStr(_cookie(client, "CASTGC")),
                browser_fingerprint=SecretStr(_cookie(client, "MULTIFACTOR_BROWSER_FINGERPRINT")),
                user_agent=credentials.user_agent,
            ),
            user_info=identity,
        )
