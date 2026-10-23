from __future__ import annotations

from datetime import datetime
import unittest

import httpx2
from pydantic import SecretStr, ValidationError

from moon_uestc import APIError, CheckinTicketError, MoonClient, ProtocolError
from moon_uestc.models import CheckinRequest


TICKET = "aB0_-" * 8 + "xyz"


class CheckinTicketTests(unittest.TestCase):
    def test_raw_ok_controls_success_independently_of_h5_status(self) -> None:
        cases: list[tuple[dict[str, object], bool]] = [
            ({"ok": True}, True),
            ({"ok": True, "code": 0}, True),
            ({"ok": False, "code": 0}, False),
            ({"ok": False, "status": True}, False),
            ({"code": 0}, False),
            ({"status": True}, False),
            ({"ok": 1}, False),
            ({"ok": "true"}, False),
        ]
        for envelope, succeeds in cases:
            with self.subTest(envelope=envelope):
                payload = {**envelope, "data": {"ticket": TICKET}}
                transport = httpx2.MockTransport(lambda _: httpx2.Response(200, json=payload))
                with MoonClient(transport=transport) as client:
                    if succeeds:
                        response = client.auth.request_checkin_ticket(code="wx-code")
                        self.assertEqual(response.data.ticket.get_secret_value(), TICKET)
                        self.assertNotIn(TICKET, repr(response))
                        self.assertNotIn(TICKET, response.model_dump_json())
                    else:
                        with self.assertRaises(APIError):
                            client.auth.request_checkin_ticket(code="wx-code")

    def test_malformed_ticket_data_is_a_sanitized_protocol_error(self) -> None:
        cases: list[object] = [
            None, {}, {"ticket": None}, {"ticket": 123}, {"ticket": "a" * 42},
            {"ticket": "a" * 44}, {"ticket": "a" * 42 + "+"},
            {"ticket": "a" * 42 + "中"}, {"ticket": TICKET + "\n"},
        ]
        for data in cases:
            with self.subTest(data=data):
                payload = {"ok": True, "data": data}
                transport = httpx2.MockTransport(lambda _: httpx2.Response(200, json=payload))
                with MoonClient(transport=transport) as client:
                    with self.assertRaises(ProtocolError) as caught:
                        client.auth.request_checkin_ticket(code="private-wx-code")
                self.assertNotIn("private-wx-code", str(caught.exception))
                self.assertNotIn(TICKET, str(caught.exception))

    def test_failed_exchange_never_retries_or_replaces_installed_ticket(self) -> None:
        attempts = 0
        submissions: list[str | None] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            nonlocal attempts
            if request.url.path == "/platform-app/api/auth/checkin-ticket":
                attempts += 1
                return httpx2.Response(403, json={"ok": False, "code": 46002, "msg": "重新授权"})
            submissions.append(request.headers.get("X-Checkin-Ticket"))
            return httpx2.Response(200, json={"code": 0})

        with MoonClient(checkin_ticket="previous-ticket", transport=httpx2.MockTransport(handler)) as client:
            with self.assertRaises(CheckinTicketError) as caught:
                client.auth.request_checkin_ticket(code="wx-code")
            self.assertEqual(caught.exception.code, 46002)
            self.assertEqual(caught.exception.status_code, 403)
            client.student.checkin(CheckinRequest(
                client_time=datetime(2026, 10, 23, 21), latitude=30, longitude=104,
                reverse_geocode="local fixture",
            ))
        self.assertEqual(attempts, 1)
        self.assertEqual(submissions, ["previous-ticket"])

    def test_empty_code_fails_before_network(self) -> None:
        def handler(_: httpx2.Request) -> httpx2.Response:
            self.fail("empty code must not be sent")

        with MoonClient(transport=httpx2.MockTransport(handler)) as client:
            for code in ("", SecretStr("")):
                with self.subTest(code=code), self.assertRaises(ValidationError):
                    client.auth.request_checkin_ticket(code=code)


if __name__ == "__main__":
    unittest.main()
