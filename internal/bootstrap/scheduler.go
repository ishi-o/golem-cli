package bootstrap

import (
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// localScheduler adapts robfig/cron to golem's deliberately small Scheduler
// interface. Cron jobs and one-shot timers share the same id namespace so a
// user can cancel either kind through the same golem schedule tool.
type localScheduler struct {
	cron *cron.Cron

	mu       sync.Mutex
	cronJobs map[string]cron.EntryID
	timers   map[string]*timerJob
}

type timerJob struct {
	timer *time.Timer
}

func newLocalScheduler() *localScheduler {
	s := &localScheduler{
		cron:     cron.New(),
		cronJobs: make(map[string]cron.EntryID),
		timers:   make(map[string]*timerJob),
	}
	s.cron.Start()
	return s
}

func (s *localScheduler) ScheduleCron(id, expression string, run func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entryID, err := s.cron.AddFunc(expression, run)
	if err != nil {
		return err
	}
	if previous, ok := s.cronJobs[id]; ok {
		s.cron.Remove(previous)
	}
	if previous, ok := s.timers[id]; ok {
		if previous.timer != nil {
			previous.timer.Stop()
		}
		delete(s.timers, id)
	}
	s.cronJobs[id] = entryID
	return nil
}

func (s *localScheduler) ScheduleAt(id string, at time.Time, run func()) {
	s.mu.Lock()
	if previous, ok := s.cronJobs[id]; ok {
		s.cron.Remove(previous)
		delete(s.cronJobs, id)
	}
	if previous, ok := s.timers[id]; ok {
		if previous.timer != nil {
			previous.timer.Stop()
		}
	}
	job := &timerJob{}
	// Keep the job in the map before starting the timer. This also makes an
	// already-due timer unable to race a replacement and delete the new job.
	s.timers[id] = job
	job.timer = time.AfterFunc(time.Until(at), func() {
		run()
		s.mu.Lock()
		if current, ok := s.timers[id]; ok && current == job {
			delete(s.timers, id)
		}
		s.mu.Unlock()
	})
	s.mu.Unlock()
}

func (s *localScheduler) Unschedule(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entryID, ok := s.cronJobs[id]; ok {
		s.cron.Remove(entryID)
		delete(s.cronJobs, id)
	}
	if timer, ok := s.timers[id]; ok {
		if timer.timer != nil {
			timer.timer.Stop()
		}
		delete(s.timers, id)
	}
}

func (s *localScheduler) Stop() {
	s.mu.Lock()
	for id, entryID := range s.cronJobs {
		s.cron.Remove(entryID)
		delete(s.cronJobs, id)
	}
	for id, timer := range s.timers {
		if timer.timer != nil {
			timer.timer.Stop()
		}
		delete(s.timers, id)
	}
	s.mu.Unlock()
	_ = s.cron.Stop()
}
