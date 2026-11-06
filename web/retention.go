package web

import (
	"context"
	"log"
	"time"

	"github.com/guregu/intertube/tube"
)

func StartRetention(ctx context.Context) {
	go func() {
		runRetention(ctx, time.Now().UTC())
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				runRetention(ctx, now.UTC())
			}
		}
	}()
}

func runRetention(ctx context.Context, now time.Time) {
	if _, err := tube.DeleteStaleFiles(ctx, now.Add(-24*time.Hour)); err != nil {
		log.Println("retention: abandoned uploads:", err)
	}
	if _, err := tube.DeleteExpiredSessions(ctx, now); err != nil {
		log.Println("retention: expired sessions:", err)
	}
}
