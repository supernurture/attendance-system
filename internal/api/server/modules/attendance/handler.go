package attendance

import (
	"context"
	"errors"
	"net/http"

	"github.com/oapi-codegen/runtime/types"

	"attendance-system/internal/api/server/modules/schedule"
	attendancecontract "attendance-system/internal/api/server/oapicodegen/attendance"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

var _ attendancecontract.StrictServerInterface = (*Handler)(nil)

func (h *Handler) CheckIn(
	ctx context.Context, req attendancecontract.CheckInRequestObject,
) (attendancecontract.CheckInResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	row, err := h.svc.CheckIn(ctx, claims.UserID, mark(*req.Body))
	switch status(err) {
	case http.StatusBadRequest:
		return attendancecontract.CheckIn400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return attendancecontract.CheckIn403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return attendancecontract.CheckIn404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return attendancecontract.CheckIn409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return attendancecontract.CheckIn201JSONResponse(attendanceResponse(row)), nil
}

func (h *Handler) CheckOut(
	ctx context.Context, req attendancecontract.CheckOutRequestObject,
) (attendancecontract.CheckOutResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	row, err := h.svc.CheckOut(ctx, claims.UserID, mark(*req.Body))
	switch status(err) {
	case http.StatusBadRequest:
		return attendancecontract.CheckOut400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return attendancecontract.CheckOut403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return attendancecontract.CheckOut200JSONResponse(attendanceResponse(row)), nil
}

func (h *Handler) ListMyAttendance(
	ctx context.Context, req attendancecontract.ListMyAttendanceRequestObject,
) (attendancecontract.ListMyAttendanceResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := h.svc.Mine(ctx, claims.UserID, schedule.Range{From: req.Params.From.Time, To: req.Params.To.Time})
	if status(err) == http.StatusBadRequest {
		return attendancecontract.ListMyAttendance400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]attendancecontract.Attendance, 0, len(rows))
	for _, row := range rows {
		listed = append(listed, attendanceResponse(row))
	}
	return attendancecontract.ListMyAttendance200JSONResponse(listed), nil
}

func (h *Handler) SaveDailyReport(
	ctx context.Context, req attendancecontract.SaveDailyReportRequestObject,
) (attendancecontract.SaveDailyReportResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	row, err := h.svc.SaveDailyReport(ctx, claims.UserID, req.Body.WorkDate.Time, req.Body.Content)
	switch status(err) {
	case http.StatusBadRequest:
		return attendancecontract.SaveDailyReport400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusNotFound:
		return attendancecontract.SaveDailyReport404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return attendancecontract.SaveDailyReport200JSONResponse(attendanceResponse(row)), nil
}

func (h *Handler) GetAttendancePhoto(
	ctx context.Context, req attendancecontract.GetAttendancePhotoRequestObject,
) (attendancecontract.GetAttendancePhotoResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	url, err := h.svc.PhotoURL(ctx, claims, req.Id, Side(req.Side))
	switch status(err) {
	case http.StatusBadRequest:
		return attendancecontract.GetAttendancePhoto400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return attendancecontract.GetAttendancePhoto403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return attendancecontract.GetAttendancePhoto404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return attendancecontract.GetAttendancePhoto302Response{
		Headers: attendancecontract.GetAttendancePhoto302ResponseHeaders{Location: url},
	}, nil
}

func (h *Handler) GetWhosIn(
	ctx context.Context, req attendancecontract.GetWhosInRequestObject,
) (attendancecontract.GetWhosInResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	presences, err := h.svc.WhosIn(ctx, claims, req.Params.Date.Time)
	if status(err) == http.StatusForbidden {
		return attendancecontract.GetWhosIn403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]attendancecontract.Presence, 0, len(presences))
	for _, presence := range presences {
		listed = append(listed, presenceResponse(presence))
	}
	return attendancecontract.GetWhosIn200JSONResponse(listed), nil
}

func (h *Handler) RequestCorrection(
	ctx context.Context, req attendancecontract.RequestCorrectionRequestObject,
) (attendancecontract.RequestCorrectionResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	correction, err := h.svc.RequestCorrection(ctx, claims, Proposal{
		UserID:     req.Body.UserId,
		WorkDate:   req.Body.WorkDate.Time,
		CheckInAt:  req.Body.ProposedCheckInAt,
		CheckOutAt: req.Body.ProposedCheckOutAt,
		Reason:     req.Body.Reason,
	})
	switch status(err) {
	case http.StatusBadRequest:
		return attendancecontract.RequestCorrection400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return attendancecontract.RequestCorrection403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusConflict:
		return attendancecontract.RequestCorrection409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return attendancecontract.RequestCorrection201JSONResponse(correctionResponse(correction)), nil
}

func (h *Handler) ListMyCorrections(
	ctx context.Context, _ attendancecontract.ListMyCorrectionsRequestObject,
) (attendancecontract.ListMyCorrectionsResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	corrections, err := h.svc.MyCorrections(ctx, claims.UserID)
	if err != nil {
		return nil, err
	}
	return attendancecontract.ListMyCorrections200JSONResponse(correctionsResponse(corrections)), nil
}

func (h *Handler) ListPendingCorrections(
	ctx context.Context, _ attendancecontract.ListPendingCorrectionsRequestObject,
) (attendancecontract.ListPendingCorrectionsResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	corrections, err := h.svc.PendingCorrections(ctx, claims)
	if status(err) == http.StatusForbidden {
		return attendancecontract.ListPendingCorrections403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return attendancecontract.ListPendingCorrections200JSONResponse(correctionsResponse(corrections)), nil
}

func (h *Handler) CancelCorrection(
	ctx context.Context, req attendancecontract.CancelCorrectionRequestObject,
) (attendancecontract.CancelCorrectionResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.CancelCorrection(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return attendancecontract.CancelCorrection403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return attendancecontract.CancelCorrection404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return attendancecontract.CancelCorrection409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return attendancecontract.CancelCorrection204Response{}, nil
}

func (h *Handler) DecideCorrection(
	ctx context.Context, req attendancecontract.DecideCorrectionRequestObject,
) (attendancecontract.DecideCorrectionResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	correction, err := h.svc.DecideCorrection(ctx, claims, req.Id, Decision(req.Body.Decision), text(req.Body.Note))
	switch status(err) {
	case http.StatusBadRequest:
		return attendancecontract.DecideCorrection400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return attendancecontract.DecideCorrection403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return attendancecontract.DecideCorrection404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return attendancecontract.DecideCorrection409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return attendancecontract.DecideCorrection200JSONResponse(correctionResponse(correction)), nil
}

// caller unwraps the pooled *gin.Context and reads who is asking; the routes sit behind middleware.Auth.
func caller(ctx context.Context) (context.Context, middleware.Claims, error) {
	ctx = middleware.RequestContext(ctx)
	claims, ok := middleware.ClaimsFrom(ctx)
	if !ok {
		return ctx, claims, errors.New("attendance route reached without middleware.Auth")
	}
	return ctx, claims, nil
}

// status is the code the client should see, or 0 when the error is not theirs to fix.
func status(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, apperr.ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, apperr.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, apperr.ErrConflict):
		return http.StatusConflict
	case apperr.IsValidation(err):
		return http.StatusBadRequest
	}
	return 0
}

func badRequest(err error) attendancecontract.BadRequestJSONResponse {
	return attendancecontract.BadRequestJSONResponse{Message: err.Error()}
}

func forbidden(err error) attendancecontract.ForbiddenJSONResponse {
	return attendancecontract.ForbiddenJSONResponse{Message: err.Error()}
}

func notFound(err error) attendancecontract.NotFoundJSONResponse {
	return attendancecontract.NotFoundJSONResponse{Message: err.Error()}
}

func conflict(err error) attendancecontract.ConflictJSONResponse {
	return attendancecontract.ConflictJSONResponse{Message: err.Error()}
}

func mark(body attendancecontract.MarkRequest) Mark {
	return Mark{
		PhotoKey: body.PhotoKey, Lat: body.Lat, Lng: body.Lng,
		AccuracyM: body.AccuracyM, MockLocation: body.MockLocation,
	}
}

func attendanceResponse(row Attendance) attendancecontract.Attendance {
	return attendancecontract.Attendance{
		Id:                   row.ID,
		UserId:               row.UserID,
		WorkDate:             types.Date{Time: row.WorkDate},
		ScheduleId:           row.ScheduleID,
		CheckInAt:            row.CheckInAt,
		CheckOutAt:           row.CheckOutAt,
		CheckIn:              evidenceResponse(row.CheckIn),
		CheckOut:             evidenceResponse(row.CheckOut),
		LateMinutes:          row.LateMinutes,
		EarlyLeaveMinutes:    row.EarlyLeaveMinutes,
		DailyReport:          row.DailyReport,
		DailyReportUpdatedAt: row.DailyReportUpdatedAt,
	}
}

// evidenceResponse is nil for a side with no evidence; the columns' CHECK keeps a side all there or all missing.
func evidenceResponse(evidence Evidence) *attendancecontract.Evidence {
	if evidence.PhotoKey == nil {
		return nil
	}
	return &attendancecontract.Evidence{
		Lat:              *evidence.Lat,
		Lng:              *evidence.Lng,
		AccuracyM:        *evidence.AccuracyM,
		OfficeLocationId: evidence.LocationID,
		WithinGeofence:   *evidence.WithinGeofence,
		MockLocation:     *evidence.MockLocation,
	}
}

func presenceResponse(presence Presence) attendancecontract.Presence {
	response := attendancecontract.Presence{
		UserId:       presence.UserID,
		FullName:     presence.FullName,
		DepartmentId: presence.DepartmentID,
		Status:       attendancecontract.PresenceStatus(presence.Status),
	}
	if presence.Attendance != nil {
		row := attendanceResponse(*presence.Attendance)
		response.Attendance = &row
	}
	return response
}

func correctionsResponse(corrections []Correction) []attendancecontract.Correction {
	listed := make([]attendancecontract.Correction, 0, len(corrections))
	for _, correction := range corrections {
		listed = append(listed, correctionResponse(correction))
	}
	return listed
}

func correctionResponse(correction Correction) attendancecontract.Correction {
	return attendancecontract.Correction{
		Id:                 correction.ID,
		UserId:             correction.UserID,
		WorkDate:           types.Date{Time: correction.WorkDate},
		RequestedBy:        correction.RequestedBy,
		ProposedCheckInAt:  correction.ProposedCheckInAt,
		ProposedCheckOutAt: correction.ProposedCheckOutAt,
		OldCheckInAt:       correction.OldCheckInAt,
		OldCheckOutAt:      correction.OldCheckOutAt,
		Reason:             correction.Reason,
		Status:             attendancecontract.CorrectionStatus(correction.Status),
		ReviewedBy:         correction.ReviewedBy,
		ReviewedAt:         correction.ReviewedAt,
		ReviewNote:         correction.ReviewNote,
		CreatedAt:          correction.CreatedAt,
	}
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
