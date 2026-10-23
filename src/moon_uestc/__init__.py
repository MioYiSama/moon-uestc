"""Typed SDK for the student-facing 晚安成电 APIs."""

from .client import DEFAULT_BASE_URL, MoonClient
from .errors import (
    APIError,
    AuthenticationError,
    AuthorizationError,
    CheckinTicketError,
    MoonError,
    ProtocolError,
    ReauthenticationRequiredError,
)

__all__ = [
    "APIError",
    "AuthenticationError",
    "AuthorizationError",
    "CheckinTicketError",
    "DEFAULT_BASE_URL",
    "MoonClient",
    "MoonError",
    "ProtocolError",
    "ReauthenticationRequiredError",
]
