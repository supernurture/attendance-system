package schedule

import (
	"context"
	"errors"
	"net/http"

	"github.com/oapi-codegen/runtime/types"

	schedulecontract "attendance-system/internal/api/server/oapicodegen/schedule"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

var _ schedulecontract.StrictServerInterface = (*Handler)(nil)

func (h *Handler) GetMySchedule(
	ctx context.Context, req schedulecontract.GetMyScheduleRequestObject,
) (schedulecontract.GetMyScheduleResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	days, err := h.svc.Days(ctx, claims.UserID, span(req.Params.From, req.Params.To))
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.GetMySchedule400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusNotFound:
		return schedulecontract.GetMySchedule404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]schedulecontract.ScheduledDay, 0, len(days))
	for _, day := range days {
		listed = append(listed, dayResponse(day))
	}
	return schedulecontract.GetMySchedule200JSONResponse(listed), nil
}

func (h *Handler) ListWorkSchedules(
	ctx context.Context, _ schedulecontract.ListWorkSchedulesRequestObject,
) (schedulecontract.ListWorkSchedulesResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	schedules, err := h.svc.ListSchedules(ctx, claims)
	if status(err) == http.StatusForbidden {
		return schedulecontract.ListWorkSchedules403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]schedulecontract.WorkSchedule, 0, len(schedules))
	for _, schedule := range schedules {
		listed = append(listed, scheduleResponse(schedule))
	}
	return schedulecontract.ListWorkSchedules200JSONResponse(listed), nil
}

func (h *Handler) CreateWorkSchedule(
	ctx context.Context, req schedulecontract.CreateWorkScheduleRequestObject,
) (schedulecontract.CreateWorkScheduleResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	schedule, err := h.svc.CreateSchedule(ctx, claims, hours(*req.Body))
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.CreateWorkSchedule400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.CreateWorkSchedule403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.CreateWorkSchedule201JSONResponse(scheduleResponse(schedule)), nil
}

func (h *Handler) UpdateWorkSchedule(
	ctx context.Context, req schedulecontract.UpdateWorkScheduleRequestObject,
) (schedulecontract.UpdateWorkScheduleResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	schedule, err := h.svc.ReplaceSchedule(ctx, claims, req.Id, hours(*req.Body))
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.UpdateWorkSchedule400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.UpdateWorkSchedule403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return schedulecontract.UpdateWorkSchedule404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.UpdateWorkSchedule200JSONResponse(scheduleResponse(schedule)), nil
}

func (h *Handler) DeleteWorkSchedule(
	ctx context.Context, req schedulecontract.DeleteWorkScheduleRequestObject,
) (schedulecontract.DeleteWorkScheduleResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.DeleteSchedule(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return schedulecontract.DeleteWorkSchedule403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return schedulecontract.DeleteWorkSchedule404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return schedulecontract.DeleteWorkSchedule409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.DeleteWorkSchedule204Response{}, nil
}

func (h *Handler) ListHolidays(
	ctx context.Context, req schedulecontract.ListHolidaysRequestObject,
) (schedulecontract.ListHolidaysResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	holidays, err := h.svc.ListHolidays(ctx, claims, span(req.Params.From, req.Params.To))
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.ListHolidays400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.ListHolidays403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]schedulecontract.Holiday, 0, len(holidays))
	for _, holiday := range holidays {
		listed = append(listed, holidayResponse(holiday))
	}
	return schedulecontract.ListHolidays200JSONResponse(listed), nil
}

func (h *Handler) CreateHoliday(
	ctx context.Context, req schedulecontract.CreateHolidayRequestObject,
) (schedulecontract.CreateHolidayResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	holiday, err := h.svc.CreateHoliday(ctx, claims, req.Body.Date.Time, req.Body.Name)
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.CreateHoliday400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.CreateHoliday403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusConflict:
		return schedulecontract.CreateHoliday409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.CreateHoliday201JSONResponse(holidayResponse(holiday)), nil
}

func (h *Handler) UpdateHoliday(
	ctx context.Context, req schedulecontract.UpdateHolidayRequestObject,
) (schedulecontract.UpdateHolidayResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	holiday, err := h.svc.ReplaceHoliday(ctx, claims, req.Id, req.Body.Date.Time, req.Body.Name)
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.UpdateHoliday400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.UpdateHoliday403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return schedulecontract.UpdateHoliday404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return schedulecontract.UpdateHoliday409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.UpdateHoliday200JSONResponse(holidayResponse(holiday)), nil
}

func (h *Handler) DeleteHoliday(
	ctx context.Context, req schedulecontract.DeleteHolidayRequestObject,
) (schedulecontract.DeleteHolidayResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.DeleteHoliday(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return schedulecontract.DeleteHoliday403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return schedulecontract.DeleteHoliday404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.DeleteHoliday204Response{}, nil
}

func (h *Handler) ListOfficeLocations(
	ctx context.Context, _ schedulecontract.ListOfficeLocationsRequestObject,
) (schedulecontract.ListOfficeLocationsResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	locations, err := h.svc.ListLocations(ctx, claims)
	if status(err) == http.StatusForbidden {
		return schedulecontract.ListOfficeLocations403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]schedulecontract.OfficeLocation, 0, len(locations))
	for _, location := range locations {
		listed = append(listed, locationResponse(location))
	}
	return schedulecontract.ListOfficeLocations200JSONResponse(listed), nil
}

func (h *Handler) CreateOfficeLocation(
	ctx context.Context, req schedulecontract.CreateOfficeLocationRequestObject,
) (schedulecontract.CreateOfficeLocationResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	location, err := h.svc.CreateLocation(ctx, claims, place(*req.Body))
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.CreateOfficeLocation400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.CreateOfficeLocation403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.CreateOfficeLocation201JSONResponse(locationResponse(location)), nil
}

func (h *Handler) UpdateOfficeLocation(
	ctx context.Context, req schedulecontract.UpdateOfficeLocationRequestObject,
) (schedulecontract.UpdateOfficeLocationResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	location, err := h.svc.ReplaceLocation(ctx, claims, req.Id, place(*req.Body))
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.UpdateOfficeLocation400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.UpdateOfficeLocation403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return schedulecontract.UpdateOfficeLocation404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.UpdateOfficeLocation200JSONResponse(locationResponse(location)), nil
}

func (h *Handler) DeleteOfficeLocation(
	ctx context.Context, req schedulecontract.DeleteOfficeLocationRequestObject,
) (schedulecontract.DeleteOfficeLocationResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.DeleteLocation(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return schedulecontract.DeleteOfficeLocation403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return schedulecontract.DeleteOfficeLocation404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.DeleteOfficeLocation204Response{}, nil
}

func (h *Handler) ListShiftAssignments(
	ctx context.Context, req schedulecontract.ListShiftAssignmentsRequestObject,
) (schedulecontract.ListShiftAssignmentsResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	assignments, err := h.svc.ListAssignments(ctx, claims,
		span(req.Params.From, req.Params.To), req.Params.UserId)
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.ListShiftAssignments400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.ListShiftAssignments403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]schedulecontract.ShiftAssignment, 0, len(assignments))
	for _, assignment := range assignments {
		listed = append(listed, assignmentResponse(assignment))
	}
	return schedulecontract.ListShiftAssignments200JSONResponse(listed), nil
}

func (h *Handler) AssignShifts(
	ctx context.Context, req schedulecontract.AssignShiftsRequestObject,
) (schedulecontract.AssignShiftsResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	wanted := make([]Assignment, 0, len(req.Body.Assignments))
	for _, assignment := range req.Body.Assignments {
		wanted = append(wanted, Assignment{
			UserID:     assignment.UserId,
			WorkDate:   assignment.WorkDate.Time,
			ScheduleID: assignment.ScheduleId,
			Note:       text(assignment.Note),
		})
	}

	assignments, err := h.svc.Assign(ctx, claims, wanted)
	switch status(err) {
	case http.StatusBadRequest:
		return schedulecontract.AssignShifts400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return schedulecontract.AssignShifts403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]schedulecontract.ShiftAssignment, 0, len(assignments))
	for _, assignment := range assignments {
		listed = append(listed, assignmentResponse(assignment))
	}
	return schedulecontract.AssignShifts200JSONResponse(listed), nil
}

func (h *Handler) DeleteShiftAssignment(
	ctx context.Context, req schedulecontract.DeleteShiftAssignmentRequestObject,
) (schedulecontract.DeleteShiftAssignmentResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.DeleteAssignment(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return schedulecontract.DeleteShiftAssignment403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return schedulecontract.DeleteShiftAssignment404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return schedulecontract.DeleteShiftAssignment204Response{}, nil
}

// caller unwraps the pooled *gin.Context and reads who is asking; the routes sit behind middleware.Auth.
func caller(ctx context.Context) (context.Context, middleware.Claims, error) {
	ctx = middleware.RequestContext(ctx)
	claims, ok := middleware.ClaimsFrom(ctx)
	if !ok {
		return ctx, claims, errors.New("schedule route reached without middleware.Auth")
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
	}
	if apperr.IsValidation(err) {
		return http.StatusBadRequest
	}
	return 0
}

func forbidden(err error) schedulecontract.ForbiddenJSONResponse {
	return schedulecontract.ForbiddenJSONResponse{Message: err.Error()}
}

func notFound(err error) schedulecontract.NotFoundJSONResponse {
	return schedulecontract.NotFoundJSONResponse{Message: err.Error()}
}

func badRequest(err error) schedulecontract.BadRequestJSONResponse {
	return schedulecontract.BadRequestJSONResponse{Message: err.Error()}
}

func conflict(err error) schedulecontract.ConflictJSONResponse {
	return schedulecontract.ConflictJSONResponse{Message: err.Error()}
}

func span(from, to types.Date) Range {
	return Range{From: from.Time, To: to.Time}
}

func hours(body schedulecontract.WorkScheduleRequest) Hours {
	return Hours{
		Name:             body.Name,
		StartTime:        body.StartTime,
		EndTime:          body.EndTime,
		GraceMinutes:     number(body.GraceMinutes),
		BreakMinutes:     number(body.BreakMinutes),
		Workdays:         body.Workdays,
		ObservesHolidays: body.ObservesHolidays != nil && *body.ObservesHolidays,
	}
}

func place(body schedulecontract.OfficeLocationRequest) Place {
	return Place{Name: body.Name, Lat: body.Lat, Lng: body.Lng, RadiusM: body.RadiusM}
}

func dayResponse(day Day) schedulecontract.ScheduledDay {
	response := schedulecontract.ScheduledDay{
		Date:    types.Date{Time: day.Date},
		Working: day.Working,
		Reason:  schedulecontract.ScheduledDayReason(day.Reason),
	}
	if day.Holiday != "" {
		response.HolidayName = &day.Holiday
	}
	if day.Schedule != nil {
		hours := scheduleResponse(*day.Schedule)
		response.Schedule = &hours
	}
	return response
}

func scheduleResponse(schedule WorkSchedule) schedulecontract.WorkSchedule {
	workdays := make([]int, 0, len(schedule.Workdays))
	for _, day := range schedule.Workdays {
		workdays = append(workdays, int(day))
	}

	return schedulecontract.WorkSchedule{
		Id:               schedule.ID,
		Name:             schedule.Name,
		StartTime:        schedule.StartTime.String(),
		EndTime:          schedule.EndTime.String(),
		GraceMinutes:     schedule.GraceMinutes,
		BreakMinutes:     schedule.BreakMinutes,
		Workdays:         workdays,
		ObservesHolidays: schedule.ObservesHolidays,
		CrossesMidnight:  schedule.CrossesMidnight(),
	}
}

func locationResponse(location OfficeLocation) schedulecontract.OfficeLocation {
	return schedulecontract.OfficeLocation{
		Id: location.ID, Name: location.Name,
		Lat: location.Lat, Lng: location.Lng, RadiusM: location.RadiusM,
	}
}

func holidayResponse(holiday Holiday) schedulecontract.Holiday {
	return schedulecontract.Holiday{Id: holiday.ID, Date: types.Date{Time: holiday.Date}, Name: holiday.Name}
}

func assignmentResponse(assignment ShiftAssignment) schedulecontract.ShiftAssignment {
	response := schedulecontract.ShiftAssignment{
		UserId:     assignment.UserID,
		WorkDate:   types.Date{Time: assignment.WorkDate},
		ScheduleId: assignment.ScheduleID,
	}
	if assignment.ID != 0 {
		response.Id = &assignment.ID
	}
	if assignment.Note != "" {
		response.Note = &assignment.Note
	}
	return response
}

// number reads an optional integer field, where absent means zero.
func number(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func text(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
