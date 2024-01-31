package zcron

import (
	"context"
	"fmt"
	"github.com/gorhill/cronexpr"
	"regexp"
	"strings"
	"sync"
	"time"
)

/*
 * @Author: zenor
 * @Date: 2024-01-10 10:00:00
 * @LastEditors: zenor
 * @LastEditTime: 2024-01-10 10:00:00
 * @Description:根据github.com/robfig/cron重写了一个,一些新的cron标准比如L,W,年份有效
 * @FilePath: /zcron/cron.go
 * Copyright (c) 2023 by ${git_name_email}, All Rights Reserved.
 */

// Cron keeps track of any number of entries, invoking the associated func as
// specified by the schedule. It may be started, stopped, and the entries may
// be inspected while running.
type Cron struct {
	entries []*Entry
	//chain     Chain
	stop     chan struct{}
	add      chan *Entry
	remove   chan EntryID
	snapshot chan chan []Entry
	running  bool
	//logger    Logger
	runningMu sync.Mutex
	location  *time.Location
	//parser    ScheduleParser
	nextID    EntryID
	jobWaiter sync.WaitGroup
}

// EntryID identifies an entry within a Cron instance
type EntryID int

// Entry consists of a schedule and the func to execute on that schedule.
type Entry struct {
	// ID is the cron-assigned ID of this entry, which may be used to look up a
	// snapshot or remove it.
	ID EntryID

	// Schedule on which this job should be run.
	CronExpr *cronexpr.Expression

	// Next time the job will run, or the zero time if Cron has not been
	// started or this entry's schedule is unsatisfiable
	Next time.Time

	// Prev is the last time this job was run, or the zero time if never.
	Prev time.Time

	// WrappedJob is the thing to run when the Schedule is activated.
	WrappedJob Job

	// Job is the thing that was submitted to cron.
	// It is kept around so that user code that needs to get at the job later,
	// e.g. via Entries() can do so.
	Job Job
}

// Job is an interface for submitted cron jobs.
type Job interface {
	Run()
}

// FuncJob is a wrapper that turns a func() into a cron.Job
type FuncJob func()

func (f FuncJob) Run() { f() }

// AddFunc adds a func to the Cron to be run on the given schedule.
// The spec is parsed using the time zone of this Cron instance as the default.
// An opaque ID is returned that can be used to later remove it.
func (c *Cron) AddFunc(spec string, cmd func()) (EntryID, error) {
	return c.AddJob(spec, FuncJob(cmd))
}

// AddJob adds a Job to the Cron to be run on the given schedule.
// The spec is parsed using the time zone of this Cron instance as the default.
// An opaque ID is returned that can be used to later remove it.
func (c *Cron) AddJob(spec string, cmd Job) (EntryID, error) {
	re := regexp.MustCompile(`\s+`)
	result := re.Split(spec, -1)
	if len(result) == 6 {
		//如果生成的表达式是6位
		//则删除第一个位置(秒位)
		result = append(result[:0], result[1:]...)
		spec = strings.Join(result, " ")
	}
	expr, err := cronexpr.Parse(spec)
	if err != nil {
		return 0, err
	}
	nextTime := expr.Next(time.Now())
	if nextTime.IsZero() {
		return 0, fmt.Errorf("cron: Spec %q is invalid", spec)
	}
	//fmt.Printf("next run time:%+v", expr.NextN(time.Now(), 5))
	entry := &Entry{
		ID:         c.nextID,
		CronExpr:   expr,
		WrappedJob: cmd,
		Job:        cmd,
	}
	c.runningMu.Lock()
	defer c.runningMu.Unlock()
	c.nextID++
	if !c.running {
		c.entries = append(c.entries, entry)
	} else {
		c.add <- entry
	}
	return entry.ID, nil
}

func New() *Cron {
	c := &Cron{
		entries: nil,
		//chain:     NewChain(),
		add:       make(chan *Entry),
		stop:      make(chan struct{}),
		snapshot:  make(chan chan []Entry),
		remove:    make(chan EntryID),
		running:   false,
		runningMu: sync.Mutex{},
		//logger:    DefaultLogger,
		location: time.Local,
		//parser:    standardParser,
	}
	return c
}

// Start the cron scheduler in its own goroutine, or no-op if already started.
func (c *Cron) Start() {
	c.runningMu.Lock()
	defer c.runningMu.Unlock()
	if c.running {
		return
	}
	c.running = true
	go c.run()
}

func (c *Cron) startJob(j Job) {
	c.jobWaiter.Add(1)
	go func() {
		defer c.jobWaiter.Done()
		j.Run()
	}()
}

func (c *Cron) Stop() context.Context {
	c.runningMu.Lock()
	defer c.runningMu.Unlock()
	if c.running {
		c.stop <- struct{}{}
		c.running = false
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		c.jobWaiter.Wait()
		cancel()
	}()
	return ctx
}

// Remove an entry from being run in the future.
func (c *Cron) Remove(id EntryID) {
	c.runningMu.Lock()
	defer c.runningMu.Unlock()
	if c.running {
		c.remove <- id
	} else {
		c.removeEntry(id)
	}
}

// RemoveAll removes all entries from the cron
func (c *Cron) RemoveAll() bool {
	c.runningMu.Lock()
	defer c.runningMu.Unlock()
	for _, entry := range c.entries {
		if c.running {
			c.remove <- entry.ID
		} else {
			c.removeEntry(entry.ID)
		}
	}
	return true
}

func (c *Cron) removeEntry(id EntryID) {
	var entries []*Entry
	for _, e := range c.entries {
		if e.ID != id {
			entries = append(entries, e)
		}
	}
	c.entries = entries
}

func (c *Cron) run() {
	now := time.Now().In(c.location)
	for _, entry := range c.entries {
		entry.Next = entry.CronExpr.Next(now)
	}
	for {
		var timer *time.Timer
		if len(c.entries) == 0 || c.entries[0].Next.IsZero() {
			// If there are no entries yet, just sleep - it still handles new entries
			// and stop requests.
			timer = time.NewTimer(100000 * time.Hour)
		} else {
			timer = time.NewTimer(c.entries[0].Next.Sub(now))
		}
		for {
			select {
			case now = <-timer.C:
				now = now.In(c.location)
				//c.logger.Info("wake", "now", now)

				// Run every entry whose next time was less than now
				for _, e := range c.entries {
					if e.Next.After(now) || e.Next.IsZero() {
						break
					}
					c.startJob(e.WrappedJob)
					e.Prev = e.Next
					e.Next = e.CronExpr.Next(now)
					//c.logger.Info("run", "now", now, "entry", e.ID, "next", e.Next)
				}
			case newEntry := <-c.add:
				timer.Stop()
				now = time.Now().In(c.location)
				newEntry.Next = newEntry.CronExpr.Next(now)
				c.entries = append(c.entries, newEntry)
				//c.logger.Info("added", "now", now, "entry", newEntry.ID, "next", newEntry.Next)
			case <-c.stop:
				timer.Stop()
				//c.logger.Info("stop")
				return
			case id := <-c.remove:
				timer.Stop()
				now = time.Now().In(c.location)
				c.removeEntry(id)
				//c.logger.Info("removed", "entry", id)
			}
		}
	}
}
