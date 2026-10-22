package moonuestc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// ClassCadreService exposes class-cadre endpoints. Access requires
// userData.userType == "CLASS_CADRE"; the server enforces the class scope.
// These contracts are frontend-derived and were not verified with a class-cadre
// account.
type ClassCadreService struct{ client *Client }

// MyClasses returns the classes managed by the signed-in class cadre.
func (s *ClassCadreService) MyClasses(ctx context.Context) (*Response[[]ManagedClass], error) {
	return request[[]ManagedClass](
		s.client, ctx, http.MethodGet, "/api/class-cadre/my-classes", nil, nil, false,
	)
}

// CheckinList returns a class's paginated check-in records.
func (s *ClassCadreService) CheckinList(ctx context.Context, query ClassCheckinQuery) (*Response[[]CheckinRecord], error) {
	query, err := query.prepare()
	if err != nil {
		return nil, err
	}
	values := url.Values{
		"businessDate": {query.BusinessDate.String()},
		"classKey":     {query.ClassKey},
		"keyword":      {query.Keyword},
		"pageIndex":    {strconv.Itoa(query.PageIndex)},
		"pageSize":     {strconv.Itoa(query.PageSize)},
	}
	response, err := request[[]CheckinRecord](
		s.client, ctx, http.MethodGet, "/api/class-cadre/checkin-list", values, nil, false,
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

// CheckinCount returns the class check-in count for a business date.
func (s *ClassCadreService) CheckinCount(ctx context.Context, businessDate Date) (*Response[*Int64], error) {
	if businessDate.IsZero() {
		return nil, &ValidationError{Field: "businessDate", Message: "must not be the zero value"}
	}
	response, err := request[*Int64](
		s.client, ctx, http.MethodGet, "/api/class-cadre/checkin-count",
		url.Values{"businessDate": {businessDate.String()}}, nil, false,
	)
	if err != nil {
		return nil, err
	}
	if response.Data == nil {
		return nil, protocolf("class check-in count data is missing")
	}
	if *response.Data < 0 {
		return nil, protocolf("class check-in count is negative")
	}
	return response, nil
}

// ReportSituations submits the class cadre's batch situation report. The body
// is an array, not an object wrapping a records field.
func (s *ClassCadreService) ReportSituations(ctx context.Context, reports []ClassSituationReport) (*Response[json.RawMessage], error) {
	for i := range reports {
		if err := reports[i].prepare(); err != nil {
			return nil, err
		}
	}
	if reports == nil {
		reports = []ClassSituationReport{}
	}
	return request[json.RawMessage](
		s.client, ctx, http.MethodPost, "/api/class-cadre/situation/report", nil, reports, false,
	)
}
