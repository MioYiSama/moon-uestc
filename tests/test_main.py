from __future__ import annotations

import io
import json
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from urllib.parse import parse_qs
from unittest.mock import patch

import httpx2
from pydantic import SecretStr

import main
from moon_uestc import AuthenticationError, IDASClient, MoonClient, login_interactive
from moon_uestc.models import CASCredentials


FINGERPRINT = "A" * 32
ROTATED_FINGERPRINT = "B" * 32
MFA_PATH = "/authserver/reAuthCheck/reAuthLoginView.do"


class SMSFlow:
    """Require a real challenge transition before issuing a business session."""

    def __init__(self) -> None:
        self.authorized = False
        self.initialized = False
        self.sms_sent = False
        self.trusted = False
        self.offer_sms = True
        self.service = main.SERVICE_URL
        self.submissions = 0
        self.password_logins = 0

    def __call__(self, request: httpx2.Request) -> httpx2.Response:
        path = request.url.path
        cookie = request.headers.get("cookie", "")
        if request.url.host == "idas.uestc.edu.cn":
            if "SESSION=" in cookie:
                raise AssertionError("business session leaked to CAS")
            if path == "/authserver/bfp/info":
                fingerprint = request.url.params["bfp"]
                return httpx2.Response(200, headers={
                    "Set-Cookie": f"MULTIFACTOR_BROWSER_FINGERPRINT={fingerprint}; Path=/; Secure",
                })
            if path == "/authserver/checkNeedCaptcha.htl":
                return httpx2.Response(200, json={"isNeed": False})
            if path == "/personalInfo/index":
                return httpx2.Response(200, text="<html>logged in</html>")
            if path == "/authserver/login":
                if request.method == "POST":
                    if "JSESSIONID=password-flow" not in cookie:
                        raise AssertionError("password form state was lost")
                    self.password_logins += 1
                    return httpx2.Response(302, headers=[
                        ("Location", "/personalInfo/index"),
                        ("Set-Cookie", "CASTGC=valid-cas; Path=/authserver; Secure"),
                    ])
                if "service" not in request.url.params:
                    return httpx2.Response(200, text="""
                        <form id="pwdFromId">
                          <input type="hidden" name="execution" value="e1s1">
                          <input type="hidden" id="pwdEncryptSalt" value="0123456789abcdef">
                          <input type="text" name="username"><input type="hidden" name="password">
                        </form><script>var captchaSwitch = '1';</script>
                    """, headers={"Set-Cookie": "JSESSIONID=password-flow; Path=/authserver; Secure"})
                if not self.authorized:
                    return httpx2.Response(302, headers=[
                        ("Location", MFA_PATH),
                        ("Set-Cookie", "JSESSIONID=challenge-state; Path=/authserver; Secure"),
                    ])
                if not all(value in cookie for value in (
                    "JSESSIONID=verified-state", "CASTGC=rotated-cas",
                    "MULTIFACTOR_BROWSER_FINGERPRINT=" + ROTATED_FINGERPRINT,
                )):
                    raise AssertionError("verified cookies did not reach the SDK jar")
                return httpx2.Response(302, headers={
                    "Location": main.SERVICE_URL + "?ticket=ST-test-ticket",
                })
            if path == MFA_PATH:
                body = "<script>var reAuthParams = " + json.dumps({
                    "service": self.service, "reAuthType": "8",
                    "isMultifactor": "true", "reAuthUserId": "test-student",
                }) + ";</script>"
                if self.offer_sms:
                    body += '<button data-type="3">短信验证码</button>'
                return httpx2.Response(200, text=body)
            if request.method != "POST":
                raise AssertionError("unexpected CAS request")
            data = parse_qs(request.content.decode(), keep_blank_values=True)
            if request.headers.get("x-requested-with") != "XMLHttpRequest":
                raise AssertionError("missing AJAX request header")
            if path.endswith("/changeReAuthType.do"):
                if "JSESSIONID=challenge-state" not in cookie:
                    raise AssertionError("challenge state was lost")
                return httpx2.Response(200, json={"code": "1", "data": {"reAuthType": "3"}}, headers={
                    "Set-Cookie": "route=challenge-node; Path=/authserver; Secure",
                })
            if path.endswith("/getDynamicCodeByReauth.do"):
                if "JSESSIONID=challenge-state" not in cookie or "route=challenge-node" not in cookie:
                    raise AssertionError("SMS request lost the selected challenge")
                self.sms_sent = True
                return httpx2.Response(200, json={"res": "success"}, headers={
                    "Set-Cookie": "JSESSIONID=sent-state; Path=/authserver; Secure",
                })
            if path.endswith("/reAuthSubmit.do"):
                self.submissions += 1
                if not self.sms_sent or "JSESSIONID=sent-state" not in cookie:
                    raise AssertionError("SMS state was lost before verification")
                if data.get("dynamicCode") != ["123456"]:
                    return httpx2.Response(200, json={"code": "reAuth_failed", "msg": "invalid code"})
                self.authorized = True
                self.trusted = data.get("skipTmpReAuth") == ["true"]
                return httpx2.Response(200, json={"code": "success"}, headers=[
                    ("Set-Cookie", "JSESSIONID=verified-state; Path=/authserver; Secure"),
                    ("Set-Cookie", "CASTGC=rotated-cas; Path=/authserver; Secure"),
                    ("Set-Cookie", "MULTIFACTOR_BROWSER_FINGERPRINT=" + ROTATED_FINGERPRINT + "; Path=/; Secure"),
                ])
        elif request.url.host == "jzsz.uestc.edu.cn":
            if "CASTGC=" in cookie or "JSESSIONID=" in cookie:
                raise AssertionError("CAS credentials leaked to business origin")
            if path == "/" and self.authorized:
                return httpx2.Response(200, headers={"Set-Cookie": "SESSION=verified-session; Path=/; Secure"})
            if path == "/api/isLoginFailure" and self.authorized:
                self.initialized = True
                return httpx2.Response(200, json={"status": True, "code": 200, "data": True})
            if not self.initialized or "SESSION=verified-session" not in cookie:
                return httpx2.Response(200, json={"code": 5000, "msg": "会话已失效"})
            if path.endswith("/user-info"):
                return httpx2.Response(200, json={"code": 0, "data": {"userData": {
                    "id": "test-student", "username": "test-student", "fullName": "Test",
                    "role": "001", "roleName": "学生",
                }}})
            if path.endswith("/today"):
                return httpx2.Response(200, json={"code": 0, "data": None})
        raise AssertionError("unexpected request: " + path)


class MainAuthenticationTests(unittest.TestCase):
    def setUp(self) -> None:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.cache_path = Path(temporary.name) / "auth.json"
        path_patch = patch.object(main, "CACHE_PATH", self.cache_path)
        path_patch.start()
        self.addCleanup(path_patch.stop)
        self.flow = SMSFlow()
        transport_type = main.MFAExchangeTransport

        def business_client(*, session: str | SecretStr | None = None) -> MoonClient:
            return MoonClient(session=session, transport=httpx2.MockTransport(self.flow))

        def exchange_transport(credentials: CASCredentials) -> main.MFAExchangeTransport:
            return transport_type(credentials, transport=httpx2.MockTransport(self.flow))

        def idas_client(*, user_agent: str) -> IDASClient:
            return IDASClient(user_agent=user_agent, transport=httpx2.MockTransport(self.flow))

        patches = [
            patch.object(main, "MoonClient", side_effect=business_client),
            patch.object(main, "MFAExchangeTransport", side_effect=exchange_transport),
            patch.object(main, "IDASClient", side_effect=idas_client),
            patch.object(main, "login_interactive", side_effect=AssertionError("must not repeat password login for MFA")),
        ]
        for mock_patch in patches:
            mock_patch.start()
            self.addCleanup(mock_patch.stop)
        main.save_cache(main.AuthCache(
            castgc=SecretStr("valid-cas"), browser_fingerprint=SecretStr(FINGERPRINT),
        ))

    def test_password_login_continues_through_service_mfa_before_caching_session(self) -> None:
        self.cache_path.unlink()
        with patch.object(main, "login_interactive", login_interactive), patch("builtins.input", side_effect=["test-student", "", "y"]), patch.object(main, "getpass", side_effect=["fake-password", "123456"]), redirect_stdout(io.StringIO()):
            main.main()
        self.assertEqual(self.flow.password_logins, 1)
        self.assertTrue(self.flow.authorized)
        self.assertTrue(self.flow.initialized)
        self.assertEqual(main.load_cache().session, SecretStr("verified-session"))
        self.assertNotIn("fake-password", self.cache_path.read_text())

    def test_sms_verification_rotates_credentials_and_next_run_reuses_session(self) -> None:
        with patch("builtins.input", side_effect=["", "y"]), patch.object(main, "getpass", return_value="123456"), redirect_stdout(io.StringIO()):
            main.main()
        saved = json.loads(self.cache_path.read_text())
        self.assertEqual(saved["session"], "verified-session")
        self.assertEqual(saved["castgc"], "rotated-cas")
        self.assertEqual(saved["browser_fingerprint"], ROTATED_FINGERPRINT)
        self.assertTrue(self.flow.trusted)
        self.assertEqual(self.cache_path.stat().st_mode & 0o777, 0o600)
        self.assertNotIn("123456", self.cache_path.read_text())
        with patch("builtins.input", side_effect=AssertionError("cached SESSION must not prompt")), patch.object(main, "getpass", side_effect=AssertionError("cached SESSION must not prompt")), redirect_stdout(io.StringIO()):
            main.main()
        self.assertEqual(self.flow.submissions, 1)

    def test_wrong_sms_code_does_not_cache_a_session_or_repeat_password_login(self) -> None:
        before = self.cache_path.read_bytes()
        with patch("builtins.input", side_effect=["", ""]), patch.object(main, "getpass", return_value="000000"), redirect_stdout(io.StringIO()):
            with self.assertRaisesRegex(AuthenticationError, "短信认证被拒绝"):
                main.main()
        self.assertFalse(self.flow.authorized)
        self.assertFalse(self.flow.initialized)
        self.assertEqual(self.cache_path.read_bytes(), before)
        self.assertEqual(self.flow.submissions, 1)

    def test_trusting_device_requires_explicit_consent(self) -> None:
        with patch("builtins.input", side_effect=["", ""]), patch.object(main, "getpass", return_value="123456"), redirect_stdout(io.StringIO()):
            main.main()
        self.assertTrue(self.flow.authorized)
        self.assertFalse(self.flow.trusted)

    def test_challenge_without_sms_does_not_send_a_code(self) -> None:
        self.flow.offer_sms = False
        with patch("builtins.input", side_effect=AssertionError("unsupported challenge must not prompt")):
            with self.assertRaisesRegex(AuthenticationError, "未提供短信方式"):
                main.main()
        self.assertFalse(self.flow.sms_sent)
        self.assertFalse(self.flow.authorized)
        self.assertIsNone(main.load_cache().session)

    def test_unexpected_service_is_not_authorized(self) -> None:
        self.flow.service = "https://attacker.invalid/"
        with self.assertRaisesRegex(AuthenticationError, "目标业务不匹配"):
            main.main()
        self.assertFalse(self.flow.sms_sent)
        self.assertIsNone(main.load_cache().session)


if __name__ == "__main__":
    unittest.main()
