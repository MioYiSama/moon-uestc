package moonuestc

import (
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// StudentService exposes the signed-in student's own endpoints.
type StudentService struct{ client *Client }

// EffectiveSchedule returns the active check-in window, or nil if none is active.
func (s *StudentService) EffectiveSchedule(ctx context.Context) (*Response[*CheckinSchedule], error) {
	response, err := request[*CheckinSchedule](
		s.client, ctx, http.MethodGet, "/api/checkin-schedule/effective", nil, nil, false,
	)
	if err != nil {
		return nil, err
	}
	if response.Data != nil {
		if err := validateSchedule(response.Data); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// AvailableLocations returns the dormitory check-in points available to the student.
func (s *StudentService) AvailableLocations(ctx context.Context) (*Response[[]CheckinLocation], error) {
	response, err := request[[]CheckinLocation](
		s.client, ctx, http.MethodGet, "/api/checkin-location/available-locations", nil, nil, false,
	)
	if err != nil {
		return nil, err
	}
	for i := range response.Data {
		if err := validateLocation(&response.Data[i]); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// CampusFences returns the campus GeoJSON fences visible to the student.
func (s *StudentService) CampusFences(ctx context.Context) (*Response[[]CampusFence], error) {
	response, err := request[[]CampusFence](
		s.client, ctx, http.MethodGet, "/api/campus-fence/current-student", nil, nil, false,
	)
	if err != nil {
		return nil, err
	}
	for i := range response.Data {
		if err := validateFence(&response.Data[i]); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// Today returns today's record, or nil when the service has no record yet.
func (s *StudentService) Today(ctx context.Context) (*Response[*CheckinRecord], error) {
	response, err := request[*CheckinRecord](
		s.client, ctx, http.MethodGet, "/api/student-checkin-record/today", nil, nil, false,
	)
	if err != nil {
		return nil, err
	}
	if response.Data != nil {
		if err := validateRecord(response.Data); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// History returns the student's own paginated records. ClockIn nil means all.
func (s *StudentService) History(ctx context.Context, query HistoryQuery) (*Response[[]CheckinRecord], error) {
	query, err := query.prepare()
	if err != nil {
		return nil, err
	}
	values := url.Values{
		"year":      {strconv.Itoa(query.Year)},
		"month":     {strconv.Itoa(query.Month)},
		"pageIndex": {strconv.Itoa(query.PageIndex)},
		"pageSize":  {strconv.Itoa(query.PageSize)},
		"clockIn":   {""},
	}
	if query.ClockIn != nil {
		values.Set("clockIn", strconv.FormatBool(*query.ClockIn))
	}
	response, err := request[[]CheckinRecord](
		s.client, ctx, http.MethodGet, "/api/student-checkin-record/list", values, nil, false,
	)
	if err != nil {
		return nil, err
	}
	for i := range response.Data {
		if err := validateRecord(&response.Data[i]); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// Checkin submits a dormitory check-in. It requires an official mini-program
// operation ticket and is never retried by the SDK.
func (s *StudentService) Checkin(ctx context.Context, checkin CheckinRequest) (*Response[json.RawMessage], error) {
	if err := checkin.prepare(); err != nil {
		return nil, err
	}
	return request[json.RawMessage](
		s.client, ctx, http.MethodPost, "/api/student-checkin-record/checkin", nil, checkin, true,
	)
}

// Report submits an on- or off-campus situation report. It requires an official
// mini-program operation ticket and is never retried by the SDK.
func (s *StudentService) Report(ctx context.Context, report ReportRequest) (*Response[json.RawMessage], error) {
	if err := report.prepare(); err != nil {
		return nil, err
	}
	return request[json.RawMessage](
		s.client, ctx, http.MethodPost, "/api/student-checkin-record/report", nil, report, true,
	)
}

// LogLocation uploads location diagnostics. The SDK never calls it implicitly.
func (s *StudentService) LogLocation(ctx context.Context, log LocationLogRequest) (*Response[json.RawMessage], error) {
	if err := log.prepare(); err != nil {
		return nil, err
	}
	return request[json.RawMessage](
		s.client, ctx, http.MethodPost, "/api/student-checkin-record/location-log", nil, log, false,
	)
}

// CaseKeywords returns the allowed situation keywords for one location type.
func (s *StudentService) CaseKeywords(ctx context.Context, locationType LocationType) (*Response[[]CaseKeyword], error) {
	if !locationType.Valid() {
		return nil, &ValidationError{Field: "locationType", Message: "must be CAMPUS or OUTSIDE"}
	}
	response, err := request[[]CaseKeyword](
		s.client, ctx, http.MethodGet, "/api/case-keyword/active",
		url.Values{"locationTypeCode": {string(locationType)}}, nil, false,
	)
	if err != nil {
		return nil, err
	}
	for i := range response.Data {
		if err := validateKeyword(&response.Data[i]); err != nil {
			return nil, err
		}
	}
	return response, nil
}

// ReverseGeocode converts GCJ-02 coordinates to a human-readable address.
func (s *StudentService) ReverseGeocode(ctx context.Context, latitude Latitude, longitude Longitude) (*Response[*string], error) {
	if err := validateLatitude(latitude); err != nil {
		return nil, err
	}
	if err := validateLongitude(longitude); err != nil {
		return nil, err
	}
	values := url.Values{
		"lat": {strconv.FormatFloat(float64(latitude), 'f', -1, 64)},
		"lng": {strconv.FormatFloat(float64(longitude), 'f', -1, 64)},
	}
	response, err := request[*string](s.client, ctx, http.MethodGet, "/api/map/reverse", values, nil, false)
	if err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, protocolf("reverse geocode data is missing")
	}
	return response, nil
}

type situationTarget struct {
	Target string `json:"target"`
}

// SituationTypes returns the on-/off-campus dictionary. The endpoint uses POST
// but performs a read.
func (s *StudentService) SituationTypes(ctx context.Context) (*Response[*SituationTypes], error) {
	body := []situationTarget{{Target: "SITUATION_TYPE"}}
	response, err := request[*SituationTypes](
		s.client, ctx, http.MethodPost, "/api/common/attachMore", nil, body, false,
	)
	if err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, protocolf("situation type data is missing")
	}
	return response, nil
}

// DownloadAvatar fetches the FastDFS image at a record's lifeHeadUrl. The
// deployed server may omit Content-Type, so successful non-JSON/non-HTML
// responses are returned as bytes.
func (s *StudentService) DownloadAvatar(ctx context.Context, filePath string) ([]byte, error) {
	response, err := s.client.sendFromOrigin(
		ctx, http.MethodGet, "/fastdfs-util/api/fastdfs/download/image",
		url.Values{"filePath": {filePath}}, nil, false,
	)
	if err != nil {
		return nil, err
	}
	mediaType := ""
	if raw := response.header.Get("Content-Type"); raw != "" {
		mediaType, _, _ = mime.ParseMediaType(raw)
	}
	mediaType = strings.ToLower(mediaType)
	if response.status >= 200 && response.status < 300 &&
		mediaType != "application/json" && mediaType != "text/html" {
		return response.body, nil
	}
	if _, err := s.client.validateEnvelope(response.status, response.body); err != nil {
		return nil, err
	}
	return nil, &ProtocolError{Message: "avatar response was not an image"}
}
