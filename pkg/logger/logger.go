package logger

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	rotatelogs "github.com/lestrrat-go/file-rotatelogs"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var hostname = os.Hostname

// RotationOptions controls log file rotation and retention.
type RotationOptions struct {
	Daily      bool // rotate every 24h (dated filename); else rotate by MaxSizeMB
	MaxSizeMB  int  // rotate when the active file exceeds this size
	MaxAgeDays int  // delete rotated files older than this
}

// Config configures a Logger. The log file is <Path>/<ServiceName>/app-<date>.log.
type Config struct {
	ServiceName  string
	Env          string
	Path         string // base log dir, defaults to ./logs
	Level        string // DEBUG, INFO, WARN, or ERROR (any case); defaults to INFO
	ReportCaller bool
	Console      bool // also write JSON logs to stdout
	Rotation     RotationOptions
}

// Logger is a structured JSON logger. Pass it as a dependency.
type Logger struct {
	zap    *zap.Logger
	closer io.Closer
}

// New builds a Logger writing to <Path>/<ServiceName>/app-<date>.log.
func New(cfg Config) (*Logger, error) {
	if cfg.Path == "" {
		cfg.Path = "./logs" // default log path
	}

	dir := filepath.Join(cfg.Path, cfg.ServiceName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	rotateOpts := []rotatelogs.Option{rotatelogs.WithLinkName("")}
	if cfg.Rotation.MaxAgeDays > 0 {
		rotateOpts = append(
			rotateOpts, rotatelogs.WithMaxAge(time.Duration(cfg.Rotation.MaxAgeDays)*24*time.Hour))
	}
	if cfg.Rotation.Daily {
		rotateOpts = append(rotateOpts, rotatelogs.WithRotationTime(24*time.Hour))
	} else if cfg.Rotation.MaxSizeMB > 0 {
		rotateOpts = append(rotateOpts, rotatelogs.WithRotationSize(int64(cfg.Rotation.MaxSizeMB)*1024*1024))
	}

	rotator, err := rotatelogs.New(filepath.Join(dir, "app-%Y-%m-%d.log"), rotateOpts...)
	if err != nil {
		return nil, err
	}
	encoder := zapcore.NewJSONEncoder(encoderConfig())

	cores := []zapcore.Core{
		zapcore.NewCore(encoder, zapcore.AddSync(rotator), parseLevel(cfg.Level))}
	if cfg.Console {
		cores = append(cores, zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), parseLevel(cfg.Level)))
	}
	core := zapcore.NewTee(cores...)

	var zapOpts []zap.Option
	if cfg.ReportCaller {
		zapOpts = append(zapOpts, zap.AddCaller(), zap.AddCallerSkip(1))
	}

	host, err := hostname()
	if err != nil {
		host = ""
	}

	return &Logger{
		zap: zap.New(core, zapOpts...).With(
			zap.String("application_name", cfg.ServiceName),
			zap.String("env", cfg.Env),
			zap.String("host", host),
		),
		closer: rotator,
	}, nil
}

// Debug logs at DEBUG level; fields become structured keys on the log line.
func (l *Logger) Debug(msg string, fields map[string]any) { l.zap.Debug(msg, zapFields(fields)...) }

// Info logs at INFO level; fields become structured keys on the log line.
func (l *Logger) Info(msg string, fields map[string]any) { l.zap.Info(msg, zapFields(fields)...) }

// Warn logs at WARN level; fields become structured keys on the log line.
func (l *Logger) Warn(msg string, fields map[string]any) { l.zap.Warn(msg, zapFields(fields)...) }

// Error logs at ERROR level; fields become structured keys on the log line.
func (l *Logger) Error(msg string, fields map[string]any) { l.zap.Error(msg, zapFields(fields)...) }

// Close flushes buffered logs and closes the underlying file.
func (l *Logger) Close() error {
	_ = l.zap.Sync()
	if l.closer != nil {
		return l.closer.Close()
	}
	return nil
}

func encoderConfig() zapcore.EncoderConfig {
	return zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		MessageKey:     "message",
		CallerKey:      "caller",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}
}

func parseLevel(level string) zapcore.Level {
	parsed, err := zapcore.ParseLevel(strings.TrimSpace(level))
	if err != nil {
		return zapcore.InfoLevel
	}
	return parsed
}

func zapFields(fields map[string]any) []zap.Field {
	out := make([]zap.Field, 0, len(fields))
	for key, value := range fields {
		out = append(out, zap.Any(key, value))
	}
	return out
}
