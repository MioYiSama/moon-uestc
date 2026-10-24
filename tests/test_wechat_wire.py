from __future__ import annotations

import base64
import unittest

from cryptography.exceptions import InvalidTag

from moon_uestc._wechat_crypto import (
    aes_gcm_encrypt_layout,
    decrypt_record_payload,
    encrypt_record_payload,
)
from moon_uestc._wechat_wire import (
    decode_wpkg_head,
    extract_code,
    lz4_all_literal,
    parse_login_buffer,
    session_decrypt,
)


class WeChatWireAuthentication(unittest.TestCase):
    def test_decodes_upstream_wpkg_header_and_trailer_offset(self) -> None:
        header = bytes.fromhex(
            "01010102959aef3a0300040005818220060b0700080009000a010b000c000d00"
            "1100120114e00b150016959aef3a170019101a041c011d011e00000e001806"
            "6465766963651b026639004a"
        )

        _, byte_fields, offset = decode_wpkg_head(header)

        self.assertEqual(byte_fields[24], b"device")
        self.assertEqual(byte_fields[27], b"f9")
        self.assertEqual(offset, len(header))

    def test_record_decryption_authenticates_sequence_and_content_type(self) -> None:
        key = bytes(range(16))
        iv = bytes(range(12))
        ciphertext = encrypt_record_payload(key, iv, 4, 0x17, b"server flight")

        with self.assertRaises(InvalidTag):
            decrypt_record_payload(key, iv, 5, 0x17, ciphertext)
        with self.assertRaises(InvalidTag):
            decrypt_record_payload(key, iv, 4, 0x16, ciphertext)

    def test_transfer_response_authenticates_and_decompresses(self) -> None:
        key = bytes(range(16))
        response = b"\x12\x03\x1a\x01x"
        ciphertext = aes_gcm_encrypt_layout(key, b"", lz4_all_literal(response))

        self.assertEqual(session_decrypt(ciphertext, key), response)
        self.assertEqual(extract_code(response), b"x")

    def test_transfer_response_rejects_tampered_authentication_tag(self) -> None:
        key = bytes(range(16))
        sensitive_response = b"wx-login-code-must-not-be-returned-on-auth-failure"
        ciphertext = bytearray(
            aes_gcm_encrypt_layout(key, b"", lz4_all_literal(sensitive_response))
        )
        ciphertext[-1] ^= 1

        with self.assertRaisesRegex(ValueError, "could not be authenticated") as raised:
            session_decrypt(bytes(ciphertext), key)

        self.assertNotIn(sensitive_response.decode("ascii"), str(raised.exception))

    def test_login_buffer_rejects_truncated_protobuf_instead_of_partial_fields(self) -> None:
        malformed = base64.b64encode(b"\x0a\x05x").decode("ascii")

        with self.assertRaisesRegex(ValueError, "truncated protobuf bytes field"):
            parse_login_buffer(malformed)


if __name__ == "__main__":
    unittest.main()
