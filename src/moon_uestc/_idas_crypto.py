"""CryptoJS-compatible IDAS password and slider-sign encryption."""

from base64 import b64encode
from secrets import choice

from cryptography.hazmat.primitives import padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from pydantic import SecretStr

_ALPHABET = "ABCDEFGHJKMNPQRSTWXYZabcdefhijkmnprstwxyz2345678"


def encrypt_password(password: str | SecretStr, salt: str | SecretStr) -> str:
    """Encrypt with the page's UTF-8 key; do not fall back to cleartext."""
    plain = password.get_secret_value() if isinstance(password, SecretStr) else password
    key = salt.get_secret_value() if isinstance(salt, SecretStr) else salt
    key_bytes = key.strip().encode("utf-8")
    if len(key_bytes) not in (16, 24, 32):
        raise ValueError("IDAS encryption requires a 16, 24 or 32-byte key")
    prefix = "".join(choice(_ALPHABET) for _ in range(64))
    iv = "".join(choice(_ALPHABET) for _ in range(16)).encode("ascii")
    padder = padding.PKCS7(128).padder()
    padded = padder.update((prefix + plain).encode("utf-8")) + padder.finalize()
    encryptor = Cipher(algorithms.AES(key_bytes), modes.CBC(iv)).encryptor()
    return b64encode(encryptor.update(padded) + encryptor.finalize()).decode("ascii")
