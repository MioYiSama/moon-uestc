from __future__ import annotations

import unittest
from urllib.parse import parse_qs
from unittest.mock import patch

import httpx2

from moon_uestc import (
    AuthenticationError,
    IDASClient,
    ReauthenticationRequiredError,
    login_interactive,
)


PAGE = '''<html><script>var captchaSwitch="1";</script>
<form id="pwdFromId"><input type="hidden" name="execution" value="e1s1">
<input type="hidden" id="pwdEncryptSalt" value="0123456789abcdef">
<input type="hidden" name="cllt" value="userNameLogin"></form></html>'''


class CaptchaFlow:
    def __init__(self, *, reject: bool = False, mfa: bool = False) -> None:
        self.image_requested = False
        self.posts = 0
        self.reject = reject
        self.mfa = mfa

    def __call__(self, request: httpx2.Request) -> httpx2.Response:
        path = request.url.path
        if path == "/authserver/bfp/info":
            return httpx2.Response(200, headers={
                "Set-Cookie": "MULTIFACTOR_BROWSER_FINGERPRINT=" + request.url.params["bfp"] + "; Path=/; HttpOnly",
            })
        if path == "/authserver/login" and request.method == "GET":
            return httpx2.Response(200, text=PAGE, headers={
                "Set-Cookie": "JSESSIONID=human-flow; Path=/authserver; HttpOnly",
            })
        if path == "/authserver/checkNeedCaptcha.htl":
            return httpx2.Response(200, json={"isNeed": not self.mfa})
        if path == "/authserver/getCaptcha.htl":
            if "JSESSIONID=human-flow" not in request.headers.get("cookie", ""):
                raise AssertionError("CAPTCHA was fetched outside the login session")
            self.image_requested = True
            return httpx2.Response(200, content=b"human-image")
        if path == "/authserver/login" and request.method == "POST":
            self.posts += 1
            if self.mfa:
                return httpx2.Response(200, text="<form id='otpForm'><input name='otp'></form>")
            fields = parse_qs(request.content.decode())
            accepted = (
                not self.reject and self.image_requested
                and fields.get("captcha") == ["Ab92"]
                and "JSESSIONID=human-flow" in request.headers.get("cookie", "")
            )
            if not accepted:
                return httpx2.Response(200, text=PAGE)
            return httpx2.Response(302, headers={
                "Location": "/personalInfo/home",
                "Set-Cookie": "CASTGC=human-accepted; Path=/authserver; HttpOnly",
            })
        if path == "/personalInfo/home":
            return httpx2.Response(200, text="<html>Personal center</html>")
        raise AssertionError("Unexpected authentication endpoint")


def human_answer(client: IDASClient, kind: str) -> str:
    if kind != "image" or client.get_image_captcha() != b"human-image":
        raise AssertionError("Human received the wrong challenge")
    return "Ab92"


class InteractiveLoginTests(unittest.TestCase):
    def test_human_answer_completes_same_session_and_returns_issued_cookies(self) -> None:
        flow = CaptchaFlow()
        with IDASClient("terminal-human/1", transport=httpx2.MockTransport(flow)) as client:
            with patch("moon_uestc.interactive.solve_captcha", side_effect=human_answer):
                credentials = login_interactive(client, "student", "private-password")
        self.assertEqual(credentials.castgc.get_secret_value(), "human-accepted")
        self.assertEqual(flow.posts, 1)

    def test_rejected_continuation_does_not_retry_password_or_return_credentials(self) -> None:
        flow = CaptchaFlow(reject=True)
        with IDASClient("terminal-human/1", transport=httpx2.MockTransport(flow)) as client:
            with patch("moon_uestc.interactive.solve_captcha", side_effect=human_answer):
                with self.assertRaises(AuthenticationError):
                    login_interactive(client, "student", "private-password")
            with self.assertRaises(AuthenticationError):
                client.credentials()
        self.assertEqual(flow.posts, 1)

    def test_cancelled_challenge_never_submits_the_password(self) -> None:
        flow = CaptchaFlow()
        with IDASClient("terminal-human/1", transport=httpx2.MockTransport(flow)) as client:
            with patch("moon_uestc.interactive.solve_captcha", side_effect=KeyboardInterrupt):
                with self.assertRaises(KeyboardInterrupt):
                    login_interactive(client, "student", "private-password")
        self.assertEqual(flow.posts, 0)

    def test_secondary_authentication_does_not_open_a_captcha_solver(self) -> None:
        flow = CaptchaFlow(mfa=True)
        with IDASClient("terminal-human/1", transport=httpx2.MockTransport(flow)) as client:
            with patch("moon_uestc.interactive.solve_captcha", side_effect=AssertionError("MFA is not CAPTCHA")):
                with self.assertRaises(ReauthenticationRequiredError):
                    login_interactive(client, "student", "private-password")
        self.assertEqual(flow.posts, 1)


if __name__ == "__main__":
    unittest.main()
