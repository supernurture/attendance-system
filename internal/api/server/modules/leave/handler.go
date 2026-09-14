package leave

import (
	"context"
	"errors"
	"net/http"

	"github.com/oapi-codegen/runtime/types"

	leavecontract "attendance-system/internal/api/server/oapicodegen/leave"
	"attendance-system/internal/middleware"
	"attendance-system/internal/pkg/apperr"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

var _ leavecontract.StrictServerInterface = (*Handler)(nil)

func (h *Handler) RequestLeave(
	ctx context.Context, req leavecontract.RequestLeaveRequestObject,
) (leavecontract.RequestLeaveResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	row, err := h.svc.Request(ctx, claims.UserID, Filing{
		Type:          req.Body.Type,
		StartDate:     req.Body.StartDate.Time,
		EndDate:       req.Body.EndDate.Time,
		Reason:        req.Body.Reason,
		AttachmentKey: req.Body.AttachmentKey,
	})
	switch status(err) {
	case http.StatusBadRequest:
		return leavecontract.RequestLeave400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return leavecontract.RequestLeave403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return leavecontract.RequestLeave404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return leavecontract.RequestLeave409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.RequestLeave201JSONResponse(leaveResponse(row)), nil
}

func (h *Handler) ListMyLeaves(
	ctx context.Context, _ leavecontract.ListMyLeavesRequestObject,
) (leavecontract.ListMyLeavesResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := h.svc.Mine(ctx, claims.UserID)
	if err != nil {
		return nil, err
	}
	return leavecontract.ListMyLeaves200JSONResponse(leavesResponse(rows)), nil
}

func (h *Handler) GetMyLeaveBalance(
	ctx context.Context, req leavecontract.GetMyLeaveBalanceRequestObject,
) (leavecontract.GetMyLeaveBalanceResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	balances, err := h.svc.Balances(ctx, claims.UserID, req.Params.Year)
	if status(err) == http.StatusBadRequest {
		return leavecontract.GetMyLeaveBalance400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]leavecontract.Balance, 0, len(balances))
	for _, balance := range balances {
		listed = append(listed, leavecontract.Balance{
			LeaveTypeId:     balance.LeaveTypeID,
			Code:            balance.Code,
			Name:            balance.Name,
			Year:            balance.Year,
			QuotaDays:       balance.QuotaDays,
			CarriedOverDays: balance.CarriedOverDays,
			UsedDays:        balance.UsedDays,
			PendingDays:     balance.PendingDays,
			RemainingDays:   balance.RemainingDays(),
		})
	}
	return leavecontract.GetMyLeaveBalance200JSONResponse(listed), nil
}

func (h *Handler) ListPendingLeaves(
	ctx context.Context, _ leavecontract.ListPendingLeavesRequestObject,
) (leavecontract.ListPendingLeavesResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := h.svc.Pending(ctx, claims)
	if status(err) == http.StatusForbidden {
		return leavecontract.ListPendingLeaves403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.ListPendingLeaves200JSONResponse(leavesResponse(rows)), nil
}

func (h *Handler) CancelLeave(
	ctx context.Context, req leavecontract.CancelLeaveRequestObject,
) (leavecontract.CancelLeaveResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.Cancel(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return leavecontract.CancelLeave403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return leavecontract.CancelLeave404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return leavecontract.CancelLeave409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.CancelLeave204Response{}, nil
}

func (h *Handler) DecideLeave(
	ctx context.Context, req leavecontract.DecideLeaveRequestObject,
) (leavecontract.DecideLeaveResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	note := ""
	if req.Body.Note != nil {
		note = *req.Body.Note
	}
	row, err := h.svc.Decide(ctx, claims, req.Id, Decision(req.Body.Decision), note)
	switch status(err) {
	case http.StatusBadRequest:
		return leavecontract.DecideLeave400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return leavecontract.DecideLeave403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return leavecontract.DecideLeave404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return leavecontract.DecideLeave409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.DecideLeave200JSONResponse(leaveResponse(row)), nil
}

func (h *Handler) GetLeaveAttachment(
	ctx context.Context, req leavecontract.GetLeaveAttachmentRequestObject,
) (leavecontract.GetLeaveAttachmentResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	url, err := h.svc.AttachmentURL(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return leavecontract.GetLeaveAttachment403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return leavecontract.GetLeaveAttachment404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.GetLeaveAttachment302Response{
		Headers: leavecontract.GetLeaveAttachment302ResponseHeaders{Location: url},
	}, nil
}

func (h *Handler) ListLeaveTypes(
	ctx context.Context, _ leavecontract.ListLeaveTypesRequestObject,
) (leavecontract.ListLeaveTypesResponseObject, error) {
	kinds, err := h.svc.Types(middleware.RequestContext(ctx))
	if err != nil {
		return nil, err
	}

	listed := make([]leavecontract.LeaveType, 0, len(kinds))
	for _, kind := range kinds {
		listed = append(listed, typeResponse(kind))
	}
	return leavecontract.ListLeaveTypes200JSONResponse(listed), nil
}

func (h *Handler) CreateLeaveType(
	ctx context.Context, req leavecontract.CreateLeaveTypeRequestObject,
) (leavecontract.CreateLeaveTypeResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	kind, err := h.svc.CreateType(ctx, claims, typeFields(*req.Body))
	switch status(err) {
	case http.StatusBadRequest:
		return leavecontract.CreateLeaveType400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return leavecontract.CreateLeaveType403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusConflict:
		return leavecontract.CreateLeaveType409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.CreateLeaveType201JSONResponse(typeResponse(kind)), nil
}

func (h *Handler) UpdateLeaveType(
	ctx context.Context, req leavecontract.UpdateLeaveTypeRequestObject,
) (leavecontract.UpdateLeaveTypeResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	kind, err := h.svc.ReplaceType(ctx, claims, req.Id, typeFields(*req.Body))
	switch status(err) {
	case http.StatusBadRequest:
		return leavecontract.UpdateLeaveType400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return leavecontract.UpdateLeaveType403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return leavecontract.UpdateLeaveType404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return leavecontract.UpdateLeaveType409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.UpdateLeaveType200JSONResponse(typeResponse(kind)), nil
}

func (h *Handler) DeleteLeaveType(
	ctx context.Context, req leavecontract.DeleteLeaveTypeRequestObject,
) (leavecontract.DeleteLeaveTypeResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.DeleteType(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return leavecontract.DeleteLeaveType403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return leavecontract.DeleteLeaveType404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.DeleteLeaveType204Response{}, nil
}

func (h *Handler) SetLeaveQuota(
	ctx context.Context, req leavecontract.SetLeaveQuotaRequestObject,
) (leavecontract.SetLeaveQuotaResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	quota, err := h.svc.SetQuota(ctx, claims, Quota{
		UserID: req.Body.UserId, LeaveTypeID: req.Body.LeaveTypeId, Year: req.Body.Year, QuotaDays: req.Body.QuotaDays,
	}, req.Body.CarriedOverDays)
	switch status(err) {
	case http.StatusBadRequest:
		return leavecontract.SetLeaveQuota400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return leavecontract.SetLeaveQuota403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return leavecontract.SetLeaveQuota200JSONResponse{
		UserId: quota.UserID, LeaveTypeId: quota.LeaveTypeID, Year: quota.Year,
		QuotaDays: quota.QuotaDays, CarriedOverDays: quota.CarriedOverDays,
	}, nil
}

// caller unwraps the pooled *gin.Context and reads who is asking; the routes sit behind middleware.Auth.
func caller(ctx context.Context) (context.Context, middleware.Claims, error) {
	ctx = middleware.RequestContext(ctx)
	claims, ok := middleware.ClaimsFrom(ctx)
	if !ok {
		return ctx, claims, errors.New("leave route reached without middleware.Auth")
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

func badRequest(err error) leavecontract.BadRequestJSONResponse {
	return leavecontract.BadRequestJSONResponse{Message: err.Error()}
}

func forbidden(err error) leavecontract.ForbiddenJSONResponse {
	return leavecontract.ForbiddenJSONResponse{Message: err.Error()}
}

func notFound(err error) leavecontract.NotFoundJSONResponse {
	return leavecontract.NotFoundJSONResponse{Message: err.Error()}
}

func conflict(err error) leavecontract.ConflictJSONResponse {
	return leavecontract.ConflictJSONResponse{Message: err.Error()}
}

func leavesResponse(rows []Leave) []leavecontract.Leave {
	listed := make([]leavecontract.Leave, 0, len(rows))
	for _, row := range rows {
		listed = append(listed, leaveResponse(row))
	}
	return listed
}

func leaveResponse(row Leave) leavecontract.Leave {
	return leavecontract.Leave{
		Id:             row.ID,
		UserId:         row.UserID,
		LeaveTypeId:    row.LeaveTypeID,
		StartDate:      types.Date{Time: row.StartDate},
		EndDate:        types.Date{Time: row.EndDate},
		WorkingDays:    row.WorkingDays,
		Reason:         row.Reason,
		AttachmentMime: row.AttachmentMime,
		Status:         leavecontract.LeaveStatus(row.Status),
		ReviewedBy:     row.ReviewedBy,
		ReviewedAt:     row.ReviewedAt,
		ReviewNote:     row.ReviewNote,
		CancelledBy:    row.CancelledBy,
		CancelledAt:    row.CancelledAt,
		CreatedAt:      row.CreatedAt,
	}
}

func typeFields(body leavecontract.LeaveTypeRequest) LeaveType {
	return LeaveType{
		Code:                       body.Code,
		Name:                       body.Name,
		QuotaDaysPerYear:           body.QuotaDaysPerYear,
		MaxWorkingDaysPerRequest:   body.MaxWorkingDaysPerRequest,
		AttachmentRequiredFromDays: body.AttachmentRequiredFromDays,
		IsPaid:                     body.IsPaid,
		AutoApprove:                body.AutoApprove,
	}
}

func typeResponse(kind LeaveType) leavecontract.LeaveType {
	return leavecontract.LeaveType{
		Id:                         kind.ID,
		Code:                       kind.Code,
		Name:                       kind.Name,
		QuotaDaysPerYear:           kind.QuotaDaysPerYear,
		MaxWorkingDaysPerRequest:   kind.MaxWorkingDaysPerRequest,
		AttachmentRequiredFromDays: kind.AttachmentRequiredFromDays,
		IsPaid:                     kind.IsPaid,
		AutoApprove:                kind.AutoApprove,
	}
}
