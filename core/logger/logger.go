package logger

import (
	"os"

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
	return lgr
}
