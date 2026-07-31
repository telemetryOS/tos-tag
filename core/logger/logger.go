package logger

import (
	"os"
	"path/filepath"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/config"
)

func New(cfg *config.Config) *blackbox.Logger {
	lgr := blackbox.NewWithCtx(blackbox.Ctx{"service": "tos-tag"})
	if cfg == nil {
		return lgr
	}
	if cfg.Logging.UseJSON {
		lgr.AddTarget(blackbox.NewJSONTarget(os.Stdout, os.Stderr))
	} else {
		lgr.AddTarget(blackbox.NewPrettyTarget(os.Stdout, os.Stderr).UseColor(!cfg.Logging.DisableColor))
	}
	level := cfg.Logging.Level
	if level == "" {
		level = "info"
	}
	lgr.SetLevel(blackbox.LevelFromString(level))
	if cfg.Logging.FilePath != "" {
		directory := filepath.Dir(cfg.Logging.FilePath)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			lgr.WithCtx(blackbox.Ctx{"error_type": "mkdir"}).Error("open structured log file")
			return lgr
		}
		file, err := os.OpenFile(cfg.Logging.FilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			lgr.WithCtx(blackbox.Ctx{"error_type": "open"}).Error("open structured log file")
			return lgr
		}
		_ = file.Chmod(0o600)
		lgr.AddTarget(blackbox.NewJSONTarget(file, file))
		lgr.WithCtx(blackbox.Ctx{"log_file": cfg.Logging.FilePath}).Info("structured log file enabled")
	}
	return lgr
}
