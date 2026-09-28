package log

import (
	"log/slog"
	"os"
)

var Logger *slog.Logger

func init() {
	Logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(Logger)
}

func Info(msg string, args ...any)  { Logger.Info(msg, args...) }
func Error(msg string, args ...any) { Logger.Error(msg, args...) }
func Warn(msg string, args ...any)  { Logger.Warn(msg, args...) }
func Debug(msg string, args ...any) { Logger.Debug(msg, args...) }
