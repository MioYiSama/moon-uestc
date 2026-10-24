from __future__ import annotations

import json
import unittest
from unittest.mock import patch

import httpx2
from pydantic import SecretStr, ValidationError

import main
from moon_uestc import AuthorizationError, CheckinTicketError, MoonClient, ProtocolError
from moon_uestc.models import UserInfo


TICKET = "t" * 43


def user_info(user_id: str) -> dict[str, object]:
    return {"userData": {
        "id": user_id, "fullName": "Test", "username": user_id,
        "role": "001", "roleName": "学生", "ability": None,
        "roles": [{"id": "student", "name": "学生", "roleCode": "001", "permissions": None}],
    }}


class MiniProgramFlow:
    def __init__(self) -> None:
        self.bound = True
        self.mini_user_id = "school-user"
        self.reject_session = False
        self.missing_session = False
        self.logged_in = False
        self.identity_read = False
        self.consumed_codes: list[str] = []
        self.ticket_attempts = 0

    def __call__(self, request: httpx2.Request) -> httpx2.Response:
        path = request.url.path
        cookie = request.headers.get("cookie", "")
        if path == "/daily-checkin/api/auth/user-info":
            if cookie != "SESSION=h5-session" or request.headers["platform"] != "h5":
                raise AssertionError("H5 identity must retain its own session and platform")
            return httpx2.Response(200, json={"code": 0, "data": user_info("school-user")})
        if request.headers["platform"] != "mp-weixin" or "h5-session" in cookie:
            raise AssertionError("H5 session leaked into mini-program authentication")
        if path == "/platform-app/api/auth/check-bind":
            if cookie:
                raise AssertionError("mini-program login must start independently")
            data = json.loads(request.content)
            if data != {"code": "login-code", "sn": ""}:
                raise AssertionError("mini-program login must use its own fresh code")
            self.consumed_codes.append(data["code"])
            self.logged_in = True
            result: dict[str, object] = {"bind": self.bound}
            if not self.missing_session:
                result["sessionId"] = "mini-session"
            if not self.bound:
                result["authorizeUrl"] = "https://idas.uestc.edu.cn/private-binding-url"
            return httpx2.Response(200, json={"ok": True, "code": 0, "data": result})
        if not self.logged_in or cookie != "SESSION=mini-session":
            raise AssertionError("a verified mini-program session is required")
        if path == "/platform-app/api/auth/user-info":
            if self.reject_session:
                return httpx2.Response(401, json={"ok": False})
            self.identity_read = True
            return httpx2.Response(200, json={"ok": True, "data": user_info(self.mini_user_id)})
        if path == "/platform-app/api/auth/checkin-ticket":
            self.ticket_attempts += 1
            if not self.identity_read:
                raise AssertionError("mini-program identity must be checked before issuing tickets")
            code = json.loads(request.content)["code"]
            if code in self.consumed_codes or code != "ticket-code":
                return httpx2.Response(200, json={"ok": False, "msg": "code already used"})
            self.consumed_codes.append(code)
            return httpx2.Response(200, json={"ok": True, "data": {"ticket": TICKET}})
        raise AssertionError("unexpected business write: " + path)


class MiniProgramAuthenticationTests(unittest.TestCase):
    def setUp(self) -> None:
        self.flow = MiniProgramFlow()
        self.transport = httpx2.MockTransport(self.flow)

    def request_ticket(self) -> SecretStr:
        def client(*, session: str | SecretStr | None = None) -> MoonClient:
            return MoonClient(session=session, transport=self.transport)

        with client(session="h5-session") as h5:
            with patch.object(main, "MoonClient", side_effect=client), patch.object(
                main, "wechat_code", side_effect=[SecretStr("login-code"), SecretStr("ticket-code")],
            ):
                return main.checkin_ticket(h5)

    def test_login_and_ticket_use_separate_codes_and_session_from_check_bind(self) -> None:
        ticket = self.request_ticket()
        self.assertEqual(ticket.get_secret_value(), TICKET)
        self.assertEqual(self.flow.consumed_codes, ["login-code", "ticket-code"])
        self.assertNotIn(TICKET, repr(ticket))

    def test_different_bound_school_identity_cannot_obtain_ticket_for_h5_user(self) -> None:
        self.flow.mini_user_id = "different-school-user"
        with self.assertRaises(AuthorizationError):
            self.request_ticket()
        self.assertEqual(self.flow.ticket_attempts, 0)
        self.assertEqual(self.flow.consumed_codes, ["login-code"])

    def test_unbound_wechat_stops_before_ticket_exchange(self) -> None:
        self.flow.bound = False
        with self.assertRaises(CheckinTicketError):
            self.request_ticket()
        self.assertFalse(self.flow.identity_read)
        self.assertEqual(self.flow.ticket_attempts, 0)

    def test_bound_response_without_session_is_rejected_before_identity_read(self) -> None:
        self.flow.missing_session = True
        with self.assertRaises(ProtocolError):
            self.request_ticket()
        self.assertFalse(self.flow.identity_read)
        self.assertEqual(self.flow.ticket_attempts, 0)

    def test_mini_session_rejection_does_not_trigger_cas_reauthentication(self) -> None:
        self.flow.reject_session = True
        with self.assertRaises(CheckinTicketError) as caught:
            self.request_ticket()
        self.assertEqual(caught.exception.status_code, 401)
        self.assertEqual(self.flow.ticket_attempts, 0)

    def test_empty_login_code_is_rejected_before_network(self) -> None:
        def handler(_request: httpx2.Request) -> httpx2.Response:
            self.fail("an empty code must never be sent")

        with MoonClient(transport=httpx2.MockTransport(handler)) as client:
            with self.assertRaises(ValidationError):
                client.auth.check_wechat_bind(code=SecretStr(""))

    def test_null_permissions_are_empty_not_granted_or_unparseable(self) -> None:
        user = UserInfo.model_validate(user_info("school-user")).user_data
        self.assertEqual(user.ability, [])
        self.assertEqual(user.roles[0].permissions, [])


if __name__ == "__main__":
    unittest.main()
