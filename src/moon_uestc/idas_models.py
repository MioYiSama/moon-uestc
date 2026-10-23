"""IDAS login state and caller-supplied browser/CAPTCHA observations."""

from hashlib import md5
from typing import ClassVar, Literal

from pydantic import BaseModel, ConfigDict, Field, SecretStr


class _IDASModel(BaseModel):
    model_config: ClassVar[ConfigDict] = ConfigDict(
        strict=True, extra="forbid", frozen=True, hide_input_in_errors=True
    )


class IDASLoginPage(_IDASModel):
    """Current Web Flow state. Valid only with the client that fetched it."""

    execution: SecretStr
    encrypt_salt: SecretStr
    captcha_kind: Literal["image", "slider"]
    form_fields: dict[str, str] = Field(repr=False, exclude=True)


class SliderCaptcha(_IDASModel):
    """Images for a caller-operated challenge; the piece carries the signing key."""

    background_image: bytes = Field(repr=False, exclude=True)
    puzzle_image: bytes = Field(repr=False, exclude=True)

    @property
    def encrypt_salt(self) -> SecretStr:
        if len(self.puzzle_image) < 16:
            raise ValueError("IDAS slider image is missing its encryption key")
        try:
            key = self.puzzle_image[-16:].decode("ascii")
        except UnicodeDecodeError:
            raise ValueError("IDAS slider image has an invalid encryption key") from None
        return SecretStr(key)


class SliderTrack(_IDASModel):
    """A human movement sample: x/y displacement, milliseconds since the prior sample."""

    a: float = Field(allow_inf_nan=False)
    b: float = Field(allow_inf_nan=False)
    c: int = Field(ge=0)


class FingerprintComponents(_IDASModel):
    """Inputs used by common-header.js; collect them from the actual browser.

    ``canvas_png`` is the decoded PNG from the site's ``createDataUrl()``.
    Defaults mirror the JavaScript's missing-value fallbacks, not a synthetic UA.
    """

    browser_name: str = "-"
    engine_name: str = "-"
    os_name: str = "-"
    cpu_architecture: str = "-"
    device_type: str = "-"
    device_model: str = "-"
    device_vendor: str = "-"
    platform: str
    language: str
    hardware_concurrency: int = Field(default=0, ge=0)
    max_touch_points: int = Field(default=0, ge=0)
    device_memory: float = Field(default=0.0, ge=0, allow_inf_nan=False)
    canvas_png: bytes = Field(repr=False, exclude=True)

    def digest(self) -> SecretStr:
        """Reproduce the site's uppercase MD5-of-components fingerprint."""
        canvas_hash = md5(self.canvas_png, usedforsecurity=False).hexdigest().upper() if self.canvas_png else "N/A"
        memory = str(int(self.device_memory)) if self.device_memory.is_integer() else str(self.device_memory)
        items = [
            self.browser_name or "-", self.engine_name or "-", self.os_name or "-",
            self.cpu_architecture or "-", self.device_type or "-", self.device_model or "-",
            self.device_vendor or "-", self.platform, self.language,
            str(self.hardware_concurrency), str(self.max_touch_points), memory, canvas_hash,
        ]
        return SecretStr(md5("|".join(items).encode("utf-8"), usedforsecurity=False).hexdigest().upper())
