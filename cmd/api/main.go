package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/telemetryos/tos-tag/core"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/logger"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fail(err)
	}
	lgr := logger.New(cfg)
	service, err := core.New(cfg, lgr)
	if err != nil {
		fail(err)
	}
	runCtx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := service.Start(runCtx); err != nil {
		fail(err)
	}
	<-runCtx.Done()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout+time.Second)
	defer stopCancel()
	if err := service.Stop(stopCtx); err != nil {
		fail(err)
	}
}

func fail(err error) {
	_, _ = fmt.Fprintf(os.Stderr, "tos-tag: %v\n", err)
	os.Exit(1)
}
