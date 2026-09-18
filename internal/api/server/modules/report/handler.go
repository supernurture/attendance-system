package report

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oapi-codegen/runtime/types"

	"attendance-system/internal/api/server/modules/attendance"
	"attendance-system/internal/api/server/modules/schedule"
	reportcontract "attendance-system/internal/api/server/oapicodegen/report"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

var _ reportcontract.StrictServerInterface = (*Handler)(nil)

func (h *Handler) GetAttendanceReport(
	ctx context.Context, req reportcontract.GetAttendanceReportRequestObject,
) (reportcontract.GetAttendanceReportResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	p := req.Params
	report, err := h.svc.Attendance(ctx, claims, query(p.From, p.To, p.UserId, p.DepartmentId, p.Status))
	switch {
	case errors.Is(err, apperr.ErrForbidden):
		return reportcontract.GetAttendanceReport403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case apperr.IsValidation(err):
		return reportcontract.GetAttendanceReport400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case err != nil:
		return nil, err
	}
	return reportcontract.GetAttendanceReport200JSONResponse(reportResponse(report)), nil
}

func (h *Handler) GetAttendanceReportCsv(
	ctx context.Context, req reportcontract.GetAttendanceReportCsvRequestObject,
) (reportcontract.GetAttendanceReportCsvResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	p := req.Params
	body, err := h.svc.CSV(ctx, claims, query(p.From, p.To, p.UserId, p.DepartmentId, p.Status))
	switch {
	case errors.Is(err, apperr.ErrForbidden):
		return reportcontract.GetAttendanceReportCsv403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case apperr.IsValidation(err):
		return reportcontract.GetAttendanceReportCsv400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case err != nil:
		return nil, err
	}
	return reportcontract.GetAttendanceReportCsv200TextcsvResponse{
		Body: bytes.NewReader(body), ContentLength: int64(len(body)),
		Headers: reportcontract.GetAttendanceReportCsv200ResponseHeaders{
			ContentDisposition: disposition(p.From, p.To, "csv"),
		},
	}, nil
}

func (h *Handler) GetAttendanceReportPdf(
	ctx context.Context, req reportcontract.GetAttendanceReportPdfRequestObject,
) (reportcontract.GetAttendanceReportPdfResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	p := req.Params
	body, err := h.svc.PDF(ctx, claims, query(p.From, p.To, p.UserId, p.DepartmentId, p.Status))
	switch {
	case errors.Is(err, apperr.ErrForbidden):
		return reportcontract.GetAttendanceReportPdf403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case apperr.IsValidation(err):
		return reportcontract.GetAttendanceReportPdf400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case err != nil:
		return nil, err
	}
	return reportcontract.GetAttendanceReportPdf200ApplicationpdfResponse{
		Body: bytes.NewReader(body), ContentLength: int64(len(body)),
		Headers: reportcontract.GetAttendanceReportPdf200ResponseHeaders{
			ContentDisposition: disposition(p.From, p.To, "pdf"),
		},
	}, nil
}

// caller unwraps the pooled *gin.Context and reads who is asking; the routes sit behind middleware.Auth.
func caller(ctx context.Context) (context.Context, middleware.Claims, error) {
	ctx = middleware.RequestContext(ctx)
	claims, ok := middleware.ClaimsFrom(ctx)
	if !ok {
		return ctx, claims, errors.New("report route reached without middleware.Auth")
	}
	return ctx, claims, nil
}

func query(from, to types.Date, userID, departmentID *int64, status *reportcontract.Status) Query {
	q := Query{
		Span:   schedule.Range{From: from.Time, To: to.Time},
		Filter: attendance.Filter{UserID: userID, DepartmentID: departmentID},
	}
	if status != nil {
		picked := attendance.Status(*status)
		q.Status = &picked
	}
	return q
}

func disposition(from, to types.Date, extension string) string {
	return fmt.Sprintf(`attachment; filename="attendance_%s_%s.%s"`,
		from.Format(time.DateOnly), to.Format(time.DateOnly), extension)
}

func badRequest(err error) reportcontract.BadRequestJSONResponse {
	return reportcontract.BadRequestJSONResponse{Message: err.Error()}
}

func forbidden(err error) reportcontract.ForbiddenJSONResponse {
	return reportcontract.ForbiddenJSONResponse{Message: err.Error()}
}

func reportResponse(report Report) reportcontract.AttendanceReport {
	response := reportcontract.AttendanceReport{
		From:   types.Date{Time: report.Span.From},
		To:     types.Date{Time: report.Span.To},
		Days:   make([]reportcontract.ReportDay, 0, len(report.Days)),
		People: make([]reportcontract.ReportTotal, 0, len(report.People)),
	}
	for _, day := range report.Days {
		entry := reportcontract.ReportDay{
			UserId: day.UserID, FullName: day.FullName, DepartmentId: day.DepartmentID,
			WorkDate: types.Date{Time: day.Date}, Status: reportcontract.Status(day.Status),
			WorkedMinutes: worked(day),
		}
		if held := day.Attendance; held != nil {
			entry.CheckInAt, entry.CheckOutAt = &held.CheckInAt, held.CheckOutAt
			entry.LateMinutes, entry.EarlyLeaveMinutes = held.LateMinutes, held.EarlyLeaveMinutes
		}
		response.Days = append(response.Days, entry)
	}
	for _, total := range report.People {
		response.People = append(response.People, reportcontract.ReportTotal{
			UserId: total.UserID, FullName: total.FullName, DepartmentId: total.DepartmentID,
			Present: total.Present, Late: total.Late, OnLeave: total.OnLeave, Absent: total.Absent,
			WorkedMinutes: total.WorkedMinutes,
		})
	}
	return response
}
