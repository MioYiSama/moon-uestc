"""Explicit IDAS web authentication with caller-operated CAPTCHA challenges."""

from __future__ import annotations

import base64
import binascii
import json
import math
import re
import time
from collections.abc import Sequence
from html.parser import HTMLParser
from typing import Literal, cast
from urllib.parse import urlsplit

from http.cookiejar import Cookie

from secrets import token_hex

import httpx2
from pydantic import SecretStr

from ._idas_crypto import encrypt_password
from .errors import AuthenticationError, CaptchaRequiredError, ProtocolError, ReauthenticationRequiredError
from .idas_models import IDASLoginPage, SliderCaptcha, SliderTrack
from .models import CASCredentials

_IDAS_ORIGIN = "https://idas.uestc.edu.cn"
_IDAS_HOST = "idas.uestc.edu.cn"
_REDIRECT_CODES = frozenset({301, 302, 303, 307, 308})
_MAX_REDIRECTS = 8
_FINGERPRINT_RE = re.compile(r"[0-9A-F]{32}\Z")
_CAPTCHA_SWITCH_RE = re.compile(r"\bcaptchaSwitch\s*=\s*(['\"])([12])\1")


class _Form:
    def __init__(self, attributes: dict[str, str | None]) -> None:
        self.attributes = attributes
        self.inputs: list[dict[str, str | None]] = []


class _LoginHTMLParser(HTMLParser):
    """Collect forms, inputs and inline script text without executing page code."""

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.forms: list[_Form] = []
        self.inputs: list[dict[str, str | None]] = []
        self._current_form: _Form | None = None
        self._inside_script = False
        self.scripts: list[str] = []

    @staticmethod
    def _attributes(attributes: list[tuple[str, str | None]]) -> dict[str, str | None]:
        return {name.lower(): value for name, value in attributes}

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attributes = self._attributes(attrs)
        if tag.lower() == "form":
            form = _Form(attributes)
            self.forms.append(form)
            self._current_form = form
        elif tag.lower() == "input":
            self.inputs.append(attributes)
            if self._current_form is not None:
                self._current_form.inputs.append(attributes)
        elif tag.lower() == "script":
            self._inside_script = True

    def handle_startendtag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        self.handle_starttag(tag, attrs)
        self.handle_endtag(tag)

    def handle_endtag(self, tag: str) -> None:
        if tag.lower() == "form":
            self._current_form = None
        elif tag.lower() == "script":
            self._inside_script = False

    def handle_data(self, data: str) -> None:
        if self._inside_script:
            self.scripts.append(data)


def _secret(value: str | SecretStr) -> str:
    return value.get_secret_value() if isinstance(value, SecretStr) else value


def _response_json(response: httpx2.Response, description: str) -> dict[str, object]:
    try:
        payload = json.loads(response.content)
    except (ValueError, TypeError):
        raise ProtocolError(f"IDAS {description} returned invalid JSON") from None
    if not isinstance(payload, dict):
        raise ProtocolError(f"IDAS {description} returned an invalid JSON contract")
    return cast(dict[str, object], payload)


def _parse_html(content: str) -> _LoginHTMLParser:
    parser = _LoginHTMLParser()
    try:
        parser.feed(content)
        parser.close()
    except Exception:
        # HTMLParser is intentionally forgiving, but never expose source snippets.
        raise ProtocolError("IDAS returned malformed login HTML") from None
    return parser


def _same_idas_origin(url: httpx2.URL) -> bool:
    try:
        parts = urlsplit(str(url))
        port = parts.port
    except (ValueError, httpx2.InvalidURL):
        return False
    return (
        parts.scheme.lower() == "https"
        and parts.hostname is not None
        and parts.hostname.lower() == _IDAS_HOST
        and port in (None, 443)
        and parts.username is None
        and parts.password is None
    )


def _is_mfa_path(path: str) -> bool:
    segments = tuple(segment.lower() for segment in path.split("/") if segment)
    return any(
        segment == "other"
        or segment in {"mfa", "otp", "totp", "sms", "fido"}
        or segment.startswith(("mfa-", "otp-", "totp-", "sms-", "fido-"))
        for segment in segments
    )


class IDASClient:
    """A stateful client for the observed IDAS password-login flow.

    CAPTCHA work remains with the caller: image answers are supplied to ``login``
    and slider tracks must be samples from an actual browser interaction.
    """

    def __init__(
        self,
        user_agent: str,
        *,
        timeout: float = 30,
        transport: httpx2.BaseTransport | None = None,
    ) -> None:
        CASCredentials.validate_user_agent(user_agent)
        self._client = httpx2.Client(
            base_url=_IDAS_ORIGIN,
            headers={"User-Agent": user_agent},
            timeout=timeout,
            follow_redirects=False,
            transport=transport,
        )
        self._user_agent = user_agent
        self._page: IDASLoginPage | None = None
        self._slider_salt: SecretStr | None = None
        self._slider_verified = False

    def __enter__(self) -> IDASClient:
        return self

    def __exit__(self, exc_type: object, exc_value: object, traceback: object) -> None:
        self.close()

    def close(self) -> None:
        self._client.close()
        self._discard_page_state()

    def _discard_page_state(self) -> None:
        self._page = None
        self._slider_salt = None
        self._slider_verified = False

    def _request(
        self,
        method: str,
        url: str | httpx2.URL,
        *,
        params: dict[str, str | int] | None = None,
        data: dict[str, str] | None = None,
    ) -> httpx2.Response:
        try:
            return self._client.request(method, url, params=params, data=data)
        except httpx2.RequestError:
            # Requests can contain credentials or one-time tickets in URLs/bodies.
            raise AuthenticationError("IDAS request failed due to a network error") from None

    @staticmethod
    def _check_status(response: httpx2.Response, description: str) -> None:
        if not response.is_success:
            raise AuthenticationError(
                f"IDAS {description} received an unsuccessful HTTP response",
                status_code=response.status_code,
            )

    def _redirects(
        self,
        response: httpx2.Response,
        *,
        method: str,
        data: dict[str, str] | None = None,
    ) -> httpx2.Response:
        followed = 0
        while response.status_code in _REDIRECT_CODES:
            if followed >= _MAX_REDIRECTS:
                raise ProtocolError("IDAS authentication redirect limit exceeded")
            location = response.headers.get("location")
            if not location:
                raise ProtocolError("IDAS authentication redirect has no Location header")
            try:
                destination = response.url.join(location)
            except (ValueError, httpx2.InvalidURL):
                raise ProtocolError("IDAS authentication returned an invalid redirect") from None
            if not _same_idas_origin(destination):
                raise ProtocolError("IDAS authentication redirected outside the secure IDAS origin")
            if _is_mfa_path(urlsplit(str(destination)).path):
                raise ReauthenticationRequiredError("Complete secondary authentication on the official IDAS page")
            if method == "POST" and response.status_code in {301, 302, 303}:
                method = "GET"
                data = None
            response = self._request(method, destination, data=data)
            followed += 1
        return response

    def _all_cookies(self, name: str) -> list[Cookie]:
        return [cookie for cookie in self._client.cookies.jar if cookie.name == name]


    def _clear_cookie_name(self, name: str) -> None:
        jar = self._client.cookies.jar
        for cookie in self._all_cookies(name):
            try:
                jar.clear(domain=cookie.domain, path=cookie.path, name=cookie.name)
            except KeyError:
                # A response can rotate cookies while the previous snapshot is read.
                continue

    def _scoped_cookie(self, name: str, path: str) -> str:
        matches = self._all_cookies(name)
        if not matches:
            raise AuthenticationError("IDAS did not retain a required authentication cookie")
        if len(matches) != 1:
            raise ProtocolError("IDAS retained ambiguous authentication cookies")
        cookie = matches[0]
        if cookie.domain.lower() != _IDAS_HOST or cookie.domain_specified or cookie.path != path:
            raise ProtocolError("IDAS retained an authentication cookie with an unexpected scope")

        value = cookie.value
        if not value:
            raise AuthenticationError("IDAS did not retain a required authentication cookie")
        return value

    def _parse_login_page(self, content: str) -> IDASLoginPage:
        parser = _parse_html(content)
        forms = [form for form in parser.forms if form.attributes.get("id") == "pwdFromId"]
        if not forms:
            raise ProtocolError("IDAS login page did not contain the password login form")

        candidates: list[tuple[_Form, dict[str, str]]] = []
        for form in forms:
            fields: dict[str, str] = {}
            conflict = False
            for attributes in form.inputs:
                name = attributes.get("name")
                input_type = (attributes.get("type") or "").lower()
                if (
                    not name
                    or input_type != "hidden"
                    or "disabled" in attributes
                    or name == "passwordText"
                ):
                    continue
                value = attributes.get("value") or ""
                previous = fields.get(name)
                if previous is not None and previous != value:
                    conflict = True
                    break
                fields[name] = value
            if not conflict and fields.get("execution"):
                candidates.append((form, fields))
        if not candidates:
            raise ProtocolError("IDAS password form did not contain an execution value")

        # Duplicate pwdFromId markup is sometimes emitted for a hidden template.
        # Accept it only when its submitted hidden state is equivalent.
        fields = candidates[0][1]
        if any(candidate_fields != fields for _, candidate_fields in candidates[1:]):
            raise ProtocolError("IDAS login page contained conflicting password forms")

        salt_values = {
            attributes.get("value") or ""
            for form, _ in candidates
            for attributes in form.inputs
            if attributes.get("id") == "pwdEncryptSalt"
        }
        salt_values.discard("")
        if len(salt_values) != 1:
            raise ProtocolError("IDAS login page did not contain a unique password encryption salt")

        switches = {match.group(2) for script in parser.scripts for match in _CAPTCHA_SWITCH_RE.finditer(script)}
        if len(switches) != 1:
            raise ProtocolError("IDAS login page did not declare a unique CAPTCHA type")

        execution = fields["execution"]
        fields.setdefault("_eventId", "submit")
        fields.setdefault("cllt", "userNameLogin")
        fields.setdefault("dllt", "generalLogin")
        fields.setdefault("lt", "")
        fields.pop("passwordText", None)
        captcha_kind: Literal["image", "slider"] = "slider" if switches.pop() == "2" else "image"
        return IDASLoginPage(
            execution=SecretStr(execution),
            encrypt_salt=SecretStr(next(iter(salt_values))),
            captcha_kind=captcha_kind,
            form_fields=fields,
        )

    def _login_form_present(self, parser: _LoginHTMLParser) -> bool:
        for form in parser.forms:
            if form.attributes.get("id") == "pwdFromId":
                return True
            names = {(attributes.get("name") or "").lower() for attributes in form.inputs}
            if "username" in names and ("password" in names or "passwordtext" in names):
                return True
        return False

    def _secondary_form_present(self, parser: _LoginHTMLParser) -> bool:
        for form in parser.forms:
            form_id = (form.attributes.get("id") or "").lower()
            action = (form.attributes.get("action") or "").lower()
            names = {(attributes.get("name") or "").lower() for attributes in form.inputs}
            if "other" in form_id or "other" in action:
                return True
            if names.intersection({"otp", "totp", "smscode", "fido", "verificationcode"}):
                return True
        return False

    def prepare_login(self) -> IDASLoginPage:
        """Fetch and retain the live login page and its one-use form values."""
        self._discard_page_state()
        self._clear_cookie_name("CASTGC")
        response = self._request("GET", _IDAS_ORIGIN + "/authserver/login")
        self._check_status(response, "login page")
        page = self._parse_login_page(response.text)
        self._page = page
        return page

    def needs_captcha(self, username: str) -> bool:
        """Ask IDAS whether this username currently requires a CAPTCHA."""
        response = self._request(
            "GET",
            _IDAS_ORIGIN + "/authserver/checkNeedCaptcha.htl",
            params={"username": username},
        )
        self._check_status(response, "CAPTCHA check")
        payload = _response_json(response, "CAPTCHA check")
        required = payload.get("isNeed")
        if type(required) is not bool:
            raise ProtocolError("IDAS CAPTCHA check returned an invalid boolean value")
        return required

    def register_fingerprint(self, fingerprint: str | SecretStr | None = None) -> SecretStr:
        """Register a supplied fingerprint or reuse/create an SDK device identifier.

        A generated identifier is not a browser fingerprint derived from browser
        components; callers may provide ``FingerprintComponents.digest()`` when
        they have the actual browser observations.
        """
        if fingerprint is None:
            try:
                return SecretStr(self._scoped_cookie("MULTIFACTOR_BROWSER_FINGERPRINT", "/"))
            except AuthenticationError:
                value = token_hex(16).upper()
        else:
            value = _secret(fingerprint)
        if _FINGERPRINT_RE.fullmatch(value) is None:
            raise ValueError("fingerprint must be 32 uppercase hexadecimal characters")
        self._clear_cookie_name("MULTIFACTOR_BROWSER_FINGERPRINT")
        response = self._request(
            "GET",
            _IDAS_ORIGIN + "/authserver/bfp/info",
            params={"bfp": value},
        )
        self._check_status(response, "fingerprint registration")
        retained = self._scoped_cookie("MULTIFACTOR_BROWSER_FINGERPRINT", "/")
        if retained != value:
            raise AuthenticationError("IDAS did not retain the requested browser fingerprint")
        return SecretStr(retained)


    def get_image_captcha(self) -> bytes:
        """Fetch the current image challenge for caller-operated solving."""
        page = self._page
        if page is None:
            raise ProtocolError("Prepare the IDAS login page before requesting a CAPTCHA")
        if page.captcha_kind != "image":
            raise ProtocolError("The current IDAS page uses a slider CAPTCHA")
        response = self._request(
            "GET",
            _IDAS_ORIGIN + "/authserver/getCaptcha.htl",
            params={"timestamp": time.time_ns() // 1_000_000},
        )
        self._check_status(response, "image CAPTCHA")
        if not response.content:
            raise ProtocolError("IDAS returned an empty image CAPTCHA")
        return response.content

    def get_slider_captcha(self) -> SliderCaptcha:
        """Fetch the current slider images and retain their signing key."""
        page = self._page
        if page is None:
            raise ProtocolError("Prepare the IDAS login page before requesting a CAPTCHA")
        if page.captcha_kind != "slider":
            raise ProtocolError("The current IDAS page uses an image CAPTCHA")
        self._slider_salt = None
        self._slider_verified = False
        response = self._request("GET", _IDAS_ORIGIN + "/authserver/common/openSliderCaptcha.htl")
        self._check_status(response, "slider CAPTCHA")
        payload = _response_json(response, "slider CAPTCHA")
        big_image = payload.get("bigImage")
        small_image = payload.get("smallImage")
        if not isinstance(big_image, str) or not isinstance(small_image, str):
            raise ProtocolError("IDAS slider CAPTCHA did not contain image data")
        try:
            background = base64.b64decode(big_image, validate=True)
            puzzle = base64.b64decode(small_image, validate=True)
        except (binascii.Error, ValueError):
            raise ProtocolError("IDAS slider CAPTCHA contained invalid image data") from None
        if not background or len(puzzle) <= 16:
            raise ProtocolError("IDAS slider CAPTCHA images were incomplete")
        captcha = SliderCaptcha(background_image=background, puzzle_image=puzzle)
        try:
            salt = captcha.encrypt_salt
        except ValueError:
            raise ProtocolError("IDAS slider CAPTCHA contained an invalid signing key") from None
        self._slider_salt = salt
        self._slider_verified = False
        return captcha

    def verify_slider_captcha(
        self,
        *,
        canvas_length: float,
        move_length: float,
        tracks: Sequence[SliderTrack],
    ) -> bool:
        """Submit caller-captured pointer samples; never generate or repair tracks."""
        page = self._page
        salt = self._slider_salt
        if page is None or page.captcha_kind != "slider" or salt is None:
            raise ProtocolError("Fetch the current IDAS slider CAPTCHA before verifying it")
        if (
            isinstance(canvas_length, bool)
            or isinstance(move_length, bool)
            or not math.isfinite(canvas_length)
            or not math.isfinite(move_length)
        ):
            raise ValueError("slider measurements must be finite numbers")
        payload = {
            "canvasLength": canvas_length,
            "moveLength": move_length,
            "tracks": [{"a": track.a, "b": track.b, "c": track.c} for track in tracks],
        }
        try:
            serialized = json.dumps(payload, ensure_ascii=False, separators=(",", ":"), allow_nan=False)
            sign = encrypt_password(serialized, salt)
        except (TypeError, ValueError):
            raise ProtocolError("IDAS slider CAPTCHA payload could not be encrypted") from None
        self._slider_verified = False
        response = self._request(
            "POST",
            _IDAS_ORIGIN + "/authserver/common/verifySliderCaptcha.htl",
            data={"sign": sign},
        )
        self._check_status(response, "slider verification")
        result = _response_json(response, "slider verification").get("errorCode")
        if type(result) is not int:
            raise ProtocolError("IDAS slider verification returned an invalid result")
        verified = result == 1
        self._slider_verified = verified
        return verified

    def login(
        self,
        username: str,
        password: str | SecretStr,
        *,
        browser_fingerprint: str | SecretStr | None = None,
        remember_me: bool = False,
        captcha: str | None = None,
    ) -> CASCredentials:
        """Submit password credentials and return only server-issued CAS cookies."""
        if browser_fingerprint is not None:
            supplied_fingerprint = _secret(browser_fingerprint)
            if _FINGERPRINT_RE.fullmatch(supplied_fingerprint) is None:
                raise ValueError("fingerprint must be 32 uppercase hexadecimal characters")
            try:
                registered_fingerprint = self._scoped_cookie("MULTIFACTOR_BROWSER_FINGERPRINT", "/")
            except AuthenticationError:
                registered_fingerprint = ""
            if registered_fingerprint != supplied_fingerprint:
                self.register_fingerprint(browser_fingerprint)
        else:
            try:
                # Reuse the server-issued value; initialize an SDK device ID only if absent.
                self._scoped_cookie("MULTIFACTOR_BROWSER_FINGERPRINT", "/")
            except AuthenticationError:
                self.register_fingerprint()

        page = self._page if self._page is not None else self.prepare_login()
        captcha_required = self.needs_captcha(username)
        if captcha_required:
            if page.captcha_kind == "image":
                if captcha is None or captcha == "":
                    raise CaptchaRequiredError("image")
            elif not self._slider_verified:
                raise CaptchaRequiredError("slider")

        try:
            encrypted_password = encrypt_password(password, page.encrypt_salt)
        except (TypeError, ValueError):
            raise ProtocolError("IDAS login page supplied an invalid password encryption salt") from None

        fields = dict(page.form_fields)
        fields["username"] = username
        fields["password"] = encrypted_password
        fields.pop("passwordText", None)
        fields.pop("rememberMe", None)
        if remember_me:
            fields["rememberMe"] = "true"
        if captcha_required and page.captcha_kind == "image":
            fields["captcha"] = captcha or ""

        # A prior successful CAS ticket must never make this password attempt look successful.
        self._clear_cookie_name("CASTGC")
        self._slider_salt = None
        self._slider_verified = False
        try:
            response = self._request("POST", _IDAS_ORIGIN + "/authserver/login", data=fields)
            response = self._redirects(response, method="POST", data=fields)
        except (AuthenticationError, ProtocolError):
            self._clear_cookie_name("CASTGC")
            self._discard_page_state()
            raise

        final_path = urlsplit(str(response.url)).path
        if _is_mfa_path(final_path):
            self._clear_cookie_name("CASTGC")
            raise ReauthenticationRequiredError("Complete secondary authentication on the official IDAS page")

        parser: _LoginHTMLParser | None = None
        if "html" in response.headers.get("content-type", "").lower() or b"<form" in response.content.lower():
            try:
                parser = _parse_html(response.text)
            except ProtocolError:
                self._clear_cookie_name("CASTGC")
                raise
            if self._login_form_present(parser):
                try:
                    self._page = self._parse_login_page(response.text)
                except ProtocolError:
                    self._page = None
                self._slider_salt = None
                self._slider_verified = False
                self._clear_cookie_name("CASTGC")
                raise AuthenticationError("IDAS rejected the supplied username, password or CAPTCHA")
            if self._secondary_form_present(parser):
                self._clear_cookie_name("CASTGC")
                raise ReauthenticationRequiredError("Complete secondary authentication on the official IDAS page")

        try:
            self._check_status(response, "login")
        except AuthenticationError:
            self._clear_cookie_name("CASTGC")
            raise
        if final_path.startswith("/authserver/") and parser is not None and parser.forms:
            self._clear_cookie_name("CASTGC")
            raise ReauthenticationRequiredError("Complete secondary authentication on the official IDAS page")

        try:
            credentials = CASCredentials(
                castgc=SecretStr(self._scoped_cookie("CASTGC", "/authserver")),
                browser_fingerprint=SecretStr(self._scoped_cookie("MULTIFACTOR_BROWSER_FINGERPRINT", "/")),
                user_agent=self._user_agent,
            )
        except (AuthenticationError, ProtocolError):
            self._clear_cookie_name("CASTGC")
            raise
        self._discard_page_state()
        return credentials


    def credentials(self) -> CASCredentials:
        """Return the currently retained, correctly scoped CAS credentials."""
        return CASCredentials(
            castgc=SecretStr(self._scoped_cookie("CASTGC", "/authserver")),
            browser_fingerprint=SecretStr(self._scoped_cookie("MULTIFACTOR_BROWSER_FINGERPRINT", "/")),
            user_agent=self._user_agent,
        )

    def logout(self) -> None:
        """Explicitly log out of IDAS and clear all local IDAS state on success."""
        response = self._request("GET", _IDAS_ORIGIN + "/authserver/logout")
        response = self._redirects(response, method="GET")
        self._check_status(response, "logout")
        self._client.cookies.clear()
        self._discard_page_state()
