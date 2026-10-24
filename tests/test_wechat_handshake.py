from __future__ import annotations

import hashlib
import hmac
import socket
import struct
import time
import unittest
from concurrent.futures import ThreadPoolExecutor
from unittest.mock import patch

from cryptography.hazmat.primitives import serialization
from cryptography.hazmat.primitives.asymmetric import ec

from moon_uestc._wechat_crypto import (
    decrypt_record_payload,
    derive_handshake_keys,
    encrypt_record_payload,
    finished_verify_data,
    mmtls_secret,
)
from moon_uestc._wechat_mmtls import MmtlsConnection
from moon_uestc._wechat_wire import build_finished, build_handshake, build_record, parse_finished, split_handshake


def receive_exact(sock: socket.socket, length: int) -> bytes:
    result = bytearray()
    while len(result) < length:
        chunk = sock.recv(length - len(result))
        if not chunk:
            raise EOFError("test peer closed")
        result.extend(chunk)
    return bytes(result)


def receive_record(sock: socket.socket) -> tuple[int, bytes]:
    content_type, version, length = struct.unpack(">BHH", receive_exact(sock, 5))
    if version != 0xF103:
        raise ValueError("unexpected record version")
    return content_type, receive_exact(sock, length)


class WeChatHandshakeTests(unittest.TestCase):
    def test_both_finished_messages_use_the_same_pre_finished_transcript(self) -> None:
        client_key = ec.derive_private_key(1, ec.SECP256R1())
        server_key = ec.derive_private_key(2, ec.SECP256R1())
        client_public = client_key.public_key().public_bytes(
            serialization.Encoding.X962, serialization.PublicFormat.UncompressedPoint,
        )
        server_public = server_key.public_key().public_bytes(
            serialization.Encoding.X962, serialization.PublicFormat.UncompressedPoint,
        )

        def peer(sock: socket.socket) -> bool:
            _, client_hello = receive_record(sock)
            key_share = b"\x00\x00" + struct.pack(">IH", 1, len(server_public)) + server_public
            extension = b"\x01" + struct.pack(">I", len(key_share)) + key_share
            server_hello = build_handshake(2, b"\x03\xf1\xc0\x2b" + bytes(32) + struct.pack(">I", len(extension)) + extension)
            secret = mmtls_secret(server_key, client_public)
            keys = derive_handshake_keys(secret, hashlib.sha256(client_hello + server_hello).digest())
            certificate_verify = build_handshake(0x0F, b"synthetic-certificate")
            final_hash = hashlib.sha256(client_hello + server_hello + certificate_verify).digest()
            server_finished = build_finished(finished_verify_data(secret, False, final_hash))
            sock.sendall(
                build_record(0x16, server_hello)
                + build_record(0x16, encrypt_record_payload(keys.server_key, keys.server_iv, 1, 0x16, certificate_verify))
                + build_record(0x16, encrypt_record_payload(keys.server_key, keys.server_iv, 2, 0x16, server_finished))
            )
            content_type, ciphertext = receive_record(sock)
            message_type, body = split_handshake(decrypt_record_payload(
                keys.client_key, keys.client_iv, 1, content_type, ciphertext,
            ))
            if content_type != 0x16 or message_type != 0x14:
                return False
            return hmac.compare_digest(
                parse_finished(body), finished_verify_data(secret, True, final_hash),
            )

        client_socket, server_socket = socket.socketpair()
        with client_socket, server_socket:
            client_socket.settimeout(3)
            server_socket.settimeout(3)
            with ThreadPoolExecutor(max_workers=1) as executor:
                result = executor.submit(peer, server_socket)
                with patch("moon_uestc._wechat_mmtls.generate_ecdh", return_value=(client_key, client_public)):
                    connection = MmtlsConnection(client_socket, time.monotonic() + 3)
                    connection.handshake()
                    connection.finish_handshake()
                self.assertTrue(result.result(timeout=3), "peer rejected ClientFinished transcript")


if __name__ == "__main__":
    unittest.main()
