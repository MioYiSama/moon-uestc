"""Student endpoints, plus separately grouped class-cadre capabilities."""

from __future__ import annotations

from datetime import date

from pydantic import JsonValue, TypeAdapter

from ._transport import Transport
from .models import (
    APIResponse,
    CampusFence,
    CaseKeyword,
    CheckinLocation,
    CheckinRecord,
    CheckinRequest,
    CheckinSchedule,
    ClassCheckinQuery,
    ClassSituationReport,
    HistoryQuery,
    Latitude,
    LocationLogRequest,
    LocationType,
    Longitude,
    ManagedClass,
    ReportRequest,
    RequestModel,
    SituationTypes,
)


class _SituationTarget(RequestModel):
    target: str = "SITUATION_TYPE"


class StudentService:
    def __init__(self, transport: Transport) -> None:
        self._http = transport

    def effective_schedule(self) -> APIResponse[CheckinSchedule | None]:
        return self._http.request("GET", "/api/checkin-schedule/effective", APIResponse[CheckinSchedule | None])

    def available_locations(self) -> APIResponse[list[CheckinLocation]]:
        return self._http.request("GET", "/api/checkin-location/available-locations", APIResponse[list[CheckinLocation]])

    def campus_fences(self) -> APIResponse[list[CampusFence]]:
        return self._http.request("GET", "/api/campus-fence/current-student", APIResponse[list[CampusFence]])

    def today(self) -> APIResponse[CheckinRecord | None]:
        return self._http.request("GET", "/api/student-checkin-record/today", APIResponse[CheckinRecord | None])

    def history(self, query: HistoryQuery) -> APIResponse[list[CheckinRecord]]:
        return self._http.request(
            "GET",
            "/api/student-checkin-record/list",
            APIResponse[list[CheckinRecord]],
            params={
                "year": query.year,
                "month": query.month,
                "pageIndex": query.page_index,
                "pageSize": query.page_size,
                "clockIn": "" if query.clock_in is None else str(query.clock_in).lower(),
            },
        )

    def checkin(self, request: CheckinRequest) -> APIResponse[JsonValue]:
        """Business write. Requires an official mini-program operation ticket."""
        return self._http.request(
            "POST", "/api/student-checkin-record/checkin", APIResponse[JsonValue], body=request, signed=True
        )

    def report(self, request: ReportRequest) -> APIResponse[JsonValue]:
        """Business write. Location/keyword policy remains server-authoritative."""
        return self._http.request(
            "POST", "/api/student-checkin-record/report", APIResponse[JsonValue], body=request, signed=True
        )

    def log_location(self, request: LocationLogRequest) -> APIResponse[JsonValue]:
        """Write location diagnostics; not called automatically by the SDK."""
        return self._http.request(
            "POST", "/api/student-checkin-record/location-log", APIResponse[JsonValue], body=request
        )

    def case_keywords(self, location_type: LocationType) -> APIResponse[list[CaseKeyword]]:
        return self._http.request(
            "GET", "/api/case-keyword/active", APIResponse[list[CaseKeyword]],
            params={"locationTypeCode": location_type.value},
        )

    def reverse_geocode(self, *, latitude: float, longitude: float) -> APIResponse[str]:
        # Public scalar parameters need the same boundary checks as request models.
        lat = TypeAdapter(Latitude).validate_python(latitude, strict=True)
        lng = TypeAdapter(Longitude).validate_python(longitude, strict=True)
        return self._http.request(
            "GET", "/api/map/reverse", APIResponse[str], params={"lat": lat, "lng": lng}
        )

    def situation_types(self) -> APIResponse[SituationTypes]:
        """Read-only metadata despite the POST verb; used by the class-cadre UI."""
        return self._http.request(
            "POST", "/api/common/attachMore", APIResponse[SituationTypes], body=[_SituationTarget()]
        )

    def download_avatar(self, file_path: str) -> bytes:
        """Download the relative FastDFS path supplied as lifeHeadUrl."""
        return self._http.avatar(file_path)


class ClassCadreService:
    """Requires userData.userType == CLASS_CADRE; the server enforces scope.

    Contracts are frontend-derived, not live-verified with a class-cadre account.
    """

    def __init__(self, transport: Transport) -> None:
        self._http = transport

    def my_classes(self) -> APIResponse[list[ManagedClass]]:
        return self._http.request("GET", "/api/class-cadre/my-classes", APIResponse[list[ManagedClass]])

    def checkin_list(self, query: ClassCheckinQuery) -> APIResponse[list[CheckinRecord]]:
        return self._http.request(
            "GET", "/api/class-cadre/checkin-list", APIResponse[list[CheckinRecord]],
            params={
                "businessDate": query.business_date.isoformat(),
                "classKey": query.class_key,
                "keyword": query.keyword,
                "pageIndex": query.page_index,
                "pageSize": query.page_size,
            },
        )

    def checkin_count(self, business_date: date) -> APIResponse[int]:
        return self._http.request(
            "GET", "/api/class-cadre/checkin-count", APIResponse[int],
            params={"businessDate": business_date.isoformat()},
        )

    def report_situations(self, reports: list[ClassSituationReport]) -> APIResponse[JsonValue]:
        """Business write for the classes assigned to the current class cadre."""
        return self._http.request(
            "POST", "/api/class-cadre/situation/report", APIResponse[JsonValue], body=reports
        )
