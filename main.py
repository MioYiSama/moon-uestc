import json
import os
import re
from datetime import datetime
from getpass import getpass
from pathlib import Path
from tempfile import NamedTemporaryFile
from time import time

import httpx2
from pydantic import (
    BaseModel,
    ConfigDict,
    JsonValue,
    SecretStr,
    TypeAdapter,
    ValidationError,
)

from moon_uestc import (
    AuthenticationError,
    AuthorizationError,
    CheckinTicketError,
    IDASClient,
    MoonClient,
    ReauthenticationRequiredError,
    WeChatClient,
    WeChatCredentials,
    login_interactive,
    login_wechat_interactive,
)
from moon_uestc.models import CASCredentials, CheckinRecord, CheckinRequest

IDAS_ORIGIN = "https://idas.uestc.edu.cn"
SERVICE_URL = "https://jzsz.uestc.edu.cn/"
WECHAT_APP_ID = "wxaa78bf2905f01cee"
_MFA_RESPONSE = TypeAdapter(dict[str, JsonValue])


class ReauthPage(BaseModel):
    model_config = ConfigDict(hide_input_in_errors=True)

    service: str
    reAuthType: str
    isMultifactor: str
    reAuthUserId: SecretStr


class MFAExchangeTransport(httpx2.BaseTransport):
    """Continue the official SMS challenge inside the CAS exchange's cookie jar."""

    def __init__(
        self,
        credentials: CASCredentials,
        *,
        transport: httpx2.BaseTransport | None = None,
    ) -> None:
        cookies = httpx2.Cookies()
        cookies.set(
            "CASTGC",
            credentials.castgc.get_secret_value(),
            domain="idas.uestc.edu.cn",
            path="/authserver",
        )
        cookies.set(
            "MULTIFACTOR_BROWSER_FINGERPRINT",
            credentials.browser_fingerprint.get_secret_value(),
            domain="idas.uestc.edu.cn",
            path="/",
        )
        self._client = httpx2.Client(
            headers={"User-Agent": credentials.user_agent},
            cookies=cookies,
            timeout=30,
            follow_redirects=False,
            transport=transport,
        )
        self._challenged = False

    def handle_request(self, request: httpx2.Request) -> httpx2.Response:
        response = self._client.send(request)
        if (
            request.url.scheme == "https"
            and request.url.host == "idas.uestc.edu.cn"
            and request.url.path == "/authserver/reAuthCheck/reAuthLoginView.do"
            and response.is_success
        ):
            if self._challenged:
                raise AuthenticationError(
                    "短信认证后仍未获准访问，请在官方页面检查认证要求。"
                )
            self._challenged = True
            return self._complete_sms(response)
        return response

    def _complete_sms(self, response: httpx2.Response) -> httpx2.Response:
        match = re.search(r"var reAuthParams\s*=\s*(\{.*?\});", response.text, re.S)
        try:
            if match is None:
                raise ValueError
            page = ReauthPage.model_validate_json(match.group(1))
        except ValueError:
            raise AuthenticationError("无法解析官方二次认证页面。") from None
        if page.service != SERVICE_URL:
            raise AuthenticationError("二次认证页面的目标业务不匹配。")
        if page.reAuthType != "3" and not re.search(
            r"""(?:data-type|id)\s*=\s*["']3["']""",
            response.text,
        ):
            raise AuthenticationError(
                "当前二次认证未提供短信方式，请在官方页面完成认证。"
            )

        # Collect every Set-Cookie, including rotations/deletions, for the SDK jar.
        cookie_headers = list(response.headers.multi_items())
        ajax_headers = {
            "X-Requested-With": "XMLHttpRequest",
            "Origin": IDAS_ORIGIN,
            "Referer": str(response.request.url),
        }

        def post(path: str, data: dict[str, str]) -> dict[str, JsonValue]:
            result = self._client.post(
                IDAS_ORIGIN + path,
                data=data,
                headers=ajax_headers,
            )
            cookie_headers.extend(result.headers.multi_items())
            if not result.is_success:
                raise AuthenticationError(
                    "二次认证接口请求失败。",
                    status_code=result.status_code,
                )
            try:
                return _MFA_RESPONSE.validate_json(result.content)
            except ValidationError:
                raise AuthenticationError("二次认证接口返回格式不符。") from None

        if page.reAuthType != "3":
            switched = post(
                "/authserver/reAuthCheck/changeReAuthType.do",
                {
                    "isMultifactor": page.isMultifactor,
                    "reAuthType": "3",
                    "service": page.service,
                },
            )
            selected = switched.get("data")
            if (
                str(switched.get("code")) != "1"
                or not isinstance(selected, dict)
                or selected.get("reAuthType") != "3"
            ):
                raise AuthenticationError("服务端未允许切换短信认证。")

        print("访问打卡平台需要短信二次认证。")
        input("按 Enter 向绑定手机发送验证码，Ctrl-C 取消：")
        sent = post(
            "/authserver/dynamicCode/getDynamicCodeByReauth.do",
            {
                "userName": page.reAuthUserId.get_secret_value(),
                "authCodeTypeName": "reAuthDynamicCodeType",
            },
        )
        if sent.get("res") != "success":
            raise AuthenticationError("短信未发送成功，请稍后在官方页面重试。")
        code = getpass("短信验证码：").strip()
        if not code:
            raise AuthenticationError("未输入短信验证码。")
        trust = input("信任此设备，减少后续二次认证？[y/N]：").strip().lower() == "y"
        submitted = post(
            "/authserver/reAuthCheck/reAuthSubmit.do",
            {
                "service": page.service,
                "reAuthType": "3",
                "isMultifactor": page.isMultifactor,
                "password": "",
                "dynamicCode": code,
                "uuid": "",
                "answer1": "",
                "answer2": "",
                "otpCode": "",
                "skipTmpReAuth": "true" if trust else "false",
            },
        )
        if submitted.get("code") in ("reAuth_failed", "reAuth_unauthorized"):
            raise AuthenticationError("短信认证被拒绝，验证码可能错误或已失效。")

        # The official page navigates back to CAS after submission. The SDK must
        # still verify the platform bootstrap and identity before caching SESSION.
        return httpx2.Response(
            302,
            request=response.request,
            headers=[
                (
                    "Location",
                    str(
                        httpx2.URL(
                            IDAS_ORIGIN + "/authserver/login",
                            params={"service": page.service},
                        )
                    ),
                ),
                *[
                    (key, value)
                    for key, value in cookie_headers
                    if key.lower() == "set-cookie"
                ],
            ],
        )

    def close(self) -> None:
        self._client.close()


CACHE_PATH = Path(__file__).with_name(".moon-uestc-auth.json")


class AuthCache(BaseModel):
    model_config = ConfigDict(extra="forbid", hide_input_in_errors=True)

    browser_fingerprint: SecretStr | None = None
    user_agent: str = "moon-uestc/0.1"
    castgc: SecretStr | None = None
    session: SecretStr | None = None
    wechat_credentials: WeChatCredentials | None = None

    def update_credentials(self, credentials: CASCredentials) -> None:
        self.castgc = credentials.castgc
        self.browser_fingerprint = credentials.browser_fingerprint
        self.user_agent = credentials.user_agent


def load_cache() -> AuthCache:
    try:
        return AuthCache.model_validate_json(CACHE_PATH.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return AuthCache()
    except ValidationError:
        raise SystemExit(
            f"认证缓存格式损坏，请删除 {CACHE_PATH} 后重新运行。"
        ) from None


def save_cache(cache: AuthCache) -> None:
    # SecretStr 的默认 JSON 是掩码，持久化时需要显式取出真实值。
    payload = {
        key: value.get_secret_value() if isinstance(value, SecretStr) else value
        for key, value in cache.model_dump().items()
    }
    if cache.wechat_credentials is not None:
        credentials = cache.wechat_credentials
        payload["wechat_credentials"] = {
            "openid": credentials.openid.get_secret_value(),
            "access_token": credentials.access_token.get_secret_value(),
            "refresh_token": credentials.refresh_token.get_secret_value(),
            "expires_at": credentials.expires_at,
        }
    with NamedTemporaryFile(
        mode="w",
        encoding="utf-8",
        dir=CACHE_PATH.parent,
        prefix=CACHE_PATH.name + ".",
        delete=False,
    ) as stream:
        temporary = Path(stream.name)
        try:
            os.fchmod(stream.fileno(), 0o600)
            json.dump(payload, stream, ensure_ascii=False)
            stream.write("\n")
            stream.flush()
            os.replace(temporary, CACHE_PATH)
        finally:
            temporary.unlink(missing_ok=True)


def login(cache: AuthCache) -> None:
    with IDASClient(user_agent=cache.user_agent) as idas:
        cache.browser_fingerprint = idas.register_fingerprint(cache.browser_fingerprint)
        # 即使登录被取消，下次也沿用已注册的设备标识。
        save_cache(cache)
        credentials = login_interactive(
            idas,
            input("学号/工号：").strip(),
            getpass("统一认证密码："),
            browser_fingerprint=cache.browser_fingerprint,
            remember_me=True,
        )
    cache.update_credentials(credentials)
    save_cache(cache)


def exchange(cache: AuthCache) -> None:
    assert cache.castgc is not None and cache.browser_fingerprint is not None
    credentials = CASCredentials(
        castgc=cache.castgc,
        browser_fingerprint=cache.browser_fingerprint,
        user_agent=cache.user_agent,
    )
    with MoonClient() as bootstrap, MFAExchangeTransport(credentials) as transport:
        exchanged = bootstrap.auth.exchange_session(
            castgc=credentials.castgc,
            browser_fingerprint=credentials.browser_fingerprint,
            user_agent=credentials.user_agent,
            transport=transport,
        )
    cache.session = exchanged.session
    cache.update_credentials(exchanged.credentials)
    save_cache(cache)


def wechat_code() -> SecretStr:
    cache = load_cache()
    with WeChatClient() as wechat:
        credentials = cache.wechat_credentials
        if credentials is None:
            credentials = login_wechat_interactive(wechat)
        elif credentials.expires_at <= time() + 60:
            credentials = wechat.refresh(credentials)
        cache.wechat_credentials = credentials
        # Persist token rotation before the MMTLS call, even if that call fails.
        save_cache(cache)
        return wechat.get_code(app_id=WECHAT_APP_ID, credentials=credentials)


def checkin_ticket(client: MoonClient) -> SecretStr:
    expected_user = client.auth.user_info().data.user_data
    with MoonClient() as bootstrap:
        binding = bootstrap.auth.check_wechat_bind(code=wechat_code()).data
    if not binding.bind:
        raise CheckinTicketError("该微信尚未绑定学校账号，请先在官方小程序完成绑定。")
    assert binding.session_id is not None
    try:
        with MoonClient(session=binding.session_id) as mini_program:
            user = mini_program.auth.mini_program_user_info().data.user_data
            if user.id != expected_user.id:
                raise AuthorizationError("微信绑定的学校账号与当前 H5 账号不同，已停止取票。")
            # wx.login codes are one-time: check-bind consumed the first one.
            return mini_program.auth.request_checkin_ticket(code=wechat_code()).data.ticket
    except AuthenticationError as error:
        raise CheckinTicketError(
            "小程序会话未通过验证，请重新登录小程序。",
            code=error.code,
            status_code=error.status_code,
        ) from None


def today(session: SecretStr) -> CheckinRecord | None:
    with MoonClient(session=session) as client:
        ticket = checkin_ticket(client)
        print(ticket)
        client.set_checkin_ticket(ticket)

        address = client.student.reverse_geocode(
            latitude=30.67449967,
            longitude=104.09629583,
        ).data
        print(address)

        result = client.student.checkin(
            CheckinRequest(
                client_time=datetime.now(),
                latitude=30.67449967,
                longitude=104.09629583,
                reverse_geocode=address,
            )
        )

        print(result.model_dump_json())


def main() -> CheckinRecord | None:
    cache = load_cache()
    if cache.session is not None:
        try:
            return today(cache.session)
        except AuthenticationError:
            cache.session = None
            save_cache(cache)

    if cache.castgc is not None and cache.browser_fingerprint is not None:
        try:
            exchange(cache)
        except ReauthenticationRequiredError:
            cache.castgc = None
            save_cache(cache)
        else:
            assert cache.session is not None
            return today(cache.session)

    login(cache)
    exchange(cache)
    assert cache.session is not None
    return today(cache.session)


if __name__ == "__main__":
    main()
