package cmd

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/supabase/auth/internal/api"
	"github.com/supabase/auth/internal/conf"
	"github.com/supabase/auth/internal/reloader"
	"github.com/supabase/auth/internal/storage"
	"github.com/supabase/auth/internal/utilities"
)

var serveCmd = cobra.Command{
	Use:  "serve",
	Long: "Start API server",
	Run: func(cmd *cobra.Command, args []string) {
		serve(cmd.Context())
	},
}

func serve(ctx context.Context) {
	config, err := conf.LoadGlobal(configFile)
	if err != nil {
		logrus.WithError(err).Fatal("unable to load config")
	}

	db, err := storage.Dial(config)
	if err != nil {
		logrus.Fatalf("error opening database: %+v", err)
	}
	defer db.Close()

	addr := net.JoinHostPort(config.API.Host, config.API.Port)
	logrus.Infof("GoTrue API started on: %s", addr)

	a := api.NewAPIWithVersion(config, db, utilities.Version)
	hr := reloader.NewAtomicHandler(a)

	baseCtx, baseCancel := context.WithCancel(context.Background())
	defer baseCancel()

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           hr,
		ReadHeaderTimeout: 2 * time.Second, // to mitigate a Slowloris attack
		BaseContext: func(net.Listener) context.Context {
			return baseCtx
		},
	}
	log := logrus.WithField("component", "api")

	var wg sync.WaitGroup
	defer wg.Wait() // Do not return to caller until this goroutine is done.

	wg.Add(1)
	go func() {
		defer wg.Done()

		<-ctx.Done()

		defer baseCancel() // close baseContext

		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), time.Minute)
		defer shutdownCancel()

		if err := httpSrv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.WithError(err).Error("shutdown failed")
		}
	}()

	if err := httpSrv.ListenAndServe(); err != http.ErrServerClosed {
		log.WithError(err).Fatal("http server listen failed")
	}
}
