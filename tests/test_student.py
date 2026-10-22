from __future__ import annotations

from datetime import date, datetime, timezone
import unittest

import httpx2
from pydantic import ValidationError

from moon_uestc import (
    APIError,
    AuthenticationError,
    AuthorizationError,
    CheckinTicketError,
    MoonClient,
    ProtocolError,
)
from moon_uestc.models import (
    CheckinRequest,
    HistoryQuery,
    LocationType,
    RecordStatus,
    ReportRequest,
)


def client_with_response(payload: object, *, status: int = 200) -> MoonClient:
    return MoonClient(transport=httpx2.MockTransport(lambda _: httpx2.Response(status, json=payload)))


class ResponseContractTests(unittest.TestCase):
    def test_record_decodes_json_contacts_dates_and_optional_fields(self) -> None:
        payload = {
            "code": 0,
            "data": {
                "id": "record-1",
                "businessDate": "2026-10-22",
                "studentSn": "student-1",
                "studentName": "Example",
                "recordStatusCode": "CHECKED_IN",
                "instructorList": '[{"sn":"teacher-1","name":"Teacher","phone":"10000"}]',
                "checkinTime": "2026-10-22 21:30:00",
                "reportedLocationTypeCode": None,
                "newServerField": {"version": 2},
            },
        }
        with client_with_response(payload) as c:
            record = c.student.today().data
        assert record is not None
        self.assertEqual(record.business_date, date(2026, 10, 22))
        self.assertEqual(record.checkin_time, datetime(2026, 10, 22, 21, 30))
        self.assertEqual(record.record_status_code, RecordStatus.CHECKED_IN)
        self.assertEqual(record.instructor_list[0].sn, "teacher-1")
        self.assertIsNone(record.reported_location_type_code)
        self.assertEqual(record.model_extra, {"newServerField": {"version": 2}})

    def test_null_today_differs_from_malformed_record(self) -> None:
        with client_with_response({"code": 0, "data": None}) as c:
            self.assertIsNone(c.student.today().data)
        with client_with_response({"code": 0, "data": {"id": "broken"}}) as c:
            with self.assertRaises(ProtocolError):
                c.student.today()

    def test_pagination_keeps_large_string_counts_exact(self) -> None:
        with client_with_response({
            "code": 0,
            "data": [],
            "page": {"pageIndex": 2, "pageSize": 10, "totalCount": "9007199254740993", "totalPage": 900719925474100},
        }) as c:
            response = c.student.history(HistoryQuery(year=2026, month=10, page_index=2))
        assert response.page is not None
        self.assertEqual(response.page.total_count, 9007199254740993)
        self.assertEqual(response.page.page_index, 2)

    def test_business_errors_are_not_http_success(self) -> None:
        cases: list[tuple[int, str, type[APIError]]] = [
            (5000, "系统异常: 未登录或会话已失效", AuthenticationError),
            (4003, "无权执行该操作", AuthorizationError),
            (46001, "授权无效", CheckinTicketError),
            (46002, "授权过期", CheckinTicketError),
            (46003, "授权身份不一致", CheckinTicketError),
            (5000, "其它系统错误", APIError),
        ]
        for code, message, error_type in cases:
            with self.subTest(code=code, message=message):
                with client_with_response({"code": code, "msg": message}) as c:
                    with self.assertRaises(error_type) as caught:
                        c.student.today()
                    self.assertEqual(caught.exception.code, code)

    def test_non_success_ticket_code_keeps_operation_error(self) -> None:
        with client_with_response({"code": 46002, "msg": "授权失效"}, status=403) as c:
            with self.assertRaises(CheckinTicketError) as caught:
                c.student.today()
        self.assertEqual(caught.exception.code, 46002)
        self.assertEqual(caught.exception.status_code, 403)

    def test_boolean_code_is_not_numeric_success(self) -> None:
        with client_with_response({"code": False, "data": None}) as c:
            with self.assertRaises(ProtocolError):
                c.student.today()

    def test_redirect_is_not_followed_with_credentials(self) -> None:
        requests: list[httpx2.Request] = []

        def redirect(request: httpx2.Request) -> httpx2.Response:
            requests.append(request)
            return httpx2.Response(302, headers={"Location": "https://other.invalid/login"})

        with MoonClient(session="private-session", transport=httpx2.MockTransport(redirect)) as c:
            with self.assertRaises(ProtocolError):
                c.auth.user_info()
        self.assertEqual(len(requests), 1)
        self.assertEqual(str(requests[0].url.host), "jzsz.uestc.edu.cn")

    def test_login_html_is_not_treated_as_data(self) -> None:
        transport = httpx2.MockTransport(lambda _: httpx2.Response(200, text="<html>login</html>"))
        with MoonClient(transport=transport) as c:
            with self.assertRaises(ProtocolError):
                c.student.today()

    def test_binary_avatar_without_mime_header_and_json_error(self) -> None:
        image = b"\xff\xd8\xff\xe1image"
        transport = httpx2.MockTransport(lambda _: httpx2.Response(200, content=image))
        with MoonClient(transport=transport) as c:
            self.assertEqual(c.student.download_avatar("group/example.jpg"), image)
        with client_with_response({"code": 4003, "msg": "禁止访问"}) as c:
            with self.assertRaises(AuthorizationError):
                c.student.download_avatar("group/example.jpg")


class SubmissionBoundaryTests(unittest.TestCase):
    def test_missing_ticket_prevents_writes_before_network(self) -> None:
        calls: list[httpx2.Request] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            calls.append(request)
            raise AssertionError("must not submit without operation authorization")

        with MoonClient(session="login-only", transport=httpx2.MockTransport(handler)) as c:
            with self.assertRaises(CheckinTicketError):
                c.student.checkin(CheckinRequest(
                    client_time=datetime(2026, 10, 22, 21),
                    latitude=30.0, longitude=104.0, reverse_geocode="example",
                ))
            with self.assertRaises(CheckinTicketError):
                c.student.report(ReportRequest(
                    reported_location_type_code=LocationType.CAMPUS,
                    case_keyword_id="keyword-1", latitude=30.0, longitude=104.0,
                    reverse_geocode="example",
                ))
        self.assertEqual(calls, [])

    def test_rejected_submission_is_not_retried(self) -> None:
        submissions: list[httpx2.Request] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            submissions.append(request)
            return httpx2.Response(200, json={"code": 46002, "msg": "授权失效"})

        with MoonClient(checkin_ticket="expired", transport=httpx2.MockTransport(handler)) as c:
            with self.assertRaises(CheckinTicketError):
                c.student.checkin(CheckinRequest(
                    client_time=datetime(2026, 10, 22, 21),
                    latitude=30.0, longitude=104.0, reverse_geocode="example",
                ))
        self.assertEqual(len(submissions), 1)

    def test_history_filter_distinguishes_false_and_all(self) -> None:
        filters: list[str] = []

        def handler(request: httpx2.Request) -> httpx2.Response:
            filters.append(request.url.params["clockIn"])
            return httpx2.Response(200, json={"code": 0, "data": []})

        with MoonClient(transport=httpx2.MockTransport(handler)) as c:
            for value in (False, None, True):
                c.student.history(HistoryQuery(year=2026, month=10, clock_in=value))
        self.assertEqual(filters, ["false", "", "true"])

    def test_client_time_has_server_wall_clock_format_and_rejects_timezone(self) -> None:
        request = CheckinRequest(
            client_time=datetime(2026, 10, 22, 21, 30, 1),
            latitude=30.0, longitude=104.0, reverse_geocode="example",
        )
        self.assertEqual(
            request.model_dump(mode="json", by_alias=True)["clientTime"],
            "2026-10-22 21:30:01",
        )
        with self.assertRaises(ValidationError):
            CheckinRequest(
                client_time=datetime(2026, 10, 22, 21, 30, tzinfo=timezone.utc),
                latitude=30.0, longitude=104.0, reverse_geocode="example",
            )

    def test_invalid_coordinates_and_month_fail_locally(self) -> None:
        for latitude in (91.0, float("nan"), float("inf")):
            with self.subTest(latitude=latitude):
                with self.assertRaises(ValidationError):
                    CheckinRequest(client_time=datetime(2026, 10, 22), latitude=latitude, longitude=104.0, reverse_geocode="")
        with self.assertRaises(ValidationError):
            HistoryQuery(year=2026, month=13)


if __name__ == "__main__":
    unittest.main()
