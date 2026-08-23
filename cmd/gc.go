package cmd

import (
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/pkg/storage"
)

func init() {
	rootCmd.AddCommand(gcCmd)
}

var gcCmd = &cobra.Command{
	Use:   "gc [maxDeletes]",
	Short: "Reclaim expired chunks once and exit",
	Long: "Run one expiry sweep. The server does this on a timer by itself, so this is for\n" +
		"a manual reclaim, or for cron on an install that has set gc.interval to 0.",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Parse()
		if err != nil {
			return err
		}

		maxDeletes := cfg.GC.MaxDeletes
		if len(args) == 1 {
			parsed, err := strconv.Atoi(args[0])
			if err != nil || parsed <= 0 {
				return err
			}
			maxDeletes = parsed
		}

		store := storage.NewStore(cfg)
		gc := storage.NewGc(cfg, store, storage.NewQuota(cfg))

		// Read before sweeping: Sweep stamps the new time on its way in.
		last, ok := gc.LastSweepAt()
		if ok {
			cmd.Printf("last sweep: %s (%.1f h ago)\n",
				last.UTC().Format(time.RFC3339), time.Since(last).Hours())
		} else {
			cmd.Println("last sweep: never")
		}

		cmd.Printf("reclaimed %d expired item(s)\n", gc.Sweep(maxDeletes))

		return nil
	},
}
