from __future__ import annotations

import hashlib
import struct
import unittest

from moon_uestc._wechat_crypto import (
    aes_gcm_encrypt_layout,
    derive_psk_one_way_keys,
    encrypt_record_payload,
    generate_ecdh,
)
from moon_uestc._wechat_mmtls import parse_0rtt_response
from moon_uestc._wechat_wire import (
    build_handshake,
    build_psk_client_hello,
    build_record,
    build_shortlink,
    lz4_all_literal,
    pb_len,
    pb_var,
)
from moon_uestc.wechat_models import WeChatAuthorizationError, WeChatProtocolError


_PSK = bytes.fromhex("a5" * 32)
_RECV_KEY = bytes.fromhex("5a" * 16)
_TIMESTAMP = 1_760_000_000
_CLIENT_HELLO = build_psk_client_hello(
    bytes(range(32)), _TIMESTAMP, b"fixture-ticket-entry"
)
_TYPE8 = bytes.fromhex("00000010080000000b01000000060012") + struct.pack(">I", _TIMESTAMP)


def _server_hello() -> bytes:
    _, public_key = generate_ecdh()
    key_share = b"\x00\x00" + struct.pack(">IH", 1, len(public_key)) + public_key
    extension = b"\x01" + struct.pack(">I", len(key_share)) + key_share
    body = (
        b"\x03\xf1"
        + struct.pack(">H", 0xC02B)
        + bytes(range(32))
        + struct.pack(">I", len(extension))
        + extension
    )
    return build_handshake(0x02, body)


def _response_records(
    code: bytes,
    *,
    base_error: int = 0,
    transcript_variant: int = 0,
    sequence: int = 2,
) -> bytes:
    server_hello = _server_hello()
    base_response = pb_var(1, base_error)
    response = pb_len(1, base_response) + pb_len(2, pb_len(3, code))
    encrypted_response = aes_gcm_encrypt_layout(
        _RECV_KEY,
        b"",
        lz4_all_literal(response),
    )
    shortlink = build_shortlink(0x0B41, 0, encrypted_response, 0x076D)
    transcripts = (
        _CLIENT_HELLO + server_hello,
        _CLIENT_HELLO + _TYPE8 + server_hello,
        _CLIENT_HELLO + server_hello + _TYPE8,
    )
    handshake_hash = hashlib.sha256(transcripts[transcript_variant]).digest()
    keys = derive_psk_one_way_keys(_PSK, b"handshake key expansion", handshake_hash)
    ciphertext = encrypt_record_payload(
        keys.key,
        keys.iv,
        sequence,
        0x17,
        shortlink,
    )
    return build_record(0x16, server_hello) + build_record(0x17, ciphertext)


class WeChatNetworkResponseTests(unittest.TestCase):
    def test_decrypts_verified_0rtt_login_response(self) -> None:
        for variant in range(3):
            for sequence in (2, 1, 3):
                with self.subTest(transcript_variant=variant, sequence=sequence):
                    body = _response_records(
                        b"wx-login-code",
                        transcript_variant=variant,
                        sequence=sequence,
                    )
                    self.assertEqual(
                        parse_0rtt_response(
                            body, _PSK, _CLIENT_HELLO, _TYPE8, _RECV_KEY
                        ),
                        "wx-login-code",
                    )

    def test_rejects_wx_login_business_errors(self) -> None:
        body = _response_records(b"", base_error=401)
        with self.assertRaises(WeChatAuthorizationError):
            parse_0rtt_response(body, _PSK, _CLIENT_HELLO, _TYPE8, _RECV_KEY)

    def test_rejects_success_response_without_code(self) -> None:
        body = _response_records(b"")
        with self.assertRaises(WeChatProtocolError):
            parse_0rtt_response(body, _PSK, _CLIENT_HELLO, _TYPE8, _RECV_KEY)
