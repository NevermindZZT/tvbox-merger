package scheduler

import (
	"fmt"
	"log"
	"time"

	"github.com/robfig/cron/v3"
	"gorm.io/gorm"

	"tvbox-merger/internal/config"
	"tvbox-merger/internal/fetcher"
	"tvbox-merger/internal/merger"
)

type Scheduler struct {
	cron   *cron.Cron
	jobID  cron.EntryID
	merger *merger.Merger
	cfg    *config.Config
	done   chan struct{}
}

func New(db *gorm.DB, cfg *config.Config) *Scheduler {
	c := cron.New(cron.WithSeconds())
	fet := fetcher.New(cfg)
	m := merger.New(db, fet)

	return &Scheduler{
		cron:   c,
		merger: m,
		cfg:    cfg,
		done:   make(chan struct{}),
	}
}

func (s *Scheduler) Start() {
	// Run immediately on start
	go func() {
		log.Println("Scheduler: running initial merge...")
		if err := s.merger.MergeAll(); err != nil {
			log.Printf("Scheduler: initial merge error: %v", err)
		} else {
			log.Println("Scheduler: initial merge complete")
		}
	}()

	// Schedule periodic refresh
	interval := s.cfg.RefreshInterval
	if interval < time.Minute {
		interval = time.Minute
	}

	cronExpr := fmtCronExpr(interval)
	var err error
	s.jobID, err = s.cron.AddFunc(cronExpr, func() {
		log.Println("Scheduler: running periodic merge...")
		if err := s.merger.MergeAll(); err != nil {
			log.Printf("Scheduler: merge error: %v", err)
		} else {
			log.Println("Scheduler: merge complete")
		}
	})
	if err != nil {
		log.Printf("Scheduler: failed to add cron job: %v", err)
		return
	}

	s.cron.Start()
	log.Printf("Scheduler: started with interval %v", interval)
}

func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
	close(s.done)
	log.Println("Scheduler: stopped")
}

// TriggerMerge triggers an immediate merge for all groups (or a specific group)
func (s *Scheduler) TriggerMerge(groupID ...uint) error {
	return s.merger.MergeAll(groupID...)
}

// fmtCronExpr converts a duration to a cron expression
// Supports: every N seconds/minutes/hours
func fmtCronExpr(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("@every %ds", seconds)
	}
	minutes := int(d.Minutes())
	if minutes < 60 {
		return fmt.Sprintf("@every %dm", minutes)
	}
	hours := int(d.Hours())
	return fmt.Sprintf("@every %dh", hours)
}
