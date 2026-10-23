from __future__ import annotations

import base64
import json
import unittest
from collections.abc import Sequence
from urllib.parse import parse_qs

import httpx2
from cryptography.hazmat.primitives import padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from pydantic import SecretStr

from moon_uestc.errors import (
    AuthenticationError,
    CaptchaRequiredError,
    MoonError,
    ProtocolError,
    ReauthenticationRequiredError,
)
from moon_uestc.idas import IDASClient
from moon_uestc.idas_models import FingerprintComponents, SliderTrack


USER_AGENT = "Behavioral-Test-Browser/1.0"
FINGERPRINT = "0123456789ABCDEF0123456789ABCDEF"
LOGIN_SALT = "0123456789abcdef"
SLIDER_SALT = "ABCDEFGHIJKLMNOP"
PASSWORD = "fake-password-雪"
PREFIX_ALPHABET = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"


def login_page(execution: str = "e1s1", *, captcha_switch: str = "2") -> str:
    """A realistic multi-form page with unrelated templates and HTML entities."""
    return f"""<!doctype html>
<html><body>
  <div hidden>
    <form id="qrFromId">
      <input type="hidden" name="execution" value="qr-decoy">
      <input type="hidden" name="templateOnly" value="not-the-password-form">
    </form>
    <form id="loginTemplateCopy">
      <input type="hidden" name="execution" value="template-decoy">
    </form>
  </div>
  <form id="pwdFromId" action="https://attacker.invalid/collect" method="post">
    <input type="hidden" name="_eventId" value="submit">
    <input type="hidden" name="cllt" value="userNameLogin">
    <input type="hidden" name="dllt" value="generalLogin">
    <input type="hidden" name="lt" value="lt&amp;token&#x3D;one">
    <input type="hidden" name="execution" value="{execution}">
    <input type="hidden" name="flowMarker" value="actual&amp;flow&#x3D;selected">
    <input type="hidden" id="pwdEncryptSalt" value="{LOGIN_SALT}">
    <input type="text" name="username" value="">
    <input type="password" name="passwordText" value="">
    <input type="hidden" name="password" value="">
    <input type="text" name="captcha" value="">
    <input type="checkbox" name="rememberMe" value="true">
  </form>
  <script>var captchaSwitch = '{captcha_switch}';</script>
</body></html>"""


def success_response(*cookie_headers: str) -> httpx2.Response:
    headers = [("Location", "/personalInfo/index")]
    headers.extend(("Set-Cookie", cookie) for cookie in cookie_headers)
    return httpx2.Response(302, headers=headers)


def decrypt_site_value(value: str, salt: str) -> bytes:
    """Decrypt the ciphertext-only wire value using CBC's recoverable blocks."""
    ciphertext = base64.b64decode(value, validate=True)
    if not ciphertext or len(ciphertext) % 16:
        raise AssertionError("IDAS ciphertext is not AES block aligned")
    key = salt.strip().encode("utf-8")
    # The exact site format sends no IV. Any IV recovers blocks after the first;
    # the 64-byte random prefix occupies the first four AES blocks.
    decryptor = Cipher(algorithms.AES(key), modes.CBC(b"0" * 16)).decryptor()
    padded = decryptor.update(ciphertext) + decryptor.finalize()
    unpadder = padding.PKCS7(128).unpadder()
    return unpadder.update(padded) + unpadder.finalize()


def recovered_prefix_tail(plaintext: bytes) -> str:
    """Validate the 48 random prefix bytes not masked by the missing IV."""
    prefix_tail = plaintext[16:64].decode("ascii")
    if len(prefix_tail) != 48 or any(character not in PREFIX_ALPHABET for character in prefix_tail):
        raise AssertionError("IDAS plaintext did not contain the expected random prefix")
    return prefix_tail


class IDASFlow:
    """Small isolated IDAS server with explicit cookies and response scripts."""

    def __init__(
        self,
        *,
        pages: Sequence[str] | None = None,
        captcha_results: Sequence[object] = (False,),
        login_responses: Sequence[httpx2.Response] | None = None,
        registration_cookies: Sequence[str] | None = None,
        login_cookies: Sequence[str] = (),
        image: bytes = b"\x89PNG\r\nIDAS image challenge",
        slider_background: bytes = b"background-png",
        slider_piece: bytes = b"puzzle-png" + SLIDER_SALT.encode("ascii"),
        slider_error_code: object = 1,
    ) -> None:
        self.pages = tuple(pages or (login_page(),))
        self.captcha_results = tuple(captcha_results)
        if login_responses is None:
            login_responses = (success_response(
                "CASTGC=issued-castgc; Path=/authserver; HttpOnly",
            ),)
        self.login_responses = tuple(login_responses)
        self.registration_cookies = (
            None if registration_cookies is None else tuple(registration_cookies)
        )
        self.login_cookies = tuple(login_cookies)
        self.image = image
        self.slider_background = slider_background
        self.slider_piece = slider_piece
        self.slider_error_code = slider_error_code
        self.requests: list[httpx2.Request] = []
        self.login_posts: list[httpx2.Request] = []
        self.slider_verifications: list[httpx2.Request] = []
        self._page_index = 0
        self._captcha_index = 0
        self._login_index = 0

    def __call__(self, request: httpx2.Request) -> httpx2.Response:
        self.requests.append(request)
        if request.url.host != "idas.uestc.edu.cn" or request.url.scheme != "https":
            raise AssertionError("IDAS traffic escaped the fixed HTTPS origin")
        path = request.url.path
        if path == "/authserver/bfp/info":
            fingerprint = request.url.params.get("bfp", "")
            cookies = self.registration_cookies
            if cookies is None:
                cookies = (
                    f"MULTIFACTOR_BROWSER_FINGERPRINT={fingerprint}; Path=/; HttpOnly",
                )
            headers = [("Set-Cookie", cookie) for cookie in cookies]
            return httpx2.Response(200, headers=headers)
        if path == "/authserver/login" and request.method == "GET":
            page = self.pages[min(self._page_index, len(self.pages) - 1)]
            self._page_index += 1
            cookies = ["JSESSIONID=flow-session; Path=/authserver; HttpOnly"]
            cookies.extend(self.login_cookies)
            headers = [("Set-Cookie", cookie) for cookie in cookies]
            return httpx2.Response(200, text=page, headers=headers)
        if path == "/authserver/checkNeedCaptcha.htl":
            result = self.captcha_results[min(self._captcha_index, len(self.captcha_results) - 1)]
            self._captcha_index += 1
            return httpx2.Response(200, json={"isNeed": result})
        if path == "/authserver/getCaptcha.htl":
            return httpx2.Response(200, content=self.image, headers={"Content-Type": "image/png"})
        if path == "/authserver/common/openSliderCaptcha.htl":
            return httpx2.Response(200, json={
                "bigImage": base64.b64encode(self.slider_background).decode("ascii"),
                "smallImage": base64.b64encode(self.slider_piece).decode("ascii"),
            })
        if path == "/authserver/common/verifySliderCaptcha.htl":
            self.slider_verifications.append(request)
            return httpx2.Response(200, json={"errorCode": self.slider_error_code})
        if path == "/authserver/login" and request.method == "POST":
            self.login_posts.append(request)
            response = self.login_responses[min(self._login_index, len(self.login_responses) - 1)]
            self._login_index += 1
            return response
        if path == "/authserver/logout":
            return httpx2.Response(200, text="logged out")
        if path.startswith("/personalInfo/"):
            return httpx2.Response(200, text="<html>account</html>")
        raise AssertionError(f"unexpected request: {request.method} {request.url.path}")


def request_form(request: httpx2.Request) -> dict[str, list[str]]:
    return parse_qs(request.content.decode("ascii"), keep_blank_values=True)


def auth_client(flow: IDASFlow) -> IDASClient:
    return IDASClient(user_agent=USER_AGENT, transport=httpx2.MockTransport(flow))


class IDASAuthenticationTests(unittest.TestCase):
    def test_registration_requires_anonymous_server_cookie_and_masks_secrets(self) -> None:
        flow = IDASFlow()
        with auth_client(flow) as client:
            fingerprint = client.register_fingerprint(SecretStr(FINGERPRINT))
            self.assertEqual(fingerprint.get_secret_value(), FINGERPRINT)
            self.assertNotIn(FINGERPRINT, repr(fingerprint))
            self.assertNotIn(FINGERPRINT, str(fingerprint))
        request = flow.requests[0]
        self.assertEqual(request.url.path, "/authserver/bfp/info")
        self.assertEqual(request.url.params["bfp"], FINGERPRINT)
        self.assertFalse(request.headers.get("cookie", ""))
        self.assertEqual(request.headers["user-agent"], USER_AGENT)

        missing_cookie = IDASFlow(registration_cookies=())
        with auth_client(missing_cookie) as client:
            with self.assertRaises(AuthenticationError):
                client.register_fingerprint(FINGERPRINT)

    def test_registration_rejects_wrong_scope_ambiguous_and_mismatched_cookies(self) -> None:
        cases = (
            ("wrong scope", (f"MULTIFACTOR_BROWSER_FINGERPRINT={FINGERPRINT}; Path=/wrong",)),
            (
                "ambiguous",
                (
                    f"MULTIFACTOR_BROWSER_FINGERPRINT={FINGERPRINT}; Path=/",
                    f"MULTIFACTOR_BROWSER_FINGERPRINT={FINGERPRINT}; Path=/authserver",
                ),
            ),
            ("mismatched value", (f"MULTIFACTOR_BROWSER_FINGERPRINT={'F' * 32}; Path=/",)),
        )
        for label, cookies in cases:
            with self.subTest(case=label):
                flow = IDASFlow(registration_cookies=cookies)
                with auth_client(flow) as client:
                    with self.assertRaises(MoonError):
                        client.register_fingerprint(FINGERPRINT)

    def test_generated_device_id_is_stable_but_not_authentication(self) -> None:
        flow = IDASFlow(
            login_responses=(
                httpx2.Response(200, text=login_page("e1s2")),
                success_response("CASTGC=after-password; Path=/authserver; HttpOnly"),
            ),
        )
        with auth_client(flow) as client:
            client.prepare_login()
            with self.assertRaises(AuthenticationError):
                client.login("consumer-user", "first-password")
            with self.assertRaises(AuthenticationError):
                client.credentials()
            credentials = client.login("consumer-user", "second-password")
            self.assertEqual(credentials.castgc.get_secret_value(), "after-password")

        registration_requests = [r for r in flow.requests if r.url.path == "/authserver/bfp/info"]
        self.assertEqual(len(registration_requests), 1)
        generated = registration_requests[0].url.params["bfp"]
        self.assertRegex(generated, r"\A[0-9A-F]{32}\Z")
        self.assertEqual(credentials.browser_fingerprint.get_secret_value(), generated)
        self.assertEqual(request_form(flow.login_posts[0])["execution"], ["e1s1"])
        self.assertEqual(request_form(flow.login_posts[1])["execution"], ["e1s2"])

    def test_fingerprint_digest_uses_binary_canvas_and_canonical_component_order(self) -> None:
        components = FingerprintComponents(
            browser_name="",
            engine_name="",
            os_name="",
            cpu_architecture="",
            device_type="",
            device_model="",
            device_vendor="",
            platform="TestOS",
            language="en-US",
            hardware_concurrency=8,
            max_touch_points=5,
            device_memory=8.0,
            canvas_png=b"\x00\xff\x10",
        )
        digest = components.digest()
        self.assertEqual(digest.get_secret_value(), "38245D735A3DE6CB5E8D8E765128902B")
        self.assertNotIn(digest.get_secret_value(), repr(digest))

    def test_login_selects_password_form_decrypts_password_and_masks_cookies(self) -> None:
        flow = IDASFlow()
        with auth_client(flow) as client:
            client.register_fingerprint(FINGERPRINT)
            page = client.prepare_login()
            self.assertEqual(page.execution.get_secret_value(), "e1s1")
            self.assertEqual(page.encrypt_salt.get_secret_value(), LOGIN_SALT)
            self.assertEqual(page.captcha_kind, "slider")
            self.assertEqual(page.form_fields["flowMarker"], "actual&flow=selected")
            self.assertNotIn("e1s1", repr(page))
            self.assertNotIn(LOGIN_SALT, page.model_dump_json())

            credentials = client.login("consumer-user", SecretStr(PASSWORD), remember_me=True)
            self.assertEqual(credentials.castgc.get_secret_value(), "issued-castgc")
            self.assertEqual(credentials.browser_fingerprint.get_secret_value(), FINGERPRINT)
            self.assertEqual(credentials.user_agent, USER_AGENT)
            for secret in ("issued-castgc", FINGERPRINT):
                self.assertNotIn(secret, repr(credentials))
                self.assertNotIn(secret, credentials.model_dump_json())

        self.assertEqual(len(flow.login_posts), 1)
        posted = request_form(flow.login_posts[0])
        self.assertEqual(posted["execution"], ["e1s1"])
        self.assertEqual(posted["_eventId"], ["submit"])
        self.assertEqual(posted["cllt"], ["userNameLogin"])
        self.assertEqual(posted["dllt"], ["generalLogin"])
        self.assertEqual(posted["lt"], ["lt&token=one"])
        self.assertEqual(posted["username"], ["consumer-user"])
        self.assertEqual(posted["rememberMe"], ["true"])
        self.assertNotIn("passwordText", posted)
        self.assertNotIn(PASSWORD, flow.login_posts[0].content.decode("ascii"))
        plaintext = decrypt_site_value(posted["password"][0], LOGIN_SALT)
        recovered_prefix_tail(plaintext)
        self.assertEqual(plaintext[64:].decode("utf-8"), PASSWORD)
        for request in flow.requests:
            self.assertEqual(request.headers["user-agent"], USER_AGENT)
        self.assertTrue(any(r.url.path == "/personalInfo/index" for r in flow.requests))

    def test_stale_castgc_is_removed_before_failed_password_post(self) -> None:
        flow = IDASFlow(
            login_cookies=("CASTGC=stale-castgc; Path=/authserver; HttpOnly",),
            login_responses=(httpx2.Response(200, text=login_page("e1s2")),),
        )
        with auth_client(flow) as client:
            client.register_fingerprint(FINGERPRINT)
            client.prepare_login()
            with self.assertRaises(AuthenticationError) as caught:
                client.login("consumer-user", "failed-password")
            self.assertNotIn("stale-castgc", str(caught.exception))
            with self.assertRaises(AuthenticationError):
                client.credentials()

        self.assertEqual(len(flow.login_posts), 1)
        self.assertNotIn("CASTGC=stale-castgc", flow.login_posts[0].headers.get("cookie", ""))
        self.assertEqual(request_form(flow.login_posts[0])["execution"], ["e1s1"])

    def test_check_need_captcha_requires_a_json_boolean(self) -> None:
        invalid_values: tuple[object, ...] = ("false", 0, 1, None, {}, [])
        for invalid in invalid_values:
            with self.subTest(value=invalid):
                flow = IDASFlow(captcha_results=(invalid,))
                with auth_client(flow) as client:
                    client.prepare_login()
                    with self.assertRaises(ProtocolError):
                        client.needs_captcha("consumer-user")
        for expected in (False, True):
            with self.subTest(value=expected):
                flow = IDASFlow(captcha_results=(expected,))
                with auth_client(flow) as client:
                    client.prepare_login()
                    self.assertIs(client.needs_captcha("consumer-user"), expected)

    def test_image_captcha_is_returned_for_caller_and_caller_answer_is_submitted(self) -> None:
        image = b"caller-visible-image-captcha"
        flow = IDASFlow(
            pages=(login_page(captcha_switch="1"),),
            captcha_results=(True, True),
            image=image,
        )
        with auth_client(flow) as client:
            client.register_fingerprint(FINGERPRINT)
            client.prepare_login()
            with self.assertRaises(CaptchaRequiredError) as required:
                client.login("consumer-user", "fake-password")
            self.assertEqual(required.exception.kind, "image")
            self.assertEqual(flow.login_posts, [])

            self.assertEqual(client.get_image_captcha(), image)
            self.assertEqual(flow.login_posts, [])
            client.login("consumer-user", "fake-password", captcha="typed-by-caller")

        self.assertEqual(len(flow.login_posts), 1)
        values = request_form(flow.login_posts[0])
        self.assertIn(["typed-by-caller"], list(values.values()))
        self.assertFalse(any("SliderCaptcha" in request.url.path for request in flow.requests))
        self.assertFalse(flow.slider_verifications)

    def test_fresh_login_state_and_slider_verification_stay_in_same_session(self) -> None:
        flow = IDASFlow(
            captcha_results=(False, True, True),
            login_responses=(
                httpx2.Response(200, text=login_page("e1s2", captcha_switch="2")),
                success_response("CASTGC=retry-castgc; Path=/authserver; HttpOnly"),
            ),
        )
        tracks = [SliderTrack(a=12.5, b=-0.5, c=41), SliderTrack(a=24.0, b=1.25, c=88)]
        with auth_client(flow) as client:
            client.register_fingerprint(FINGERPRINT)
            client.prepare_login()
            with self.assertRaises(AuthenticationError):
                client.login("retry-user", "first-attempt-password")
            with self.assertRaises(CaptchaRequiredError) as required:
                client.login("retry-user", "retry-password")
            self.assertEqual(required.exception.kind, "slider")
            self.assertEqual(len(flow.login_posts), 1)

            slider = client.get_slider_captcha()
            self.assertEqual(slider.background_image, flow.slider_background)
            self.assertEqual(slider.puzzle_image, flow.slider_piece)
            self.assertEqual(slider.encrypt_salt.get_secret_value(), SLIDER_SALT)
            self.assertNotIn(SLIDER_SALT, repr(slider))
            self.assertIs(client.verify_slider_captcha(
                canvas_length=320.0,
                move_length=151.5,
                tracks=tracks,
            ), True)
            credentials = client.login("retry-user", "retry-password")
            self.assertEqual(credentials.castgc.get_secret_value(), "retry-castgc")

        self.assertEqual(sum(r.url.path == "/authserver/login" and r.method == "GET" for r in flow.requests), 1)
        self.assertEqual(len(flow.login_posts), 2)
        self.assertEqual(request_form(flow.login_posts[0])["execution"], ["e1s1"])
        self.assertEqual(request_form(flow.login_posts[1])["execution"], ["e1s2"])
        self.assertEqual(len(flow.slider_verifications), 1)
        verify_request = flow.slider_verifications[0]
        verify_form = request_form(verify_request)
        self.assertEqual(set(verify_form), {"sign"})
        self.assertNotIn("track", verify_request.content.decode("ascii").lower())
        expected_payload = {
            "canvasLength": 320.0,
            "moveLength": 151.5,
            "tracks": [track.model_dump() for track in tracks],
        }
        decrypted = decrypt_site_value(verify_form["sign"][0], SLIDER_SALT)
        recovered_prefix_tail(decrypted)
        self.assertEqual(
            decrypted[64:].decode("utf-8"),
            json.dumps(expected_payload, ensure_ascii=False, separators=(",", ":")),
        )
        self.assertEqual(verify_request.url.path, "/authserver/common/verifySliderCaptcha.htl")
        for request in flow.requests:
            cookie = request.headers.get("cookie", "")
            if request.url.path.startswith("/authserver/") and (
                request.url.path not in ("/authserver/bfp/info", "/authserver/login") or request.method != "GET"
            ):
                self.assertIn("JSESSIONID=flow-session", cookie)
            elif request.url.path.startswith("/personalInfo/"):
                self.assertNotIn("JSESSIONID=", cookie)
        self.assertEqual(sum(r.url.path == "/authserver/bfp/info" for r in flow.requests), 1)

    def test_non_success_slider_check_does_not_mark_captcha_verified_or_reuse_sign(self) -> None:
        flow = IDASFlow(captcha_results=(True, True), slider_error_code=0)
        with auth_client(flow) as client:
            client.register_fingerprint(FINGERPRINT)
            client.prepare_login()
            with self.assertRaises(CaptchaRequiredError):
                client.login("consumer-user", "fake-password")
            client.get_slider_captcha()
            tracks = [SliderTrack(a=125.0, b=0.0, c=50)]
            for _ in range(2):
                self.assertIs(client.verify_slider_captcha(
                    canvas_length=300.0,
                    move_length=125.0,
                    tracks=tracks,
                ), False)
            with self.assertRaises(CaptchaRequiredError):
                client.login("consumer-user", "fake-password")

        self.assertEqual(flow.login_posts, [])
        self.assertEqual(len(flow.slider_verifications), 2)
        prefix_tails = [
            recovered_prefix_tail(decrypt_site_value(request_form(request)["sign"][0], SLIDER_SALT))
            for request in flow.slider_verifications
        ]
        self.assertNotEqual(prefix_tails[0], prefix_tails[1])

    def test_untrusted_redirect_is_blocked_without_forwarding_password(self) -> None:
        flow = IDASFlow(login_responses=(httpx2.Response(
            307,
            headers={"Location": "https://attacker.invalid/collect"},
        ),))
        with auth_client(flow) as client:
            client.register_fingerprint(FINGERPRINT)
            client.prepare_login()
            with self.assertRaises(ProtocolError) as caught:
                client.login("consumer-user", "private-test-password")
        self.assertNotIn("private-test-password", str(caught.exception))
        self.assertEqual(len(flow.login_posts), 1)
        self.assertTrue(all(request.url.host == "idas.uestc.edu.cn" for request in flow.requests))

    def test_secondary_verification_is_not_returned_as_credentials(self) -> None:
        flow = IDASFlow(login_responses=(httpx2.Response(
            200,
            text="<html><form id='totpForm'><input name='otp'></form></html>",
        ),))
        with auth_client(flow) as client:
            client.register_fingerprint(FINGERPRINT)
            client.prepare_login()
            with self.assertRaises(ReauthenticationRequiredError):
                client.login("consumer-user", "fake-password")
            with self.assertRaises(AuthenticationError):
                client.credentials()
        self.assertEqual(len(flow.login_posts), 1)

    def test_missing_wrong_scope_and_ambiguous_castgc_are_rejected(self) -> None:
        cases = (
            ("missing", ()),
            ("wrong scope", ("CASTGC=wrong-scope; Path=/wrong; HttpOnly",)),
            (
                "ambiguous",
                (
                    "CASTGC=first-cookie; Path=/; HttpOnly",
                    "CASTGC=second-cookie; Path=/authserver; HttpOnly",
                ),
            ),
        )
        for label, cookie_headers in cases:
            with self.subTest(case=label):
                flow = IDASFlow(login_responses=(success_response(*cookie_headers),))
                with auth_client(flow) as client:
                    client.register_fingerprint(FINGERPRINT)
                    client.prepare_login()
                    with self.assertRaises(MoonError):
                        client.login("consumer-user", "fake-password")

    def test_logout_clears_authentication_cookies_and_cached_state(self) -> None:
        flow = IDASFlow()
        with auth_client(flow) as client:
            client.register_fingerprint(FINGERPRINT)
            client.prepare_login()
            client.login("consumer-user", "fake-password")
            client.logout()
            with self.assertRaises(AuthenticationError):
                client.credentials()
            client.prepare_login()
        logout_request = next(request for request in flow.requests if request.url.path == "/authserver/logout")
        self.assertIn("CASTGC=issued-castgc", logout_request.headers.get("cookie", ""))
        self.assertIn(f"MULTIFACTOR_BROWSER_FINGERPRINT={FINGERPRINT}", logout_request.headers.get("cookie", ""))
        post_logout_login = [
            request for request in flow.requests
            if request.url.path == "/authserver/login" and request.method == "GET"
        ][-1]
        self.assertNotIn("CASTGC=", post_logout_login.headers.get("cookie", ""))
        self.assertNotIn("MULTIFACTOR_BROWSER_FINGERPRINT=", post_logout_login.headers.get("cookie", ""))


if __name__ == "__main__":
    unittest.main()
