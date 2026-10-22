"""HTTP, business, authentication and contract failures are separate."""


class MoonError(Exception):
    """Base SDK error. Network failures retain httpx2's own exception types."""


class APIError(MoonError):
    def __init__(self, message: str, *, code: int | None = None, status_code: int | None = None) -> None:
        self.code = code
        self.status_code = status_code
        super().__init__(message)


class AuthenticationError(APIError):
    """The server rejected the login session."""


class AuthorizationError(APIError):
    """The logged-in identity lacks permission for the operation."""


class CheckinTicketError(AuthorizationError):
    """Missing, expired or rejected mini-program operation authorization."""


class ProtocolError(MoonError):
    """Unexpected JSON envelope, response data, redirect or media type."""
