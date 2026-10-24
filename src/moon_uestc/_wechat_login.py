from __future__ import annotations

import hashlib
import json
import re
import secrets
import time
from http.cookies import CookieError, SimpleCookie
from io import BytesIO
from urllib.parse import urljoin, urlsplit

import httpx2
from PIL import Image
from pydantic import JsonValue, SecretStr

from .wechat_models import (
    WeChatAuthorizationError,
    WeChatCredentials,
    WeChatError,
    WeChatPollResult,
    WeChatProtocolError,
    WeChatQRCode,
)

_OAUTH_APP_ID = "wxd44977328b36e647"
_OAUTH_REDIRECT_URI = "https://yybadaccess.3g.qq.com/pc_yyb/pcyyb_oauth?login_type=WX"
_OAUTH_SCOPE = "snsapi_login,snsapi_runtime_pcsdk"
_OAUTH_STATE = "web"
_OAUTH_URL = "https://open.weixin.qq.com/connect/qrconnect"
_QR_BASE_URL = "https://open.weixin.qq.com/connect/qrcode/"
_QR_POLL_URL = "https://long.open.weixin.qq.com/connect/l/qrconnect"
_CALLBACK_URL = "https://yybadaccess.3g.qq.com/pc_yyb/pcyyb_oauth"
_LOGIN_BUFFER_URL = "https://yybadaccess.3g.qq.com/pc_yyb_auth/pcyyb_get_wx_login_buffer_auth"
_REFRESH_TOKEN_URL = "https://yybadaccess.3g.qq.com/pc_yyb_auth/pcyyb_refresh_token_auth"
_LOGIN_BUSINESS_ID = "pc_yyb_auth"
_LOGIN_ACCESS_KEY = "wgrdg373hy26ww2"
_LOGIN_BUFFER_TIMEOUT = 40.0
_MAX_REDIRECTS = 5
_REDIRECT_STATUSES = frozenset({301, 302, 303, 307, 308})
_UUID_RE = re.compile(r"/connect/qrcode/([^\"'>\s]+)")
_SAFE_UUID_RE = re.compile(r"[A-Za-z0-9._~-]{1,256}\Z")
_ERRCODE_RE = re.compile(r"wx_errcode\s*=\s*(\d+)")
_CODE_RE = re.compile(r"wx_code\s*=\s*'([^']*)'")


class LoginClient:
    """Owns one isolated WeChat QR/OAuth cookie jar and its HTTP connection."""

    def __init__(
        self,
        timeout: float = 30.0,
        transport: httpx2.BaseTransport | None = None,
    ) -> None:
        self.timeout = timeout
        self._client = httpx2.Client(
            timeout=timeout,
            transport=transport,
            follow_redirects=False,
            trust_env=False,
        )
        self._closed = False

    def create_qr(self) -> WeChatQRCode:
        self._ensure_open()
        response, _ = self._get_trusted(
            _OAUTH_URL,
            params={
                "appid": _OAUTH_APP_ID,
                "redirect_uri": _OAUTH_REDIRECT_URI,
                "response_type": "code",
                "scope": _OAUTH_SCOPE,
                "state": _OAUTH_STATE,
                "fast_login": "1",
                "self_redirect": "true",
            },
            headers={"User-Agent": "Mozilla/5.0"},
            timeout=self.timeout,
            trusted_host="open.weixin.qq.com",
            operation="QR initialization",
        )
        try:
            self._require_success(response, "QR initialization")
            match = _UUID_RE.search(response.text)
        finally:
            response.close()
        if match is None:
            raise WeChatProtocolError("WeChat QR initialization did not include a QR identifier")
        uuid = match.group(1)
        if _SAFE_UUID_RE.fullmatch(uuid) is None:
            raise WeChatProtocolError("WeChat QR initialization returned an invalid QR identifier")

        image_response, _ = self._get_trusted(
            f"{_QR_BASE_URL}{uuid}",
            params=None,
            headers={"User-Agent": "Mozilla/5.0"},
            timeout=self.timeout,
            trusted_host="open.weixin.qq.com",
            operation="QR image retrieval",
        )
        try:
            self._require_success(image_response, "QR image retrieval")
            image = image_response.content
        finally:
            image_response.close()
        self._validate_qr_image(image)
        return WeChatQRCode(uuid=uuid, image=image)

    def poll_qr(self, uuid: str) -> WeChatPollResult:
        self._ensure_open()
        safe_uuid = self._validated_uuid(uuid)
        try:
            response = self._client.get(
                _QR_POLL_URL,
                params={"uuid": safe_uuid, "_": str(int(time.time() * 1000))},
                headers={"User-Agent": "Mozilla/5.0"},
                timeout=max(self.timeout, _LOGIN_BUFFER_TIMEOUT),
                follow_redirects=False,
            )
        except httpx2.TimeoutException:
            return WeChatPollResult(status="pending")
        except httpx2.HTTPError:
            raise WeChatError("WeChat QR status request failed") from None
        try:
            self._require_success(response, "QR status request")
            text = response.text
        finally:
            response.close()

        error_match = _ERRCODE_RE.search(text)
        code_match = _CODE_RE.search(text)
        error_code = int(error_match.group(1)) if error_match is not None else None
        code = code_match.group(1) if code_match is not None else ""
        if error_code == 408:
            return WeChatPollResult(status="pending")
        if error_code == 404:
            return WeChatPollResult(status="scanned")
        if error_code == 403:
            return WeChatPollResult(status="cancelled")
        if error_code == 402:
            return WeChatPollResult(status="expired")
        if error_code == 405 and code:
            return WeChatPollResult(status="authorized", code=SecretStr(code))
        raise WeChatProtocolError("WeChat QR status response was not recognized")

    def exchange_code(self, code: SecretStr) -> WeChatCredentials:
        self._ensure_open()
        authorize_code = code.get_secret_value().strip()
        if not authorize_code:
            raise WeChatAuthorizationError("WeChat authorization code is empty")
        callback_cookies = httpx2.Cookies()
        response, callback_cookie_names = self._get_trusted(
            _CALLBACK_URL,
            params={"login_type": "WX", "code": authorize_code, "state": _OAUTH_STATE},
            headers={"User-Agent": "Mozilla/5.0"},
            timeout=self.timeout,
            trusted_host="yybadaccess.3g.qq.com",
            operation="OAuth callback",
            callback_cookies=callback_cookies,
        )
        try:
            if response.status_code not in _REDIRECT_STATUSES:
                self._require_success(response, "OAuth callback")
        finally:
            response.close()

        if not {"openid", "accesstoken"}.issubset(callback_cookie_names):
            raise WeChatProtocolError("OAuth callback did not set the required credential cookies")
        cookies = self._cookies_for_callback(callback_cookies)
        openid = cookies.get("openid", "")
        access_token = cookies.get("accesstoken", "")
        if not openid or not access_token:
            raise WeChatProtocolError("OAuth callback did not set the required credential cookies")

        expires_in = self._cookie_expiry(cookies.get("expires_in"))
        return WeChatCredentials(
            openid=SecretStr(openid),
            access_token=SecretStr(access_token),
            refresh_token=SecretStr(cookies.get("refreshtoken", "")),
            expires_at=int(time.time()) + expires_in,
        )

    def fetch_login_buffer(self, credentials: WeChatCredentials) -> SecretStr:
        self._ensure_open()
        openid = credentials.openid.get_secret_value()
        access_token = credentials.access_token.get_secret_value()
        refresh_token = credentials.refresh_token.get_secret_value()
        self._require_credential(openid, "OpenID")
        self._require_credential(access_token, "access token")
        self._validate_cookie_value(openid)
        self._validate_cookie_value(access_token)
        self._validate_cookie_value(refresh_token)

        body = self._json_bytes(
            {
                "extInfo": {
                    "listS": {
                        "unionid": {"value": [openid]},
                        "user_id": {"value": [openid]},
                        "access_token": {"value": [access_token]},
                    },
                    "listI": {"user_type": {"value": [0]}},
                }
            }
        )
        timestamp = str(int(time.time() * 1000))
        nonce = str(secrets.randbelow(10_000))
        headers = self._signed_headers(body, timestamp, nonce)
        headers["Cookie"] = (
            f"openid={openid}; accesstoken={access_token}; refreshtoken={refresh_token}"
        )
        response = self._post_json(
            _LOGIN_BUFFER_URL, body, headers, "login-buffer",
        )
        try:
            payload = self._success_payload(response, "login-buffer")
        finally:
            response.close()
        ext_info = payload.get("ext_info")
        list_s = ext_info.get("list_s") if isinstance(ext_info, dict) else None
        login_buffer = list_s.get("login_buffer") if isinstance(list_s, dict) else None
        values = login_buffer.get("value") if isinstance(login_buffer, dict) else None
        if not isinstance(values, list) or not values or not isinstance(values[0], str) or not values[0]:
            raise WeChatProtocolError("YYB login-buffer response did not include a login buffer")
        return SecretStr(values[0])

    def refresh(self, credentials: WeChatCredentials) -> WeChatCredentials:
        self._ensure_open()
        openid = credentials.openid.get_secret_value()
        access_token = credentials.access_token.get_secret_value()
        refresh_token = credentials.refresh_token.get_secret_value()
        self._require_credential(openid, "OpenID")
        self._require_credential(access_token, "access token")
        self._require_credential(refresh_token, "refresh token")
        body = self._json_bytes(
            {
                "userInfo": {
                    "openId": openid,
                    "refreshToken": refresh_token,
                    "accessToken": access_token,
                    "loginType": "WX",
                }
            }
        )
        timestamp = str(int(time.time() * 1000))
        nonce = str(secrets.randbelow(10_000))
        response = self._post_json(
            _REFRESH_TOKEN_URL,
            body,
            self._signed_headers(body, timestamp, nonce),
            "credential refresh",
        )
        try:
            payload = self._success_payload(response, "credential refresh")
        finally:
            response.close()
        user_info = payload.get("user_info")
        if not isinstance(user_info, dict):
            raise WeChatProtocolError("YYB credential refresh response was incomplete")
        new_access_token = user_info.get("access_token")
        if not isinstance(new_access_token, str) or not new_access_token:
            raise WeChatProtocolError("YYB credential refresh response was incomplete")
        new_refresh_token = user_info.get("refresh_token")
        if not isinstance(new_refresh_token, str):
            new_refresh_token = ""
        expires_in = self._json_expiry(user_info.get("expires_in"))
        return WeChatCredentials(
            openid=credentials.openid,
            access_token=SecretStr(new_access_token),
            refresh_token=SecretStr(new_refresh_token or refresh_token),
            expires_at=int(time.time()) + expires_in,
        )

    def close(self) -> None:
        if not self._closed:
            self._client.close()
            self._closed = True

    def _get_trusted(
        self,
        url: str,
        *,
        params: dict[str, str] | None,
        headers: dict[str, str],
        timeout: float,
        trusted_host: str,
        operation: str,
        callback_cookies: httpx2.Cookies | None = None,
    ) -> tuple[httpx2.Response, set[str]]:
        self._ensure_open()
        current_url = url
        current_params = params
        set_cookie_names: set[str] = set()
        for redirect_count in range(_MAX_REDIRECTS + 1):
            try:
                response = self._client.get(
                    current_url,
                    params=current_params,
                    headers=headers,
                    timeout=timeout,
                    follow_redirects=False,
                )
            except httpx2.HTTPError:
                raise WeChatError(f"WeChat {operation} request failed") from None
            current_params = None
            set_cookie_names.update(self._set_cookie_names(response))
            if callback_cookies is not None:
                callback_cookies.extract_cookies(response)
            if response.status_code not in _REDIRECT_STATUSES:
                return response, set_cookie_names
            if callback_cookies is not None:
                issued = self._cookies_for_callback(callback_cookies)
                if issued.get("openid") and issued.get("accesstoken"):
                    # OAuth is complete; its landing-page redirect is not part
                    # of the credential exchange and must not be visited.
                    return response, set_cookie_names
            location = response.headers.get("location")
            if location is None:
                return response, set_cookie_names
            try:
                target = urljoin(str(response.url), location)
                parsed = urlsplit(target)
                target_port = parsed.port
            except ValueError:
                response.close()
                raise WeChatProtocolError(f"WeChat {operation} used an invalid redirect") from None
            if (
                parsed.scheme != "https"
                or parsed.hostname != trusted_host
                or target_port not in (None, 443)
                or parsed.username is not None
                or parsed.password is not None
            ):
                response.close()
                raise WeChatProtocolError(f"WeChat {operation} redirect left its trusted origin")
            response.close()
            if redirect_count == _MAX_REDIRECTS:
                raise WeChatProtocolError(f"WeChat {operation} exceeded the redirect limit")
            current_url = target
        raise WeChatProtocolError(f"WeChat {operation} exceeded the redirect limit")

    def _post_json(
        self,
        url: str,
        body: bytes,
        headers: dict[str, str],
        operation: str,
    ) -> httpx2.Response:
        self._ensure_open()
        try:
            return self._client.post(
                url,
                content=body,
                headers=headers,
                timeout=self.timeout,
                follow_redirects=False,
            )
        except httpx2.HTTPError:
            raise WeChatError(f"YYB {operation} request failed") from None

    def _success_payload(self, response: httpx2.Response, operation: str) -> dict[str, JsonValue]:
        self._require_success(response, operation)
        try:
            payload: JsonValue = response.json()
        except ValueError:
            raise WeChatProtocolError(f"YYB {operation} response was not valid JSON") from None
        if not isinstance(payload, dict):
            raise WeChatProtocolError(f"YYB {operation} response was incomplete")
        response_code = payload.get("code")
        if type(response_code) is not int:
            raise WeChatProtocolError(f"YYB {operation} response was incomplete")
        if response_code != 0:
            raise WeChatAuthorizationError(f"YYB rejected {operation}")
        return payload

    @staticmethod
    def _signed_headers(body: bytes, timestamp: str, nonce: str) -> dict[str, str]:
        signature_input = body.decode("utf-8") + timestamp + _LOGIN_ACCESS_KEY + nonce
        signature = hashlib.md5(signature_input.encode("utf-8")).hexdigest()
        return {
            "Accept": "application/json",
            "Content-Type": "application/json",
            "Ual-Access-Businessid": _LOGIN_BUSINESS_ID,
            "Ual-Access-Timestamp": timestamp,
            "Ual-Access-Nonce": nonce,
            "Ual-Access-Signature": signature,
        }

    @staticmethod
    def _json_bytes(value: dict[str, JsonValue]) -> bytes:
        return json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode("utf-8")

    @staticmethod
    def _require_success(response: httpx2.Response, operation: str) -> None:
        if response.status_code < 200 or response.status_code >= 300:
            raise WeChatProtocolError(
                f"WeChat {operation} returned HTTP {response.status_code}"
            )

    @staticmethod
    def _set_cookie_names(response: httpx2.Response) -> set[str]:
        names: set[str] = set()
        for header in response.headers.get_list("set-cookie"):
            parsed = SimpleCookie()
            try:
                parsed.load(header)
            except CookieError:
                continue
            names.update(parsed.keys())
        return names

    @staticmethod
    def _cookies_for_callback(cookies: httpx2.Cookies) -> dict[str, str]:
        try:
            request = httpx2.Request("GET", _CALLBACK_URL)
            cookies.set_cookie_header(request)
            cookie_header = request.headers.get("cookie", "")
            parsed = SimpleCookie()
            parsed.load(cookie_header)
        except (CookieError, httpx2.CookieConflict, ValueError):
            raise WeChatProtocolError("OAuth callback cookies were invalid") from None
        return {name: morsel.value for name, morsel in parsed.items()}

    @staticmethod
    def _cookie_expiry(value: str | None) -> int:
        if not value:
            return 7200
        try:
            seconds = int(value, 10)
        except ValueError:
            return 7200
        return seconds if seconds > 0 else 7200

    @staticmethod
    def _json_expiry(value: object) -> int:
        # YYB returns expires_in as a decimal JSON string in live responses.
        if isinstance(value, str) and value.isascii() and value.isdecimal():
            value = int(value, 10)
        if value is None or value == 0:
            return 7200
        if type(value) is not int or value < 0:
            raise WeChatProtocolError("YYB credential refresh response had an invalid expiry")
        return value or 7200

    @staticmethod
    def _validated_uuid(uuid: str) -> str:
        if _SAFE_UUID_RE.fullmatch(uuid) is None:
            raise WeChatProtocolError("WeChat QR identifier was invalid")
        return uuid

    @classmethod
    def _validate_qr_image(cls, data: bytes) -> None:
        if not data:
            raise WeChatProtocolError("WeChat QR image response was empty")
        try:
            with Image.open(BytesIO(data)) as image:
                image.verify()
        except Exception:
            raise WeChatProtocolError("WeChat QR image response was not a valid image") from None

    @staticmethod
    def _require_credential(value: str, name: str) -> None:
        if not value:
            raise WeChatAuthorizationError(f"WeChat {name} is missing")

    @staticmethod
    def _validate_cookie_value(value: str) -> None:
        if any(char in value for char in "\r\n;\"\\"):
            raise WeChatAuthorizationError("WeChat credential contains an invalid cookie value")

    def _ensure_open(self) -> None:
        if self._closed:
            raise WeChatError("WeChat login client is closed")
