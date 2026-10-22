package moonuestc

import (
	"fmt"
	"math"
)

func protocolf(format string, args ...any) error {
	return &ProtocolError{Message: fmt.Sprintf(format, args...)}
}

func validateLatitude(value Latitude) error {
	number := float64(value)
	if math.IsNaN(number) || math.IsInf(number, 0) || number < -90 || number > 90 {
		return &ValidationError{Field: "latitude", Message: "must be finite and within [-90, 90]"}
	}
	return nil
}

func validateLongitude(value Longitude) error {
	number := float64(value)
	if math.IsNaN(number) || math.IsInf(number, 0) || number < -180 || number > 180 {
		return &ValidationError{Field: "longitude", Message: "must be finite and within [-180, 180]"}
	}
	return nil
}

func normalizeCoordinateSystem(value *string) error {
	switch *value {
	case "":
		*value = CoordinateSystemGCJ02
		return nil
	case CoordinateSystemGCJ02:
		return nil
	default:
		return &ValidationError{Field: "coordinateSystemCode", Message: `only "gcj02" is supported`}
	}
}

func validateAccuracy(field string, value *float64) error {
	if value == nil {
		return nil
	}
	if math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0 {
		return &ValidationError{Field: field, Message: "must be finite and non-negative"}
	}
	return nil
}

func validateDuration(field string, value *Int64) error {
	if value != nil && *value < 0 {
		return &ValidationError{Field: field, Message: "must be non-negative"}
	}
	return nil
}

func (r *LocationTelemetry) prepareDiagnostics() error {
	if r.ClientTime.IsZero() {
		return &ValidationError{Field: "clientTime", Message: "must not be the zero value"}
	}
	if err := validateAccuracy("accuracy", r.Accuracy); err != nil {
		return err
	}
	if err := validateDuration("locationDurationMs", r.LocationDurationMs); err != nil {
		return err
	}
	return normalizeCoordinateSystem(&r.CoordinateSystemCode)
}

func (r *CheckinRequest) prepare() error {
	if err := r.prepareDiagnostics(); err != nil {
		return err
	}
	if err := validateLatitude(r.Latitude); err != nil {
		return err
	}
	return validateLongitude(r.Longitude)
}

// Validate checks the request without sending it.
func (r CheckinRequest) Validate() error {
	return r.prepare()
}

func (r *LocationLogRequest) prepare() error {
	return r.prepareDiagnostics()
}

// Validate checks the request without sending it.
func (r LocationLogRequest) Validate() error {
	return r.prepare()
}

func (r *ReportRequest) prepare() error {
	if !r.ReportedLocationTypeCode.Valid() {
		return &ValidationError{Field: "reportedLocationTypeCode", Message: "must be CAMPUS or OUTSIDE"}
	}
	if err := validateLatitude(r.Latitude); err != nil {
		return err
	}
	if err := validateLongitude(r.Longitude); err != nil {
		return err
	}
	if err := validateAccuracy("gpsAccuracyMeter", r.GPSAccuracyMeter); err != nil {
		return err
	}
	return normalizeCoordinateSystem(&r.CoordinateSystemCode)
}

// Validate checks the request without sending it.
func (r ReportRequest) Validate() error {
	return r.prepare()
}

func (q HistoryQuery) prepare() (HistoryQuery, error) {
	if q.Year < 1 || q.Year > 9999 {
		return q, &ValidationError{Field: "year", Message: "must be within [1, 9999]"}
	}
	if q.Month < 1 || q.Month > 12 {
		return q, &ValidationError{Field: "month", Message: "must be within [1, 12]"}
	}
	if q.PageIndex < 0 {
		return q, &ValidationError{Field: "pageIndex", Message: "must be positive"}
	}
	if q.PageSize < 0 {
		return q, &ValidationError{Field: "pageSize", Message: "must be positive"}
	}
	if q.PageIndex == 0 {
		q.PageIndex = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 10
	}
	return q, nil
}

// Validate checks the query and applies page defaults without sending it.
func (q HistoryQuery) Validate() error {
	_, err := q.prepare()
	return err
}

func (q ClassCheckinQuery) prepare() (ClassCheckinQuery, error) {
	if q.BusinessDate.IsZero() {
		return q, &ValidationError{Field: "businessDate", Message: "must not be the zero value"}
	}
	if q.PageIndex < 0 {
		return q, &ValidationError{Field: "pageIndex", Message: "must be positive"}
	}
	if q.PageSize < 0 {
		return q, &ValidationError{Field: "pageSize", Message: "must be positive"}
	}
	if q.PageIndex == 0 {
		q.PageIndex = 1
	}
	if q.PageSize == 0 {
		q.PageSize = 10
	}
	return q, nil
}

// Validate checks the query and applies page defaults without sending it.
func (q ClassCheckinQuery) Validate() error {
	_, err := q.prepare()
	return err
}

func (r *ClassSituationReport) prepare() error {
	if !r.LocationTypeCode.Valid() {
		return &ValidationError{Field: "locationTypeCode", Message: "must be CAMPUS or OUTSIDE"}
	}
	return nil
}

// Validate checks the report without sending it.
func (r ClassSituationReport) Validate() error {
	return r.prepare()
}

func validateUserInfo(info *UserInfo) error {
	if info.UserData.ID == "" {
		return protocolf("userInfo.userData.id is missing")
	}
	return nil
}

func validateRecord(record *CheckinRecord) error {
	switch {
	case record.ID == "":
		return protocolf("check-in record id is missing")
	case record.BusinessDate.IsZero():
		return protocolf("check-in record %s businessDate is missing", record.ID)
	case record.StudentSN == "":
		return protocolf("check-in record %s studentSn is missing", record.ID)
	case record.StudentName == "":
		return protocolf("check-in record %s studentName is missing", record.ID)
	case record.RecordStatusCode == "":
		return protocolf("check-in record %s recordStatusCode is missing", record.ID)
	default:
		return nil
	}
}

func validateSchedule(schedule *CheckinSchedule) error {
	if schedule.ID == "" || schedule.ScheduleName == "" {
		return protocolf("check-in schedule id or scheduleName is missing")
	}
	return nil
}

func validateLocation(location *CheckinLocation) error {
	if location.ID == "" || location.LocationName == "" {
		return protocolf("check-in location id or locationName is missing")
	}
	return nil
}

func validateFence(fence *CampusFence) error {
	if fence.ID == "" || fence.CampusCode == "" || fence.FenceGeoJSON == "" {
		return protocolf("campus fence id, campusCode or fenceGeojson is missing")
	}
	return nil
}

func validateKeyword(keyword *CaseKeyword) error {
	if keyword.ID == "" || keyword.Keyword == "" {
		return protocolf("case keyword id or keyword is missing")
	}
	if !keyword.SituationTypeCode.Valid() {
		return protocolf("case keyword %s has unknown situationTypeCode %q", keyword.ID, keyword.SituationTypeCode)
	}
	return nil
}
