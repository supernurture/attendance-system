package user

import (
	"context"
	"errors"
	"net/http"

	"github.com/oapi-codegen/runtime/types"

	usercontract "attendance-system/internal/api/server/oapicodegen/user"
	"attendance-system/internal/middleware"
)

type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

var _ usercontract.StrictServerInterface = (*Handler)(nil)

func (h *Handler) GetMe(
	ctx context.Context, _ usercontract.GetMeRequestObject,
) (usercontract.GetMeResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	user, err := h.svc.Me(ctx, claims)
	if status(err) == http.StatusNotFound {
		return usercontract.GetMe404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.GetMe200JSONResponse(response(user)), nil
}

func (h *Handler) ListUsers(
	ctx context.Context, _ usercontract.ListUsersRequestObject,
) (usercontract.ListUsersResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	users, err := h.svc.List(ctx, claims)
	if status(err) == http.StatusForbidden {
		return usercontract.ListUsers403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]usercontract.User, 0, len(users))
	for _, user := range users {
		listed = append(listed, response(user))
	}
	return usercontract.ListUsers200JSONResponse(listed), nil
}

func (h *Handler) CreateUser(
	ctx context.Context, req usercontract.CreateUserRequestObject,
) (usercontract.CreateUserResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	next := NewUser{
		Email:        req.Body.Email,
		Password:     req.Body.Password,
		FullName:     req.Body.FullName,
		DepartmentID: req.Body.DepartmentId,
		ManagerID:    req.Body.ManagerId,
	}
	if req.Body.JoinDate != nil {
		next.JoinDate = req.Body.JoinDate.Time
	}

	user, err := h.svc.Create(ctx, claims, next)
	switch status(err) {
	case http.StatusBadRequest:
		return usercontract.CreateUser400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return usercontract.CreateUser403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusConflict:
		return usercontract.CreateUser409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.CreateUser201JSONResponse(response(user)), nil
}

func (h *Handler) GetUser(
	ctx context.Context, req usercontract.GetUserRequestObject,
) (usercontract.GetUserResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	user, err := h.svc.Get(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return usercontract.GetUser403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return usercontract.GetUser404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.GetUser200JSONResponse(response(user)), nil
}

func (h *Handler) UpdateUser(
	ctx context.Context, req usercontract.UpdateUserRequestObject,
) (usercontract.UpdateUserResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	user, err := h.svc.Replace(ctx, claims, req.Id, Details{
		FullName:     req.Body.FullName,
		IsActive:     req.Body.IsActive,
		JoinDate:     req.Body.JoinDate.Time,
		DepartmentID: req.Body.DepartmentId,
		ManagerID:    req.Body.ManagerId,
	})
	switch status(err) {
	case http.StatusBadRequest:
		return usercontract.UpdateUser400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return usercontract.UpdateUser403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return usercontract.UpdateUser404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.UpdateUser200JSONResponse(response(user)), nil
}

func (h *Handler) DeleteUser(
	ctx context.Context, req usercontract.DeleteUserRequestObject,
) (usercontract.DeleteUserResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.Delete(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusBadRequest, http.StatusForbidden:
		return usercontract.DeleteUser403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return usercontract.DeleteUser404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.DeleteUser204Response{}, nil
}

func (h *Handler) UpdateUserRole(
	ctx context.Context, req usercontract.UpdateUserRoleRequestObject,
) (usercontract.UpdateUserRoleResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	user, err := h.svc.ChangeRole(ctx, claims, req.Id, middleware.Role(req.Body.Role))
	switch status(err) {
	case http.StatusBadRequest:
		return usercontract.UpdateUserRole400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return usercontract.UpdateUserRole403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return usercontract.UpdateUserRole404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.UpdateUserRole200JSONResponse(response(user)), nil
}

func (h *Handler) ListDepartments(
	ctx context.Context, _ usercontract.ListDepartmentsRequestObject,
) (usercontract.ListDepartmentsResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	departments, err := h.svc.ListDepartments(ctx, claims)
	if status(err) == http.StatusForbidden {
		return usercontract.ListDepartments403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	}
	if err != nil {
		return nil, err
	}

	listed := make([]usercontract.Department, 0, len(departments))
	for _, department := range departments {
		listed = append(listed, departmentResponse(department))
	}
	return usercontract.ListDepartments200JSONResponse(listed), nil
}

func (h *Handler) CreateDepartment(
	ctx context.Context, req usercontract.CreateDepartmentRequestObject,
) (usercontract.CreateDepartmentResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	department, err := h.svc.CreateDepartment(ctx, claims, req.Body.Name)
	switch status(err) {
	case http.StatusBadRequest:
		return usercontract.CreateDepartment400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return usercontract.CreateDepartment403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusConflict:
		return usercontract.CreateDepartment409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.CreateDepartment201JSONResponse(departmentResponse(department)), nil
}

func (h *Handler) UpdateDepartment(
	ctx context.Context, req usercontract.UpdateDepartmentRequestObject,
) (usercontract.UpdateDepartmentResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	department, err := h.svc.RenameDepartment(ctx, claims, req.Id, req.Body.Name)
	switch status(err) {
	case http.StatusBadRequest:
		return usercontract.UpdateDepartment400JSONResponse{BadRequestJSONResponse: badRequest(err)}, nil
	case http.StatusForbidden:
		return usercontract.UpdateDepartment403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return usercontract.UpdateDepartment404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	case http.StatusConflict:
		return usercontract.UpdateDepartment409JSONResponse{ConflictJSONResponse: conflict(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.UpdateDepartment200JSONResponse(departmentResponse(department)), nil
}

func (h *Handler) DeleteDepartment(
	ctx context.Context, req usercontract.DeleteDepartmentRequestObject,
) (usercontract.DeleteDepartmentResponseObject, error) {
	ctx, claims, err := caller(ctx)
	if err != nil {
		return nil, err
	}

	err = h.svc.DeleteDepartment(ctx, claims, req.Id)
	switch status(err) {
	case http.StatusForbidden:
		return usercontract.DeleteDepartment403JSONResponse{ForbiddenJSONResponse: forbidden(err)}, nil
	case http.StatusNotFound:
		return usercontract.DeleteDepartment404JSONResponse{NotFoundJSONResponse: notFound(err)}, nil
	}
	if err != nil {
		return nil, err
	}
	return usercontract.DeleteDepartment204Response{}, nil
}

// caller unwraps the pooled *gin.Context and reads who is asking; the routes sit behind middleware.Auth.
func caller(ctx context.Context) (context.Context, middleware.Claims, error) {
	ctx = middleware.RequestContext(ctx)
	claims, ok := middleware.ClaimsFrom(ctx)
	if !ok {
		return ctx, claims, errors.New("user route reached without middleware.Auth")
	}
	return ctx, claims, nil
}

// status is the code the client should see, or 0 when the error is not theirs to fix.
func status(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	}
	if _, ok := errors.AsType[*ValidationError](err); ok {
		return http.StatusBadRequest
	}
	return 0
}

func forbidden(err error) usercontract.ForbiddenJSONResponse {
	return usercontract.ForbiddenJSONResponse{Message: err.Error()}
}

func notFound(err error) usercontract.NotFoundJSONResponse {
	return usercontract.NotFoundJSONResponse{Message: err.Error()}
}

func badRequest(err error) usercontract.BadRequestJSONResponse {
	return usercontract.BadRequestJSONResponse{Message: err.Error()}
}

func conflict(err error) usercontract.ConflictJSONResponse {
	return usercontract.ConflictJSONResponse{Message: err.Error()}
}

func response(user User) usercontract.User {
	return usercontract.User{
		Id:           user.ID,
		Email:        user.Email,
		FullName:     user.FullName,
		Role:         usercontract.Role(user.Role),
		IsActive:     user.IsActive,
		JoinDate:     types.Date{Time: user.JoinDate},
		DepartmentId: user.DepartmentID,
		ManagerId:    user.ManagerID,
	}
}

func departmentResponse(department Department) usercontract.Department {
	return usercontract.Department{Id: department.ID, Name: department.Name}
}
