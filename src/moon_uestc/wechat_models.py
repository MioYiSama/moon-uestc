from __future__ import annotations

from typing import ClassVar, Literal

from pydantic import BaseModel, ConfigDict, Field, SecretStr


class WeChatError(RuntimeError):
    """WeChat authorization or protocol failure, independent of the CAS session."""


class WeChatAuthorizationError(WeChatError):
    """WeChat/YYB rejected authorization; obtain fresh credentials by scanning."""


class WeChatProtocolError(WeChatError):
    """The peer response did not satisfy the WeChat protocol."""


class WeChatCredentials(BaseModel):
    """Sensitive YYB credentials; persist only in a private local file."""

    model_config: ClassVar[ConfigDict] = ConfigDict(hide_input_in_errors=True, extra="forbid")

    openid: SecretStr
    access_token: SecretStr
    refresh_token: SecretStr
    expires_at: int


class WeChatQRCode(BaseModel):
    uuid: str
    image: bytes = Field(repr=False)


class WeChatPollResult(BaseModel):
    status: Literal["pending", "scanned", "authorized", "cancelled", "expired"]
    code: SecretStr | None = None
