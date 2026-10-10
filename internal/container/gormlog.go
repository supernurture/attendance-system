package container

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"attendance-system/internal/middleware"
	"attendance-system/pkg/logger"
)

const slowQuery = 200 * time.Millisecond

// gormLog writes GORM's failed and slow statements to the app log, tagged with the request that ran them.
type gormLog struct{ log *logger.Logger }

// LogMode ignores GORM's level: the app logger's level already filters.
func (g gormLog) LogMode(gormlogger.LogLevel) gormlogger.Interface { return g }

// Info logs a GORM message at INFO.
func (g gormLog) Info(ctx context.Context, msg string, data ...any) {
	g.log.Info(fmt.Sprintf(msg, data...), queryFields(ctx))
}

// Warn logs a GORM message at WARN.
func (g gormLog) Warn(ctx context.Context, msg string, data ...any) {
	g.log.Warn(fmt.Sprintf(msg, data...), queryFields(ctx))
}

// Error logs a GORM message at ERROR.
func (g gormLog) Error(ctx context.Context, msg string, data ...any) {
	g.log.Error(fmt.Sprintf(msg, data...), queryFields(ctx))
}

// Trace logs a failed statement at ERROR and one slower than slowQuery at WARN; a missing row is not a failure.
func (g gormLog) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	elapsed := time.Since(begin)
	failed := err != nil && !errors.Is(err, gorm.ErrRecordNotFound)
	if !failed && elapsed <= slowQuery {
		return
	}

	fields := queryFields(ctx)
	fields["sql"], fields["rows"] = fc()
	fields["latency_ms"] = elapsed.Milliseconds()
	if failed {
		fields["error"] = err.Error()
		g.log.Error("query failed", fields)
		return
	}
	g.log.Warn("slow query", fields)
}

// ParamsFilter drops bind values, so password hashes, tokens and personal data never reach the log.
func (gormLog) ParamsFilter(_ context.Context, sql string, _ ...any) (string, []any) { return sql, nil }

func queryFields(ctx context.Context) map[string]any {
	return map[string]any{"request_id": middleware.RequestIDFrom(ctx)}
}
