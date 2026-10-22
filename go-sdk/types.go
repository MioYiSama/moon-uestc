package moonuestc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// CoordinateSystemGCJ02 is the only coordinate system accepted by writes.
const CoordinateSystemGCJ02 = "gcj02"

// ServerLocation is the UTC+8 wall clock used by the service. China has no DST.
var ServerLocation = time.FixedZone("CST", 8*60*60)

// Latitude is a WGS/GCJ latitude in degrees. Range and finiteness are checked
// by request validation.
type Latitude float64

// Longitude is a WGS/GCJ longitude in degrees. Range and finiteness are checked
// by request validation.
type Longitude float64

// LocationType distinguishes on-campus and off-campus situation reports.
type LocationType string

const (
	LocationTypeCampus  LocationType = "CAMPUS"
	LocationTypeOutside LocationType = "OUTSIDE"
)

// Valid reports whether t is a known situation location type.
func (t LocationType) Valid() bool {
	return t == LocationTypeCampus || t == LocationTypeOutside
}

// RecordStatus is the student check-in state for a business date.
type RecordStatus string

const (
	RecordStatusPending            RecordStatus = "PENDING"
	RecordStatusCheckedIn          RecordStatus = "CHECKED_IN"
	RecordStatusStudentReported    RecordStatus = "STUDENT_REPORTED"
	RecordStatusInstructorReported RecordStatus = "INSTRUCTOR_REPORTED"
	RecordStatusExempt             RecordStatus = "EXEMPT"
)

// Date is a civil date without a timezone. JSON is YYYY-MM-DD.
type Date struct{ time.Time }

// NewDate builds a civil date.
func NewDate(year int, month time.Month, day int) Date {
	return Date{time.Date(year, month, day, 0, 0, 0, 0, time.UTC)}
}

// ParseDate parses YYYY-MM-DD.
func ParseDate(value string) (Date, error) {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return Date{}, fmt.Errorf("moonuestc: invalid date %q", value)
	}
	return Date{parsed}, nil
}

// DateFromTime keeps only the civil date components of t.
func DateFromTime(t time.Time) Date {
	return NewDate(t.Year(), t.Month(), t.Day())
}

func (d Date) String() string {
	if d.Time.IsZero() {
		return ""
	}
	return d.Time.Format("2006-01-02")
}

// MarshalJSON serializes the civil date and rejects the zero value.
func (d Date) MarshalJSON() ([]byte, error) {
	if d.Time.IsZero() {
		return nil, fmt.Errorf("moonuestc: cannot encode zero Date")
	}
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts YYYY-MM-DD or null.
func (d *Date) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		d.Time = time.Time{}
		return nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fmt.Errorf("moonuestc: date must be a string: %w", err)
	}
	parsed, err := ParseDate(value)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// DateTime is a server-local wall-clock timestamp. JSON uses
// YYYY-MM-DD HH:mm:ss; timezone offsets are never sent.
type DateTime struct{ time.Time }

var dateTimeLayouts = []string{
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	time.RFC3339Nano,
}

// NewDateTime builds a wall-clock timestamp.
func NewDateTime(year int, month time.Month, day, hour, minute, second int) DateTime {
	return DateTime{time.Date(year, month, day, hour, minute, second, 0, time.UTC)}
}

// DateTimeFromTime keeps the displayed wall-clock components of t and drops
// its location. Convert with time.In(ServerLocation) first when t is an instant.
func DateTimeFromTime(t time.Time) DateTime {
	return NewDateTime(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
}

// DateTimeNow returns the current wall clock at the service's UTC+8 location.
func DateTimeNow() DateTime {
	return DateTimeFromTime(time.Now().In(ServerLocation))
}

// ParseDateTime parses the server wall-clock format or RFC 3339.
func ParseDateTime(value string) (DateTime, error) {
	for _, layout := range dateTimeLayouts {
		if parsed, err := time.Parse(layout, value); err == nil {
			return DateTime{parsed}, nil
		}
	}
	return DateTime{}, fmt.Errorf("moonuestc: invalid datetime %q", value)
}

func (d DateTime) String() string {
	if d.Time.IsZero() {
		return ""
	}
	return d.Time.Format("2006-01-02 15:04:05")
}

// MarshalJSON serializes to service wall-clock format and rejects the zero value.
func (d DateTime) MarshalJSON() ([]byte, error) {
	if d.Time.IsZero() {
		return nil, fmt.Errorf("moonuestc: cannot encode zero DateTime")
	}
	return json.Marshal(d.String())
}

// UnmarshalJSON accepts the service wall-clock format, RFC 3339 or null.
func (d *DateTime) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		d.Time = time.Time{}
		return nil
	}
	var value string
	if err := json.Unmarshal(trimmed, &value); err != nil {
		return fmt.Errorf("moonuestc: datetime must be a string: %w", err)
	}
	parsed, err := ParseDateTime(value)
	if err != nil {
		return err
	}
	*d = parsed
	return nil
}

// Int64 accepts either a JSON integer or a decimal string. The deployed
// service sends some counters as strings and pagination totals as numbers.
type Int64 int64

func (n Int64) String() string { return strconv.FormatInt(int64(n), 10) }

// MarshalJSON always emits a number.
func (n Int64) MarshalJSON() ([]byte, error) {
	return []byte(strconv.FormatInt(int64(n), 10)), nil
}

// UnmarshalJSON accepts null, an integer, or a quoted integer.
func (n *Int64) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		*n = 0
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) {
		var text string
		if err := json.Unmarshal(data, &text); err != nil {
			return fmt.Errorf("moonuestc: integer string: %w", err)
		}
		trimmed = strings.TrimSpace(text)
	}
	if trimmed == "" {
		return fmt.Errorf("moonuestc: empty integer")
	}
	parsed, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return fmt.Errorf("moonuestc: expected integer, got %s", trimmed)
	}
	*n = Int64(parsed)
	return nil
}

// InstructorContact is a student's instructor contact.
type InstructorContact struct {
	SN    string `json:"sn"`
	Name  string `json:"name"`
	Phone string `json:"phone"`
}

// InstructorList decodes the service's JSON-in-a-string contact list, a real
// array, null or an empty string.
type InstructorList []InstructorContact

// MarshalJSON always emits an array, including for a nil list.
func (l InstructorList) MarshalJSON() ([]byte, error) {
	if l == nil {
		return []byte("[]"), nil
	}
	return json.Marshal([]InstructorContact(l))
}

// UnmarshalJSON accepts a JSON string, array, empty string or null.
func (l *InstructorList) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*l = nil
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] == '"' {
		var inner string
		if err := json.Unmarshal(trimmed, &inner); err != nil {
			return fmt.Errorf("moonuestc: instructor list string: %w", err)
		}
		if strings.TrimSpace(inner) == "" {
			*l = nil
			return nil
		}
		var contacts []InstructorContact
		if err := json.Unmarshal([]byte(inner), &contacts); err != nil {
			return fmt.Errorf("moonuestc: embedded instructor list: %w", err)
		}
		*l = contacts
		return nil
	}
	var contacts []InstructorContact
	if err := json.Unmarshal(trimmed, &contacts); err != nil {
		return fmt.Errorf("moonuestc: instructor list: %w", err)
	}
	*l = contacts
	return nil
}

// PageInfo is pagination metadata returned beside list data.
type PageInfo struct {
	PageIndex  int     `json:"pageIndex"`
	PageSize   int     `json:"pageSize"`
	TotalCount Int64   `json:"totalCount"`
	TotalPage  Int64   `json:"totalPage"`
	OrderBy    *string `json:"orderBy,omitempty"`
}

// Response is the validated business envelope around typed data.
type Response[T any] struct {
	Data    T         `json:"data"`
	Code    *int64    `json:"code,omitempty"`
	Msg     string    `json:"msg,omitempty"`
	Message string    `json:"message,omitempty"`
	Status  *bool     `json:"status,omitempty"`
	OK      *bool     `json:"ok,omitempty"`
	Page    *PageInfo `json:"page,omitempty"`
}

// Permission is a role or ability grant.
type Permission struct {
	Action  string `json:"action"`
	Subject string `json:"subject"`
}

// Role is a role assigned to the signed-in user.
type Role struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	RoleCode    string       `json:"roleCode"`
	RoleGroupID *string      `json:"roleGroupId,omitempty"`
	Permissions []Permission `json:"permissions"`
}

// UserData is the signed-in identity.
type UserData struct {
	ID         string       `json:"id"`
	FullName   string       `json:"fullName"`
	Username   string       `json:"username"`
	Role       string       `json:"role"`
	RoleName   string       `json:"roleName"`
	Avatar     *string      `json:"avatar,omitempty"`
	Email      *string      `json:"email,omitempty"`
	Invalid    bool         `json:"invalid"`
	Ability    []Permission `json:"ability"`
	Roles      []Role       `json:"roles"`
	UserType   *string      `json:"userType,omitempty"`
	CollegeSNs []string     `json:"collegeSns"`
	Token      string       `json:"token,omitempty"`
}

// UserInfo is /api/auth/user-info data. AccessToken and RefreshToken are
// currently NOT_IN_USE; only SESSION authenticates the deployed service.
type UserInfo struct {
	UserData     UserData `json:"userData"`
	AccessToken  string   `json:"accessToken,omitempty"`
	RefreshToken string   `json:"refreshToken,omitempty"`
}

// Entity is the common persisted-record header.
type Entity struct {
	ID         string    `json:"id"`
	CreateTime *DateTime `json:"createTime,omitempty"`
	UpdateTime *DateTime `json:"updateTime,omitempty"`
}

// CheckinSchedule is the effective daily check-in and reporting window.
type CheckinSchedule struct {
	Entity
	ScheduleName                       string `json:"scheduleName"`
	StudentCheckinStartOffsetMinute    int    `json:"studentCheckinStartOffsetMinute"`
	StudentCheckinEndOffsetMinute      int    `json:"studentCheckinEndOffsetMinute"`
	StudentSubmissionStartOffsetMinute *Int64 `json:"studentSubmissionStartOffsetMinute,omitempty"`
	StudentSubmissionEndOffsetMinute   *Int64 `json:"studentSubmissionEndOffsetMinute,omitempty"`
	StudentReportStartOffsetMinute     *Int64 `json:"studentReportStartOffsetMinute,omitempty"`
	StudentReportEndOffsetMinute       *Int64 `json:"studentReportEndOffsetMinute,omitempty"`
	InstructorReportStartOffsetMinute  *Int64 `json:"instructorReportStartOffsetMinute,omitempty"`
	InstructorReportEndOffsetMinute    *Int64 `json:"instructorReportEndOffsetMinute,omitempty"`
	CollegeConfirmStartOffsetMinute    *Int64 `json:"collegeConfirmStartOffsetMinute,omitempty"`
	CollegeConfirmEndOffsetMinute      *Int64 `json:"collegeConfirmEndOffsetMinute,omitempty"`
	Effective                          bool   `json:"effective"`
}

// CheckinLocation is a dormitory check-in point.
type CheckinLocation struct {
	Entity
	LocationName  string    `json:"locationName"`
	Address       *string   `json:"address,omitempty"`
	Longitude     float64   `json:"longitude"`
	Latitude      float64   `json:"latitude"`
	RadiusMeter   float64   `json:"radiusMeter"`
	GroupID       *string   `json:"groupId,omitempty"`
	GroupName     *string   `json:"groupName,omitempty"`
	CreatorSN     *string   `json:"creatorSn,omitempty"`
	CreatorName   *string   `json:"creatorName,omitempty"`
	CreatorPhone  *string   `json:"creatorPhone,omitempty"`
	OperatorSN    *string   `json:"operatorSn,omitempty"`
	OperatorName  *string   `json:"operatorName,omitempty"`
	OperatorPhone *string   `json:"operatorPhone,omitempty"`
	OperationTime *DateTime `json:"operationTime,omitempty"`
}

// CampusFence is a GeoJSON polygon for one campus.
type CampusFence struct {
	Entity
	CampusCode           string `json:"campusCode"`
	CampusName           string `json:"campusName"`
	CoordinateSystemCode string `json:"coordinateSystemCode"`
	FenceGeoJSON         string `json:"fenceGeojson"`
	StatusCode           string `json:"statusCode"`
}

// CaseKeyword is an allowed situation keyword for on- or off-campus reporting.
type CaseKeyword struct {
	Entity
	SituationTypeCode  LocationType `json:"situationTypeCode"`
	Keyword            string       `json:"keyword"`
	RemarkRequired     bool         `json:"remarkRequired"`
	StatusCode         string       `json:"statusCode"`
	SituationTypeValue *string      `json:"situationTypeValue,omitempty"`
	CreatorSN          *string      `json:"creatorSn,omitempty"`
	CreatorName        *string      `json:"creatorName,omitempty"`
	CreatorPhone       *string      `json:"creatorPhone,omitempty"`
}

// StudentDormitory is the synced dormitory assignment.
type StudentDormitory struct {
	Entity
	StudentSN        string  `json:"studentSn"`
	StudentName      *string `json:"studentName,omitempty"`
	SyncBatchID      *string `json:"syncBatchId,omitempty"`
	BuildingID       *string `json:"buildingId,omitempty"`
	RoomNumber       *string `json:"roomNumber,omitempty"`
	SourceCreateDate *Date   `json:"sourceCreateDate,omitempty"`
}

// StudentLocationLog is the server's audited location telemetry.
type StudentLocationLog struct {
	Entity
	CheckinRecordID       string          `json:"checkinRecordId"`
	LocationSceneCode     *string         `json:"locationSceneCode,omitempty"`
	LocationResultCode    *string         `json:"locationResultCode,omitempty"`
	ClientTime            *DateTime       `json:"clientTime,omitempty"`
	ServerReceiveTime     *DateTime       `json:"serverReceiveTime,omitempty"`
	ClientServerSkewMs    *Int64          `json:"clientServerSkewMs,omitempty"`
	Latitude              *float64        `json:"latitude,omitempty"`
	Longitude             *float64        `json:"longitude,omitempty"`
	CoordinateSystemCode  *string         `json:"coordinateSystemCode,omitempty"`
	GPSAccuracyMeter      *float64        `json:"gpsAccuracyMeter,omitempty"`
	AltitudeMeter         *float64        `json:"altitudeMeter,omitempty"`
	AltitudeAccuracyMeter *float64        `json:"altitudeAccuracyMeter,omitempty"`
	HeadingDegree         *float64        `json:"headingDegree,omitempty"`
	SpeedMeterPerSecond   *float64        `json:"speedMeterPerSecond,omitempty"`
	LocationDurationMs    *Int64          `json:"locationDurationMs,omitempty"`
	ReverseGeocode        *string         `json:"reverseGeocode,omitempty"`
	NetworkTypeCode       *string         `json:"networkTypeCode,omitempty"`
	LocationAuthorized    *string         `json:"locationAuthorized,omitempty"`
	LocationAccuracy      *string         `json:"locationAccuracy,omitempty"`
	LocationEnabled       *bool           `json:"locationEnabled,omitempty"`
	ClientIP              *string         `json:"clientIp,omitempty"`
	WechatFlag            *bool           `json:"wechatFlag,omitempty"`
	WechatVersion         *string         `json:"wechatVersion,omitempty"`
	WechatSDKVersion      *string         `json:"wechatSdkVersion,omitempty"`
	DevicePlatformCode    *string         `json:"devicePlatformCode,omitempty"`
	DeviceModel           *string         `json:"deviceModel,omitempty"`
	OSVersion             *string         `json:"osVersion,omitempty"`
	UserAgent             *string         `json:"userAgent,omitempty"`
	DeviceFingerprintHash *string         `json:"deviceFingerprintHash,omitempty"`
	MockLocationFlag      *bool           `json:"mockLocationFlag,omitempty"`
	AntiCheatResultCode   *string         `json:"antiCheatResultCode,omitempty"`
	AntiCheatDetail       json.RawMessage `json:"antiCheatDetail,omitempty"`
	ErrorMessage          *string         `json:"errorMessage,omitempty"`
}

// CheckinRecord is one student's daily check-in record, shared by the student
// history and class-cadre list endpoints.
type CheckinRecord struct {
	Entity
	BusinessDate               Date                `json:"businessDate"`
	StudentSN                  string              `json:"studentSn"`
	StudentName                string              `json:"studentName"`
	RecordStatusCode           RecordStatus        `json:"recordStatusCode"`
	AcademicYear               *string             `json:"academicYear,omitempty"`
	Semester                   *string             `json:"semester,omitempty"`
	LifeHeadURL                *string             `json:"lifeHeadUrl,omitempty"`
	GenderCode                 *string             `json:"genderCode,omitempty"`
	Phone                      *string             `json:"phone,omitempty"`
	CollegeCode                *string             `json:"collegeCode,omitempty"`
	CollegeName                *string             `json:"collegeName,omitempty"`
	MajorCode                  *string             `json:"majorCode,omitempty"`
	MajorName                  *string             `json:"majorName,omitempty"`
	ClassCode                  *string             `json:"classCode,omitempty"`
	ClassName                  *string             `json:"className,omitempty"`
	ClassKey                   *string             `json:"classKey,omitempty"`
	Grade                      *string             `json:"grade,omitempty"`
	CheckinLocationID          *string             `json:"checkinLocationId,omitempty"`
	CheckinLocationName        *string             `json:"checkinLocationName,omitempty"`
	CheckinLocationLatitude    *float64            `json:"checkinLocationLatitude,omitempty"`
	CheckinLocationLongitude   *float64            `json:"checkinLocationLongitude,omitempty"`
	CheckinRadiusMeter         *float64            `json:"checkinRadiusMeter,omitempty"`
	DormitoryLocation          *string             `json:"dormitoryLocation,omitempty"`
	CheckinTime                *DateTime           `json:"checkinTime,omitempty"`
	Latitude                   *float64            `json:"latitude,omitempty"`
	Longitude                  *float64            `json:"longitude,omitempty"`
	ReverseGeocode             *string             `json:"reverseGeocode,omitempty"`
	DistanceMeter              *float64            `json:"distanceMeter,omitempty"`
	ReportedLocationTypeCode   *LocationType       `json:"reportedLocationTypeCode,omitempty"`
	ReportedLocationTypeValue  *string             `json:"reportedLocationTypeValue,omitempty"`
	CaseSourceCode             *string             `json:"caseSourceCode,omitempty"`
	InstructorConfirmedFlag    *bool               `json:"instructorConfirmedFlag,omitempty"`
	AbnormalFlag               *bool               `json:"abnormalFlag,omitempty"`
	AbnormalOriginRecordID     *string             `json:"abnormalOriginRecordId,omitempty"`
	AbnormalReason             *string             `json:"abnormalReason,omitempty"`
	AbnormalTime               *DateTime           `json:"abnormalTime,omitempty"`
	AbnormalOperatorSN         *string             `json:"abnormalOperatorSn,omitempty"`
	AbnormalOperatorName       *string             `json:"abnormalOperatorName,omitempty"`
	AbnormalClosedFlag         *bool               `json:"abnormalClosedFlag,omitempty"`
	AbnormalClosedTime         *DateTime           `json:"abnormalClosedTime,omitempty"`
	AbnormalClosedOperatorSN   *string             `json:"abnormalClosedOperatorSn,omitempty"`
	AbnormalClosedOperatorName *string             `json:"abnormalClosedOperatorName,omitempty"`
	CaseKeywordID              *string             `json:"caseKeywordId,omitempty"`
	CaseKeyword                *string             `json:"caseKeyword,omitempty"`
	CaseRemark                 *string             `json:"caseRemark,omitempty"`
	ReportTime                 *DateTime           `json:"reportTime,omitempty"`
	InstructorList             InstructorList      `json:"instructorList"`
	InstructorSN               *string             `json:"instructorSn,omitempty"`
	InstructorName             *string             `json:"instructorName,omitempty"`
	InstructorPhone            *string             `json:"instructorPhone,omitempty"`
	ExemptionID                *string             `json:"exemptionId,omitempty"`
	StudentTag                 json.RawMessage     `json:"studentTag,omitempty"`
	StudentTags                []json.RawMessage   `json:"studentTags"`
	StudentDormitory           *StudentDormitory   `json:"studentDormitory,omitempty"`
	StudentLocationLog         *StudentLocationLog `json:"studentLocationLog,omitempty"`
	CheckinExemption           json.RawMessage     `json:"checkinExemption,omitempty"`
	MonthlyMissCount           *Int64              `json:"monthlyMissCount,omitempty"`
	LatestAbnormalTrack        json.RawMessage     `json:"latestAbnormalTrack,omitempty"`
	AbnormalTrackCount         *Int64              `json:"abnormalTrackCount,omitempty"`
}

// SituationOption is one attachMore dictionary option.
type SituationOption struct {
	Label    string            `json:"label"`
	Value    LocationType      `json:"value"`
	Ext      json.RawMessage   `json:"ext,omitempty"`
	Children []SituationOption `json:"children,omitempty"`
}

// SituationTypes is the attachMore SITUATION_TYPE metadata.
type SituationTypes struct {
	SituationTypeOptions []SituationOption `json:"situationTypeOptions"`
}

// ManagedClass is a class assigned to a class cadre. Frontend-derived.
type ManagedClass struct {
	ClassKey    string `json:"classKey"`
	ManageClass string `json:"manageClass"`
}

// LocationTelemetry is the optional diagnostic block accepted by writes.
type LocationTelemetry struct {
	ClientTime           DateTime `json:"clientTime"`
	CoordinateSystemCode string   `json:"coordinateSystemCode"`
	UserAgent            string   `json:"userAgent,omitempty"`
	Accuracy             *float64 `json:"accuracy,omitempty"`
	LocationDurationMs   *Int64   `json:"locationDurationMs,omitempty"`
	NetworkTypeCode      string   `json:"networkTypeCode,omitempty"`
	LocationEnabled      *bool    `json:"locationEnabled,omitempty"`
	LocationAuthorized   string   `json:"locationAuthorized,omitempty"`
	LocationAccuracy     string   `json:"locationAccuracy,omitempty"`
	ErrorMessage         string   `json:"errorMessage,omitempty"`
}

// CheckinRequest is the student check-in write body. It requires an official
// mini-program operation ticket in addition to SESSION.
type CheckinRequest struct {
	LocationTelemetry
	Latitude       Latitude  `json:"latitude"`
	Longitude      Longitude `json:"longitude"`
	ReverseGeocode string    `json:"reverseGeocode"`
}

// LocationLogRequest is a location diagnostic/failure write body.
type LocationLogRequest struct {
	LocationTelemetry
}

// ReportRequest is the student situation-report write body.
type ReportRequest struct {
	ReportedLocationTypeCode LocationType `json:"reportedLocationTypeCode"`
	CaseKeywordID            string       `json:"caseKeywordId"`
	Latitude                 Latitude     `json:"latitude"`
	Longitude                Longitude    `json:"longitude"`
	ReverseGeocode           string       `json:"reverseGeocode"`
	CoordinateSystemCode     string       `json:"coordinateSystemCode"`
	GPSAccuracyMeter         *float64     `json:"gpsAccuracyMeter,omitempty"`
	CaseKeyword              string       `json:"caseKeyword,omitempty"`
	CaseRemark               string       `json:"caseRemark"`
}

// HistoryQuery filters the student's own paginated record history.
type HistoryQuery struct {
	Year      int
	Month     int
	PageIndex int
	PageSize  int
	ClockIn   *bool
}

// ClassCheckinQuery filters a class cadre's class record list.
type ClassCheckinQuery struct {
	BusinessDate Date
	ClassKey     string
	Keyword      string
	PageIndex    int
	PageSize     int
}

// ClassSituationReport is one entry of the class-cadre batch report body.
type ClassSituationReport struct {
	ClassKey         string       `json:"classKey"`
	RecordID         string       `json:"recordId"`
	LocationTypeCode LocationType `json:"locationTypeCode"`
	CaseKeywordID    string       `json:"caseKeywordId"`
	CaseRemark       string       `json:"caseRemark"`
}
