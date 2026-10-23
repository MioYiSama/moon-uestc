"""HTTP, business, authentication and contract failures are separate."""

from typing import Literal


class MoonError(Exception):
    """Base SDK error; CAS exchange sanitizes network failures as auth errors."""


class APIError(MoonError):
    def __init__(self, message: str, *, code: int | None = None, status_code: int | None = None) -> None:
        self.code = code
        self.status_code = status_code
        super().__init__(message)


class AuthenticationError(APIError):
    """The server rejected the login session."""


class ReauthenticationRequiredError(AuthenticationError):
    """The official CAS page requires login or secondary authentication."""


class CaptchaRequiredError(AuthenticationError):
    """The same IDAS client must complete a CAPTCHA before retrying login."""

    def __init__(self, kind: Literal["image", "slider"]) -> None:
        self.kind: Literal["image", "slider"] = kind
        super().__init__(f"IDAS login requires a caller-completed {kind} CAPTCHA")


class AuthorizationError(APIError):
    """The logged-in identity lacks permission for the operation."""


class CheckinTicketError(AuthorizationError):
    """Missing, expired or rejected mini-program operation authorization."""


class ProtocolError(MoonError):
    """Unexpected JSON envelope, response data, redirect or media type."""
