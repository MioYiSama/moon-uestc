"""Student API contracts extracted from the deployed H5 client.

Response models preserve new server fields. Request models reject unknown fields.
Server timestamps are local wall-clock times; no timezone is inferred.
"""

from __future__ import annotations

from datetime import date, datetime
from enum import StrEnum
from re import fullmatch
from typing import Annotated, ClassVar, Literal, Self

from pydantic import (
    BaseModel,
    ConfigDict,
    Field,
    JsonValue,
    SecretStr,
    field_serializer,
    field_validator,
    model_validator,
)
from pydantic.alias_generators import to_camel


type Latitude = Annotated[float, Field(ge=-90, le=90, allow_inf_nan=False)]
type Longitude = Annotated[float, Field(ge=-180, le=180, allow_inf_nan=False)]
type NonNegativeFloat = Annotated[float, Field(ge=0, allow_inf_nan=False)]
type PositiveInt = Annotated[int, Field(gt=0)]


class APIModel(BaseModel):
    model_config: ClassVar[ConfigDict] = ConfigDict(alias_generator=to_camel, populate_by_name=True, extra="allow")


class RequestModel(BaseModel):
    model_config: ClassVar[ConfigDict] = ConfigDict(
        alias_generator=to_camel, populate_by_name=True, extra="forbid", strict=True
    )


class RecordStatus(StrEnum):
    PENDING = "PENDING"
    CHECKED_IN = "CHECKED_IN"
    STUDENT_REPORTED = "STUDENT_REPORTED"
    INSTRUCTOR_REPORTED = "INSTRUCTOR_REPORTED"
    EXEMPT = "EXEMPT"


class LocationType(StrEnum):
    CAMPUS = "CAMPUS"
    OUTSIDE = "OUTSIDE"


class PageInfo(APIModel):
    page_index: int
    page_size: int
    total_count: int
    total_page: int
    order_by: str | None = None


class APIResponse[T](APIModel):
    data: T
    code: int | None = None
    msg: str | None = None
    message: str | None = None
    status: bool | None = None
    ok: bool | None = None
    page: PageInfo | None = None


class Permission(APIModel):
    action: str
    subject: str


class Role(APIModel):
    id: str
    name: str
    role_code: str
    role_group_id: str | None = None
    permissions: list[Permission] = Field(default_factory=list[Permission])

    @field_validator("permissions", mode="before")
    @classmethod
    def empty_permissions(cls, value: object) -> object:
        return [] if value is None else value


class UserData(APIModel):
    id: str
    full_name: str
    username: str
    role: str
    role_name: str
    avatar: str | None = None
    email: str | None = None
    invalid: bool = False
    ability: list[Permission] = Field(default_factory=list[Permission])
    roles: list[Role] = Field(default_factory=list[Role])
    user_type: str | None = None
    college_sns: list[str] = Field(default_factory=list)
    token: SecretStr | None = None

    @field_validator("ability", mode="before")
    @classmethod
    def empty_ability(cls, value: object) -> object:
        return [] if value is None else value


class UserInfo(APIModel):
    user_data: UserData
    access_token: SecretStr | None = None
    refresh_token: SecretStr | None = None


class WeChatBindResult(APIModel):
    """Mini-program login result; SESSION is distinct from the H5 session."""

    model_config: ClassVar[ConfigDict] = ConfigDict(hide_input_in_errors=True)
    bind: bool = Field(strict=True)
    session_id: SecretStr | None = None
    authorize_url: SecretStr | None = None

    @model_validator(mode="after")
    def validate_bound_session(self) -> Self:
        if self.bind and self.session_id is None:
            raise ValueError("A bound WeChat login must include sessionId")
        if self.session_id is not None:
            value = self.session_id.get_secret_value()
            if not value or any(
                ord(char) <= 0x20 or ord(char) >= 0x7F or char in '";,\\'
                for char in value
            ):
                raise ValueError("sessionId must be a non-empty HTTP cookie value")
        return self


class CheckinTicket(APIModel):
    """Server-issued operation ticket; no expiry or generation scheme is inferred."""

    model_config: ClassVar[ConfigDict] = ConfigDict(hide_input_in_errors=True)
    ticket: SecretStr

    @field_validator("ticket")
    @classmethod
    def _validate_ticket(cls, value: SecretStr) -> SecretStr:
        if fullmatch(r"[A-Za-z0-9_-]{43}", value.get_secret_value()) is None:
            raise ValueError("Expected a 43-character URL-safe checkin ticket")
        return value


class CASCredentials(RequestModel):
    """Own CAS cookies; returned values include server-side cookie rotation."""

    model_config: ClassVar[ConfigDict] = ConfigDict(hide_input_in_errors=True)
    castgc: SecretStr
    browser_fingerprint: SecretStr
    user_agent: str

    @field_validator("castgc", "browser_fingerprint")
    @classmethod
    def validate_cookie(cls, value: SecretStr) -> SecretStr:
        raw = value.get_secret_value()
        if not raw or any(ord(char) <= 0x20 or ord(char) >= 0x7F or char in '";,\\' for char in raw):
            raise ValueError("CAS cookie must be a non-empty HTTP cookie value")
        return value

    @field_validator("user_agent")
    @classmethod
    def validate_user_agent(cls, value: str) -> str:
        if not value.strip() or any(ord(char) < 0x20 or ord(char) == 0x7F for char in value):
            raise ValueError("user_agent must be a non-empty single-line browser user agent")
        return value


class CASSession(APIModel):
    """A verified new SESSION; the original MoonClient remains unchanged."""

    session: SecretStr
    credentials: CASCredentials
    user_info: UserInfo


class Entity(APIModel):
    id: str
    create_time: datetime | None = None
    update_time: datetime | None = None


class CheckinSchedule(Entity):
    schedule_name: str
    student_checkin_start_offset_minute: int
    student_checkin_end_offset_minute: int
    student_submission_start_offset_minute: int | None = None
    student_submission_end_offset_minute: int | None = None
    student_report_start_offset_minute: int | None = None
    student_report_end_offset_minute: int | None = None
    instructor_report_start_offset_minute: int | None = None
    instructor_report_end_offset_minute: int | None = None
    college_confirm_start_offset_minute: int | None = None
    college_confirm_end_offset_minute: int | None = None
    effective: bool


class CheckinLocation(Entity):
    location_name: str
    address: str | None = None
    longitude: float
    latitude: float
    radius_meter: float
    group_id: str | None = None
    group_name: str | None = None
    creator_sn: str | None = None
    creator_name: str | None = None
    creator_phone: str | None = None
    operator_sn: str | None = None
    operator_name: str | None = None
    operator_phone: str | None = None
    operation_time: datetime | None = None


class CampusFence(Entity):
    campus_code: str
    campus_name: str
    coordinate_system_code: str
    fence_geojson: str
    status_code: str


class CaseKeyword(Entity):
    situation_type_code: LocationType
    keyword: str
    remark_required: bool
    status_code: str
    situation_type_value: str | None = None
    creator_sn: str | None = None
    creator_name: str | None = None
    creator_phone: str | None = None


class InstructorContact(APIModel):
    sn: str = ""
    name: str = ""
    phone: str = ""


class StudentDormitory(Entity):
    student_sn: str
    student_name: str | None = None
    sync_batch_id: str | None = None
    building_id: str | None = None
    room_number: str | None = None
    source_create_date: date | None = None


class StudentLocationLog(Entity):
    checkin_record_id: str
    location_scene_code: str | None = None
    location_result_code: str | None = None
    client_time: datetime | None = None
    server_receive_time: datetime | None = None
    client_server_skew_ms: int | None = None
    latitude: float | None = None
    longitude: float | None = None
    coordinate_system_code: str | None = None
    gps_accuracy_meter: float | None = None
    altitude_meter: float | None = None
    altitude_accuracy_meter: float | None = None
    heading_degree: float | None = None
    speed_meter_per_second: float | None = None
    location_duration_ms: int | None = None
    reverse_geocode: str | None = None
    network_type_code: str | None = None
    location_authorized: str | None = None
    location_accuracy: str | None = None
    location_enabled: bool | None = None
    client_ip: str | None = None
    wechat_flag: bool | None = None
    wechat_version: str | None = None
    wechat_sdk_version: str | None = None
    device_platform_code: str | None = None
    device_model: str | None = None
    os_version: str | None = None
    user_agent: str | None = None
    device_fingerprint_hash: str | None = None
    mock_location_flag: bool | None = None
    anti_cheat_result_code: str | None = None
    anti_cheat_detail: JsonValue = None
    error_message: str | None = None


class CheckinRecord(Entity):
    business_date: date
    student_sn: str
    student_name: str
    record_status_code: RecordStatus
    academic_year: str | None = None
    semester: str | None = None
    life_head_url: str | None = None
    gender_code: str | None = None
    phone: str | None = None
    college_code: str | None = None
    college_name: str | None = None
    major_code: str | None = None
    major_name: str | None = None
    class_code: str | None = None
    class_name: str | None = None
    class_key: str | None = None
    grade: str | None = None
    checkin_location_id: str | None = None
    checkin_location_name: str | None = None
    checkin_location_latitude: float | None = None
    checkin_location_longitude: float | None = None
    checkin_radius_meter: float | None = None
    dormitory_location: str | None = None
    checkin_time: datetime | None = None
    latitude: float | None = None
    longitude: float | None = None
    reverse_geocode: str | None = None
    distance_meter: float | None = None
    reported_location_type_code: LocationType | None = None
    reported_location_type_value: str | None = None
    case_source_code: str | None = None
    instructor_confirmed_flag: bool | None = None
    abnormal_flag: bool | None = None
    abnormal_origin_record_id: str | None = None
    abnormal_reason: str | None = None
    abnormal_time: datetime | None = None
    abnormal_operator_sn: str | None = None
    abnormal_operator_name: str | None = None
    abnormal_closed_flag: bool | None = None
    abnormal_closed_time: datetime | None = None
    abnormal_closed_operator_sn: str | None = None
    abnormal_closed_operator_name: str | None = None
    case_keyword_id: str | None = None
    case_keyword: str | None = None
    case_remark: str | None = None
    report_time: datetime | None = None
    instructor_list: list[InstructorContact] = Field(default_factory=list[InstructorContact])
    instructor_sn: str | None = None
    instructor_name: str | None = None
    instructor_phone: str | None = None
    exemption_id: str | None = None
    student_tag: JsonValue = None
    student_tags: list[JsonValue] = Field(default_factory=list[JsonValue])
    student_dormitory: StudentDormitory | None = None
    student_location_log: StudentLocationLog | None = None
    checkin_exemption: JsonValue = None
    monthly_miss_count: int | None = None
    latest_abnormal_track: JsonValue = None
    abnormal_track_count: int | None = None

    @field_validator("instructor_list", mode="before")
    @classmethod
    def decode_instructor_list(cls, value: object) -> object:
        # The server sends JSON-in-a-string; some deployments send an array.
        if value is None or value == "":
            return []
        if isinstance(value, str):
            from json import loads

            return loads(value)
        return value


class HistoryQuery(RequestModel):
    year: Annotated[int, Field(ge=1, le=9999)]
    month: Annotated[int, Field(ge=1, le=12)]
    page_index: PositiveInt = 1
    page_size: PositiveInt = 10
    clock_in: bool | None = None


class LocationTelemetry(RequestModel):
    client_time: datetime
    coordinate_system_code: Literal["gcj02"] = "gcj02"
    user_agent: str | None = None
    accuracy: NonNegativeFloat | None = None
    location_duration_ms: Annotated[int, Field(ge=0)] | None = None
    network_type_code: str | None = None
    location_enabled: bool | None = None
    location_authorized: str | None = None
    location_accuracy: str | None = None
    error_message: str | None = None

    @field_validator("client_time")
    @classmethod
    def require_local_time(cls, value: datetime) -> datetime:
        if value.tzinfo is not None:
            raise ValueError("client_time must be a naive server-local wall-clock time")
        return value

    @field_serializer("client_time")
    def serialize_client_time(self, value: datetime) -> str:
        return value.strftime("%Y-%m-%d %H:%M:%S")


class CheckinRequest(LocationTelemetry):
    latitude: Latitude
    longitude: Longitude
    reverse_geocode: str


class LocationLogRequest(LocationTelemetry):
    """Failure/permission diagnostics sent by the student location UI."""


class ReportRequest(RequestModel):
    reported_location_type_code: LocationType
    case_keyword_id: str
    latitude: Latitude
    longitude: Longitude
    reverse_geocode: str
    coordinate_system_code: Literal["gcj02"] = "gcj02"
    gps_accuracy_meter: NonNegativeFloat | None = None
    case_keyword: str | None = None
    case_remark: str = ""


class SituationOption(APIModel):
    label: str
    value: LocationType
    ext: JsonValue = None
    children: list[SituationOption] | None = None


class SituationTypes(APIModel):
    situation_type_options: list[SituationOption]


class ManagedClass(APIModel):
    """Frontend-derived; not verified with a class-cadre account."""

    class_key: str
    manage_class: str


class ClassCheckinQuery(RequestModel):
    business_date: date
    class_key: str
    keyword: str = ""
    page_index: PositiveInt = 1
    page_size: PositiveInt = 10


class ClassSituationReport(RequestModel):
    class_key: str
    record_id: str
    location_type_code: LocationType
    case_keyword_id: str
    case_remark: str = ""
