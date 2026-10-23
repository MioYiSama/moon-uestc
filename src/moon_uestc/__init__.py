"""Typed SDK for the student-facing 晚安成电 APIs."""

from .client import DEFAULT_BASE_URL, MoonClient
from .idas import IDASClient
from .idas_models import FingerprintComponents, IDASLoginPage, SliderCaptcha, SliderTrack
from .interactive import login_interactive
from .errors import (
    APIError,
    AuthenticationError,
    AuthorizationError,
    CaptchaRequiredError,
    CheckinTicketError,
    MoonError,
    ProtocolError,
    ReauthenticationRequiredError,
)

__all__ = [
    "APIError",
    "AuthenticationError",
    "AuthorizationError",
    "CaptchaRequiredError",
    "CheckinTicketError",
    "DEFAULT_BASE_URL",
    "FingerprintComponents",
    "IDASClient",
    "IDASLoginPage",
    "MoonClient",
    "MoonError",
    "ProtocolError",
    "ReauthenticationRequiredError",
    "SliderCaptcha",
    "SliderTrack",
    "login_interactive",
]
