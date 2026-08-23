package cmd

import (
	"context"
	"sync"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/ModderMule/emule-http-cache-go/http_public"
	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/install"
	"github.com/ModderMule/emule-http-cache-go/log"
	"github.com/ModderMule/emule-http-cache-go/pkg/storage"
)

func init() {
	rootCmd.AddCommand(serveCmd)
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the chunk cache HTTP server",
	Long: "Start the HTTP server that stores and serves encrypted chunks. With no config\n" +
		"file present it still starts, serving only the /install page until one exists.",
	RunE: func(cmd *cobra.Command, args []string) error {
		logger, err := getLogger()
		if err != nil {
			return err
		}

		cfg, err := config.Parse()
		if err != nil {
			return err
		}
		for _, warning := range cfg.Warnings() {
			logger.Warnf("%s", warning)
		}
		if !storage.LockSupported {
			logger.Warnf("cross-process file locking is unavailable on this platform; do not share the var directory with another process")
		}

		installer := install.New(baseDir(), configPath(), cfg.Storage.VarDir)

		// Application context, cancelled by a termination signal or by any
		// worker exiting.
		ctx, cancel := context.WithCancel(cmd.Context())
		defer cancel()
		go listenExitCommand(logger, cancel)

		sweeper := newSweeper(ctx, logger)
		defer sweeper.stop()

		store := storage.NewStore(cfg)
		quota := storage.NewQuota(cfg)
		installed := config.Installed()

		if installed {
			sweeper.restart(storage.NewGc(cfg, store, quota))
		}

		srv, err := http_public.New(http_public.Deps{
			Config:    cfg,
			Store:     store,
			Quota:     quota,
			GC:        storage.NewGc(cfg, store, quota),
			Installer: installer,
			Logger:    logger,
			AccessLog: log.NewConfig(viper.GetViper()).AccessLog,
			Installed: installed,

			// Called once, by the install page, when a config has just been
			// written. It brings the new settings into service and starts the
			// sweeper that could not run without them.
			Reload: func() (*config.Config, *storage.Store, *storage.Quota, error) {
				if err := config.LoadConfig(".", configFile); err != nil {
					return nil, nil, nil, err
				}

				fresh, err := config.Parse()
				if err != nil {
					return nil, nil, nil, err
				}
				for _, warning := range fresh.Warnings() {
					logger.Warnf("%s", warning)
				}

				newStore := storage.NewStore(fresh)
				newQuota := storage.NewQuota(fresh)
				sweeper.restart(storage.NewGc(fresh, newStore, newQuota))

				return fresh, newStore, newQuota, nil
			},
		})
		if err != nil {
			return err
		}

		// Run all long-lived workers in parallel under a WaitGroup. Each
		// cancels the context on exit, so a failure in one brings the others
		// down gracefully.
		var wg sync.WaitGroup

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()

			if err := srv.StartServer(ctx); err != nil {
				logger.Errorf("http server error: %v", err)
			}
		}()

		wg.Wait()

		return nil
	},
}

// sweeper owns the expiry goroutine, which has to be restartable: a server that
// starts uninstalled has no storage to sweep until the install page writes one.
type sweeper struct {
	parent context.Context
	logger log.Logger

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func newSweeper(parent context.Context, logger log.Logger) *sweeper {
	return &sweeper{parent: parent, logger: logger}
}

// restart stops any running sweep loop and starts one over the given collector.
func (s *sweeper) restart(gc *storage.Gc) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopLocked()

	ctx, cancel := context.WithCancel(s.parent)
	done := make(chan struct{})
	s.cancel, s.done = cancel, done

	go func() {
		defer close(done)
		gc.Run(ctx, s.logger)
	}()
}

// stop ends the sweep loop and waits for it, so a sweep in flight finishes its
// current unlink rather than being abandoned mid-way.
func (s *sweeper) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.stopLocked()
}

func (s *sweeper) stopLocked() {
	if s.cancel == nil {
		return
	}

	s.cancel()
	<-s.done
	s.cancel, s.done = nil, nil
}
