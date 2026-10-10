package health

import (
	"context"
	"fmt"
	"time"

	healthcontract "attendance-system/internal/api/server/oapicodegen/health"
	"attendance-system/internal/middleware"
	"attendance-system/pkg/logger"
)

// Handler answers the liveness and readiness probes.
type Handler struct {
	checks map[string]func(context.Context) error
	log    *logger.Logger
}

// NewHandler takes readiness checks keyed by dependency name, as from container.Pings.
func NewHandler(checks map[string]func(context.Context) error, log *logger.Logger) *Handler {
	return &Handler{checks: checks, log: log}
}

var checkTimeout = 2 * time.Second

var _ healthcontract.StrictServerInterface = (*Handler)(nil)

// GetHealth reports liveness: the process is up, whatever its dependencies are doing.
func (h *Handler) GetHealth(
	_ context.Context, _ healthcontract.GetHealthRequestObject) (healthcontract.GetHealthResponseObject, error) {
	return healthcontract.GetHealth200JSONResponse{Condition: "Healthy"}, nil
}

// GetReady runs every check at once, each with its own deadline, and logs each dependency that is down.
func (h *Handler) GetReady(
	ctx context.Context, _ healthcontract.GetReadyRequestObject) (healthcontract.GetReadyResponseObject, error) {
	ctx = middleware.RequestContext(ctx)

	type result struct {
		name string
		err  error
	}
	results := make(chan result, len(h.checks))
	for name, check := range h.checks {
		go func() {
			checkCtx, cancel := context.WithTimeout(ctx, checkTimeout)
			defer cancel()

			res := result{name: name}
			// Recovery cannot reach this goroutine, so an unrecovered panic here would kill the process.
			defer func() {
				if p := recover(); p != nil {
					res.err = fmt.Errorf("check panicked: %v", p)
				}
				results <- res
			}()
			res.err = check(checkCtx)
		}()
	}

	ready := true
	for range h.checks {
		if res := <-results; res.err != nil {
			ready = false
			h.log.Warn("readiness check failed", map[string]any{
				"request_id": middleware.RequestIDFrom(ctx),
				"dependency": res.name,
				"error":      res.err.Error(),
			})
		}
	}

	if !ready {
		return healthcontract.GetReady503JSONResponse{Condition: "NotReady"}, nil
	}
	return healthcontract.GetReady200JSONResponse{Condition: "Ready"}, nil
}
