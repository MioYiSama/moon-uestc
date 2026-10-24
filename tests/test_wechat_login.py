from __future__ import annotations

import base64
import hashlib
import json
import time
import unittest
from collections.abc import Callable
from unittest.mock import patch

import httpx2
from pydantic import SecretStr

from moon_uestc._wechat_login import LoginClient
from moon_uestc.wechat_models import (
    WeChatAuthorizationError,
    WeChatCredentials,
    WeChatProtocolError,
)


PNG = base64.b64decode(
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
)
LOGIN_BUFFER_URL = "/pc_yyb_auth/pcyyb_get_wx_login_buffer_auth"
REFRESH_URL = "/pc_yyb_auth/pcyyb_refresh_token_auth"
CALLBACK_URL = "/pc_yyb/pcyyb_oauth"
ACCESS_KEY = "wgrdg373hy26ww2"


def new_client(
    handler: Callable[[httpx2.Request], httpx2.Response],
) -> LoginClient:
    return LoginClient(transport=httpx2.MockTransport(handler))


def credentials(
    *,
    openid: str = "private-openid",
    access_token: str = "private-access",
    refresh_token: str = "private-refresh",
) -> WeChatCredentials:
    return WeChatCredentials(
        openid=SecretStr(openid),
        access_token=SecretStr(access_token),
        refresh_token=SecretStr(refresh_token),
        expires_at=int(time.time()) + 3600,
    )


def callback_response(
    *,
    openid: str = "new-openid",
    access_token: str = "new-access",
    refresh_token: str = "new-refresh",
) -> httpx2.Response:
    return httpx2.Response(
        200,
        text="authorized",
        headers=[
            ("set-cookie", f"openid={openid}; Path=/; Secure"),
            ("set-cookie", f"accesstoken={access_token}; Path=/; Secure"),
            ("set-cookie", f"refreshtoken={refresh_token}; Path=/; Secure"),
            ("set-cookie", "expires_in=3600; Path=/; Secure"),
        ],
    )


class WeChatLoginTests(unittest.TestCase):
    def test_create_qr_parses_uuid_and_validates_image(self) -> None:
        requests: list[httpx2.Request] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            requests.append(request)
            if request.url.path == "/connect/qrconnect":
                return httpx2.Response(
                    200,
                    text='<img src="/connect/qrcode/test-uuid">',
                )
            if request.url.path == "/connect/qrcode/test-uuid":
                return httpx2.Response(200, content=PNG)
            raise AssertionError(f"unexpected request path: {request.url.path}")

        client = new_client(handler)
        try:
            result = client.create_qr()
        finally:
            client.close()

        self.assertEqual(result.uuid, "test-uuid")
        self.assertEqual(result.image, PNG)
        self.assertEqual(requests[0].url.params["appid"], "wxd44977328b36e647")
        self.assertEqual(requests[0].url.params["response_type"], "code")
        self.assertEqual(requests[1].headers["user-agent"], "Mozilla/5.0")

    def test_qr_creation_rejects_untrusted_uuid_and_invalid_image(self) -> None:
        for qr_html, image in (
            ('<img src="/connect/qrcode/bad/segment">', PNG),
            ('<img src="/connect/qrcode/valid-uuid">', b"not an image"),
        ):
            with self.subTest(qr_html=qr_html):
                def handler(request: httpx2.Request) -> httpx2.Response:
                    if request.url.path == "/connect/qrconnect":
                        return httpx2.Response(200, text=qr_html)
                    return httpx2.Response(200, content=image)

                client = new_client(handler)
                try:
                    with self.assertRaises(WeChatProtocolError):
                        client.create_qr()
                finally:
                    client.close()

    def test_poll_maps_expired_cancelled_and_authorized_results(self) -> None:
        cases = (
            ("window.wx_errcode=402;", "expired", None),
            ("window.wx_errcode=403;", "cancelled", None),
            ("window.wx_errcode=405;window.wx_code='private-code';", "authorized", "private-code"),
        )
        for body, expected_status, expected_code in cases:
            with self.subTest(status=expected_status):
                def handler(request: httpx2.Request) -> httpx2.Response:
                    self.assertEqual(request.url.params["uuid"], "test-uuid")
                    return httpx2.Response(200, text=body)

                client = new_client(handler)
                try:
                    result = client.poll_qr("test-uuid")
                finally:
                    client.close()
                self.assertEqual(result.status, expected_status)
                if expected_code is None:
                    self.assertIsNone(result.code)
                else:
                    self.assertIsNotNone(result.code)
                    assert result.code is not None
                    self.assertEqual(result.code.get_secret_value(), expected_code)
                    self.assertNotIn(expected_code, repr(result))

    def test_poll_rejects_invalid_uuid_before_request(self) -> None:
        def unexpected_request(_request: httpx2.Request) -> httpx2.Response:
            raise AssertionError("invalid UUID must not trigger a request")

        client = new_client(unexpected_request)
        try:
            with self.assertRaises(WeChatProtocolError):
                client.poll_qr("../../private")
        finally:
            client.close()

    def test_exchange_code_requires_callback_credentials_and_rejects_stale_cookies(self) -> None:
        calls = 0

        def handler(request: httpx2.Request) -> httpx2.Response:
            nonlocal calls
            self.assertEqual(request.url.path, CALLBACK_URL)
            calls += 1
            if calls == 1:
                return callback_response()
            return httpx2.Response(200, text="no credentials in this callback")

        client = new_client(handler)
        try:
            first = client.exchange_code(SecretStr("first-code"))
            self.assertEqual(first.openid.get_secret_value(), "new-openid")
            with self.assertRaises(WeChatProtocolError) as caught:
                client.exchange_code(SecretStr("second-code"))
        finally:
            client.close()
        self.assertNotIn("new-openid", str(caught.exception))
        self.assertNotIn("no credentials in this callback", str(caught.exception))

    def test_exchange_code_uses_only_callback_scoped_cookies(self) -> None:
        def handler(_request: httpx2.Request) -> httpx2.Response:
            return httpx2.Response(
                200,
                headers=[
                    ("set-cookie", "openid=private-openid; Path=/; Secure"),
                    ("set-cookie", "accesstoken=private-access; Path=/wrong; Secure"),
                ],
            )

        client = new_client(handler)
        try:
            with self.assertRaises(WeChatProtocolError) as caught:
                client.exchange_code(SecretStr("private-code"))
        finally:
            client.close()
        for secret in ("private-openid", "private-access", "private-code"):
            self.assertNotIn(secret, str(caught.exception))

    def test_callback_finishes_on_issued_credentials_without_visiting_landing_page(self) -> None:
        def handler(request: httpx2.Request) -> httpx2.Response:
            self.assertEqual(request.url.host, "yybadaccess.3g.qq.com")
            self.assertEqual(request.url.path, CALLBACK_URL)
            return httpx2.Response(302, headers=[
                *callback_response().headers.multi_items(),
                ("location", "https://landing.example/"),
            ])

        client = new_client(handler)
        try:
            result = client.exchange_code(SecretStr("private-code"))
        finally:
            client.close()
        self.assertEqual(result.openid, SecretStr("new-openid"))
        self.assertEqual(result.access_token, SecretStr("new-access"))
        self.assertEqual(result.refresh_token, SecretStr("new-refresh"))

    def test_callback_follows_same_origin_only_until_credentials_are_issued(self) -> None:
        paths: list[str] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            self.assertEqual(request.url.host, "yybadaccess.3g.qq.com")
            paths.append(request.url.path)
            if request.url.path == CALLBACK_URL:
                return httpx2.Response(302, headers={"location": "/pc_yyb/finish"})
            self.assertEqual(request.url.path, "/pc_yyb/finish")
            return httpx2.Response(302, headers=[
                *callback_response().headers.multi_items(),
                ("location", "https://landing.example/"),
            ])

        client = new_client(handler)
        try:
            result = client.exchange_code(SecretStr("private-code"))
        finally:
            client.close()
        self.assertEqual(result.access_token, SecretStr("new-access"))
        self.assertEqual(paths, [CALLBACK_URL, "/pc_yyb/finish"])

    def test_callback_redirect_cannot_combine_old_credentials_with_wrong_scope_cookies(self) -> None:
        def handler(request: httpx2.Request) -> httpx2.Response:
            self.assertEqual(request.url.host, "yybadaccess.3g.qq.com")
            if request.url.params["code"] == "first-code":
                return callback_response()
            return httpx2.Response(302, headers=[
                ("set-cookie", "openid=second-openid; Path=/; Secure"),
                ("set-cookie", "accesstoken=second-access; Path=/wrong; Secure"),
                ("location", "https://landing.example/"),
            ])

        client = new_client(handler)
        try:
            client.exchange_code(SecretStr("first-code"))
            with self.assertRaises(WeChatProtocolError):
                client.exchange_code(SecretStr("second-code"))
        finally:
            client.close()

    def test_callback_redirect_cannot_leak_authorize_code(self) -> None:
        requests: list[httpx2.Request] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            requests.append(request)
            return httpx2.Response(
                302,
                headers={"location": "https://attacker.example/collect"},
                text="private callback payload",
            )

        client = new_client(handler)
        try:
            with self.assertRaises(WeChatProtocolError) as caught:
                client.exchange_code(SecretStr("private-code"))
        finally:
            client.close()
        self.assertEqual(len(requests), 1)
        for secret in ("private-code", "private callback payload", "attacker.example"):
            self.assertNotIn(secret, str(caught.exception))

    def test_callback_http_error_is_sanitized(self) -> None:
        def handler(_request: httpx2.Request) -> httpx2.Response:
            return httpx2.Response(502, text="private-code private-token upstream-body")

        client = new_client(handler)
        try:
            with self.assertRaises(WeChatProtocolError) as caught:
                client.exchange_code(SecretStr("private-code"))
        finally:
            client.close()
        for secret in ("private-code", "private-token", "upstream-body"):
            self.assertNotIn(secret, str(caught.exception))

    def test_fetch_login_buffer_signs_request_and_hides_upstream_rejection(self) -> None:
        requests: list[httpx2.Request] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            requests.append(request)
            return httpx2.Response(
                200,
                json={
                    "code": -101,
                    "msg": "private rejection payload",
                },
            )

        client = new_client(handler)
        try:
            with self.assertRaises(WeChatAuthorizationError) as caught:
                client.fetch_login_buffer(credentials())
        finally:
            client.close()
        request = requests[0]
        body = request.content.decode("utf-8")
        self.assertEqual(request.url.path, LOGIN_BUFFER_URL)
        self.assertEqual(request.headers["ual-access-businessid"], "pc_yyb_auth")
        self.assertEqual(
            request.headers["ual-access-signature"],
            hashlib.md5(
                (body + request.headers["ual-access-timestamp"] + ACCESS_KEY + request.headers["ual-access-nonce"]).encode()
            ).hexdigest(),
        )
        self.assertEqual(
            json.loads(body),
            {
                "extInfo": {
                    "listS": {
                        "unionid": {"value": ["private-openid"]},
                        "user_id": {"value": ["private-openid"]},
                        "access_token": {"value": ["private-access"]},
                    },
                    "listI": {"user_type": {"value": [0]}},
                }
            },
        )
        self.assertEqual(
            request.headers["cookie"],
            "openid=private-openid; accesstoken=private-access; refreshtoken=private-refresh",
        )
        for secret in ("private-openid", "private-access", "private-refresh", "private rejection payload"):
            self.assertNotIn(secret, str(caught.exception))


    def test_refresh_rotates_credentials_used_by_next_login_buffer_request(self) -> None:
        requests: list[httpx2.Request] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            requests.append(request)
            if request.url.path == REFRESH_URL:
                return httpx2.Response(
                    200,
                    json={
                        "code": 0,
                        "user_info": {
                            "access_token": "rotated-access",
                            "refresh_token": "rotated-refresh",
                            "expires_in": 3600,
                        },
                    },
                )
            if request.url.path == LOGIN_BUFFER_URL:
                return httpx2.Response(
                    200,
                    json={
                        "code": 0,
                        "ext_info": {
                            "list_s": {"login_buffer": {"value": ["rotated-login-buffer"]}}
                        },
                    },
                )
            raise AssertionError(f"unexpected request path: {request.url.path}")

        original = credentials()
        client = new_client(handler)
        try:
            refreshed = client.refresh(original)
            login_buffer = client.fetch_login_buffer(refreshed)
        finally:
            client.close()

        self.assertEqual(original.access_token.get_secret_value(), "private-access")
        self.assertEqual(refreshed.access_token.get_secret_value(), "rotated-access")
        self.assertEqual(refreshed.refresh_token.get_secret_value(), "rotated-refresh")
        self.assertGreater(refreshed.expires_at, int(time.time()))
        self.assertEqual(login_buffer.get_secret_value(), "rotated-login-buffer")
        login_request = requests[1]
        self.assertEqual(login_request.headers["cookie"], "openid=private-openid; accesstoken=rotated-access; refreshtoken=rotated-refresh")
        self.assertEqual(
            json.loads(login_request.content)["extInfo"]["listS"]["access_token"]["value"],
            ["rotated-access"],
        )

    def test_refresh_accepts_expiry_seconds_encoded_as_a_string(self) -> None:
        def handler(_request: httpx2.Request) -> httpx2.Response:
            return httpx2.Response(200, json={
                "code": 0,
                "user_info": {"access_token": "rotated-access", "expires_in": "7200"},
            })

        client = new_client(handler)
        try:
            with patch("moon_uestc._wechat_login.time.time", return_value=1000):
                refreshed = client.refresh(credentials())
        finally:
            client.close()
        self.assertEqual(refreshed.expires_at, 8200)

    def test_refresh_preserves_old_refresh_token_when_not_rotated(self) -> None:
        def handler(_request: httpx2.Request) -> httpx2.Response:
            return httpx2.Response(
                200,
                json={"code": 0, "user_info": {"access_token": "rotated-access"}},
            )

        client = new_client(handler)
        try:
            refreshed = client.refresh(credentials())
        finally:
            client.close()
        self.assertEqual(refreshed.access_token.get_secret_value(), "rotated-access")
        self.assertEqual(refreshed.refresh_token.get_secret_value(), "private-refresh")

    def test_refresh_rejection_is_sanitized(self) -> None:
        def handler(_request: httpx2.Request) -> httpx2.Response:
            return httpx2.Response(
                200,
                json={"code": -1, "msg": "private-refresh private-upstream-payload"},
            )

        client = new_client(handler)
        try:
            with self.assertRaises(WeChatAuthorizationError) as caught:
                client.refresh(credentials())
        finally:
            client.close()
        for secret in ("private-refresh", "private-upstream-payload", "private-access"):
            self.assertNotIn(secret, str(caught.exception))

    def test_failed_refresh_does_not_return_partial_credentials(self) -> None:
        def handler(_request: httpx2.Request) -> httpx2.Response:
            return httpx2.Response(200, json={"code": 0, "user_info": {"refresh_token": "rotated"}})

        client = new_client(handler)
        try:
            with self.assertRaises(WeChatProtocolError) as caught:
                client.refresh(credentials())
        finally:
            client.close()
        self.assertNotIn("rotated", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
