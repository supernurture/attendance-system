package example

import (
	"context"
	"errors"

	examplecontract "github.com/supernurture/go-template/internal/api/server/oapicodegen/example"
	"github.com/supernurture/go-template/internal/middleware"
	"github.com/supernurture/go-template/pkg/logger"
)

type Handler struct {
	service *Service
	log     *logger.Logger
}

func NewHandler(service *Service, log *logger.Logger) *Handler {
	return &Handler{service: service, log: log}
}

var _ examplecontract.StrictServerInterface = (*Handler)(nil)

func (h *Handler) GetExampleVisits(
	ctx context.Context,
	_ examplecontract.GetExampleVisitsRequestObject,
) (examplecontract.GetExampleVisitsResponseObject, error) {
	visits, err := h.service.CountVisit(ctx)
	if err != nil {
		return nil, err
	}

	return examplecontract.GetExampleVisits200JSONResponse{Visits: visits}, nil
}

func (h *Handler) ListExampleNotes(
	ctx context.Context,
	request examplecontract.ListExampleNotesRequestObject,
) (examplecontract.ListExampleNotesResponseObject, error) {
	notes, err := h.service.ListNotes(ctx, request.Params.Limit)
	if err != nil {
		if message, ok := validationMessage(err); ok {
			return examplecontract.ListExampleNotes400JSONResponse{Message: message}, nil
		}
		return nil, err
	}

	response := make(examplecontract.ListExampleNotes200JSONResponse, 0, len(notes))
	for _, stored := range notes {
		response = append(response, toContract(stored))
	}
	return response, nil
}

func (h *Handler) CreateExampleNote(
	ctx context.Context,
	request examplecontract.CreateExampleNoteRequestObject,
) (examplecontract.CreateExampleNoteResponseObject, error) {
	if request.Body == nil {
		return examplecontract.CreateExampleNote400JSONResponse{Message: "a JSON body is required"}, nil
	}

	var body string
	if request.Body.Body != nil {
		body = *request.Body.Body
	}

	note, err := h.service.CreateNote(ctx, request.Body.Title, body)
	if err != nil {
		if message, ok := validationMessage(err); ok {
			return examplecontract.CreateExampleNote400JSONResponse{Message: message}, nil
		}
		return nil, err
	}

	h.log.Info("note created", map[string]any{
		"request_id": middleware.RequestIDFrom(ctx),
		"note_id":    note.ID,
	})

	return examplecontract.CreateExampleNote201JSONResponse(toContract(note)), nil
}

func validationMessage(err error) (string, bool) {
	var invalid ValidationError
	if errors.As(err, &invalid) {
		return invalid.Message, true
	}
	return "", false
}

func toContract(stored Note) examplecontract.Note {
	return examplecontract.Note{
		Id:        stored.ID,
		Title:     stored.Title,
		Body:      stored.Body,
		CreatedAt: stored.CreatedAt,
	}
}
