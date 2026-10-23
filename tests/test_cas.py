from __future__ import annotations

import unittest

import httpx2
from pydantic import ValidationError

from moon_uestc import (
    AuthenticationError,
    MoonClient,
    ProtocolError,
    ReauthenticationRequiredError,
)


PLATFORM = "https://jzsz.uestc.edu.cn"
CAS = "https://idas.uestc.edu.cn"


def user_response(identifier: str) -> httpx2.Response:
    return httpx2.Response(200, json={
        "code": 0,
        "data": {"userData": {
            "id": identifier, "fullName": "Example", "username": identifier,
            "role": "001", "roleName": "学生",
        }},
    })


class CASFlow:
    """A service contract: identity is accepted only after platform bootstrap."""

    def __init__(self) -> None:
        self.initialized = False
        self.identity_code = 0
        self.bootstrap_body: object = {"status": True, "code": 200, "data": True}
        self.redirect: str | None = None
        self.issue_session = True
        self.requests: list[httpx2.Request] = []

    def __call__(self, request: httpx2.Request) -> httpx2.Response:
        self.requests.append(request)
        cookie = request.headers.get("cookie", "")
        if "authorization" in request.headers or "x-checkin-ticket" in request.headers:
            raise AssertionError("business credentials leaked into the isolated exchange")
        if request.url.host == "idas.uestc.edu.cn":
            if "SESSION=" in cookie:
                raise AssertionError("platform SESSION leaked to CAS")
            if "CASTGC=valid-sso" not in cookie or "MULTIFACTOR_BROWSER_FINGERPRINT=trusted-browser" not in cookie:
                return httpx2.Response(200, text="<html>official login or secondary authentication</html>")
            if request.headers.get("user-agent") != "Browser/1.0":
                return httpx2.Response(200, text="<html>secondary authentication required</html>")
            if request.url.params["service"] != PLATFORM + "/":
                raise AssertionError("wrong CAS service")
            return httpx2.Response(302, headers=[
                ("Location", self.redirect or PLATFORM + "/?ticket=ST-one-time-secret"),
                ("Set-Cookie", "CASTGC=rotated-sso; Path=/authserver; HttpOnly"),
                ("Set-Cookie", "MULTIFACTOR_BROWSER_FINGERPRINT=rotated-browser; Path=/; HttpOnly"),
            ])
        if request.url.host != "jzsz.uestc.edu.cn":
            raise AssertionError("request escaped authentication origins")
        if "CASTGC=" in cookie or "MULTIFACTOR_BROWSER_FINGERPRINT=" in cookie:
            raise AssertionError("CAS credentials leaked to the business origin")
        if request.url.params.get("ticket") == "ST-one-time-secret":
            headers = [("Location", "/")]
            if self.issue_session:
                headers.append(("Set-Cookie", "SESSION=fresh-session; Path=/; HttpOnly"))
            return httpx2.Response(302, headers=headers)
        if request.url.path == "/":
            return httpx2.Response(200, text="<html>platform</html>")
        if request.url.path == "/api/isLoginFailure":
            self.initialized = True
            return httpx2.Response(200, json=self.bootstrap_body)
        if request.url.path == "/daily-checkin/api/auth/user-info":
            if self.identity_code:
                return httpx2.Response(200, json={"code": self.identity_code, "msg": "未登录或会话已失效"})
            if not self.initialized or "SESSION=fresh-session" not in cookie:
                return httpx2.Response(200, json={"code": 5000, "msg": "未登录或会话已失效"})
            return user_response("new-student")
        raise AssertionError("unexpected authentication path")


def business_response(request: httpx2.Request) -> httpx2.Response:
    if "SESSION=original-session" not in request.headers.get("cookie", ""):
        return httpx2.Response(200, json={"code": 5000, "msg": "未登录或会话已失效"})
    return user_response("original-student")


class CASSessionTests(unittest.TestCase):
    def test_exchange_verifies_new_identity_preserves_old_client_and_rotates_sso(self) -> None:
        flow = CASFlow()
        with MoonClient(
            session="original-session", bearer_token="private-bearer", checkin_ticket="private-ticket",
            transport=httpx2.MockTransport(business_response),
        ) as client:
            result = client.auth.exchange_session(
                castgc="valid-sso", browser_fingerprint="trusted-browser", user_agent="Browser/1.0",
                transport=httpx2.MockTransport(flow),
            )
            self.assertEqual(result.session.get_secret_value(), "fresh-session")
            self.assertEqual(result.user_info.user_data.id, "new-student")
            self.assertEqual(result.credentials.castgc.get_secret_value(), "rotated-sso")
            self.assertEqual(result.credentials.browser_fingerprint.get_secret_value(), "rotated-browser")
            self.assertEqual(client.auth.user_info().data.user_data.id, "original-student")
        for secret in ("fresh-session", "rotated-sso", "rotated-browser"):
            self.assertNotIn(secret, repr(result))
            self.assertNotIn(secret, result.model_dump_json())

    def test_official_secondary_authentication_is_reported_without_fallback(self) -> None:
        flow = CASFlow()
        with MoonClient(session="original-session", transport=httpx2.MockTransport(business_response)) as client:
            with self.assertRaises(ReauthenticationRequiredError):
                client.auth.exchange_session(
                    castgc="expired-sso", browser_fingerprint="trusted-browser", user_agent="Browser/1.0",
                    transport=httpx2.MockTransport(flow),
                )
            self.assertEqual(client.auth.user_info().data.user_data.id, "original-student")
        self.assertEqual([str(r.url.host) for r in flow.requests], ["idas.uestc.edu.cn"])

    def test_redirect_cannot_leak_ticket_to_another_origin_or_downgrade(self) -> None:
        targets = (
            "https://other.invalid/?ticket=ST-private",
            "http://idas.uestc.edu.cn/authserver/login?ticket=ST-private",
            "https://user:password@jzsz.uestc.edu.cn/?ticket=ST-private",
        )
        for target in targets:
            with self.subTest(target=target):
                flow = CASFlow()
                flow.redirect = target
                with MoonClient() as client:
                    with self.assertRaises(ProtocolError) as caught:
                        client.auth.exchange_session(
                            castgc="valid-sso", browser_fingerprint="trusted-browser", user_agent="Browser/1.0",
                            transport=httpx2.MockTransport(flow),
                        )
                self.assertNotIn("ST-private", str(caught.exception))
                self.assertNotIn("password", str(caught.exception))
                self.assertEqual(len(flow.requests), 1)

    def test_platform_check_must_confirm_login_not_just_http_success(self) -> None:
        flow = CASFlow()
        flow.bootstrap_body = {"status": True, "code": 200, "data": False}
        with MoonClient() as client:
            with self.assertRaises(ReauthenticationRequiredError):
                client.auth.exchange_session(
                    castgc="valid-sso", browser_fingerprint="trusted-browser", user_agent="Browser/1.0",
                    transport=httpx2.MockTransport(flow),
                )
        self.assertFalse(any(r.url.path.endswith("user-info") for r in flow.requests))

    def test_platform_status_does_not_accept_string_true(self) -> None:
        flow = CASFlow()
        flow.bootstrap_body = {"status": True, "code": 200, "data": "true"}
        with MoonClient() as client:
            with self.assertRaises(ProtocolError):
                client.auth.exchange_session(
                    castgc="valid-sso", browser_fingerprint="trusted-browser", user_agent="Browser/1.0",
                    transport=httpx2.MockTransport(flow),
                )

    def test_new_cookie_without_application_identity_is_not_a_success(self) -> None:
        flow = CASFlow()
        flow.identity_code = 5000
        with MoonClient() as client:
            with self.assertRaises(AuthenticationError):
                client.auth.exchange_session(
                    castgc="valid-sso", browser_fingerprint="trusted-browser", user_agent="Browser/1.0",
                    transport=httpx2.MockTransport(flow),
                )

    def test_missing_new_session_is_not_replaced_with_current_session(self) -> None:
        flow = CASFlow()
        flow.issue_session = False
        with MoonClient(session="original-session") as client:
            with self.assertRaises(AuthenticationError):
                client.auth.exchange_session(
                    castgc="valid-sso", browser_fingerprint="trusted-browser", user_agent="Browser/1.0",
                    transport=httpx2.MockTransport(flow),
                )

    def test_cookie_header_injection_is_rejected_before_network(self) -> None:
        for secret in ("", "private-sso\r\nInjected: true", "private-sso; other=value"):
            with self.subTest(secret=repr(secret)):
                with MoonClient() as client:
                    with self.assertRaises(ValidationError) as caught:
                        client.auth.exchange_session(
                            castgc=secret, browser_fingerprint="trusted-browser", user_agent="Browser/1.0",
                        )
                self.assertNotIn("private-sso", str(caught.exception))


if __name__ == "__main__":
    unittest.main()
