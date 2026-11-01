// Package queue holds print jobs until a printer picks them up.
package queue

import (
	"errors"
	"sync"

	"example.com/printq/pages"
)

// Job is one document to print.
type Job struct {
	ID    string
	Pages []int
}

// Queue is a FIFO of jobs, safe for concurrent use.
type Queue struct {
	mu   sync.Mutex
	jobs []Job
}

// ErrEmpty is returned by Next when no job is waiting.
var ErrEmpty = errors.New("queue is empty")

// Submit parses the page selection and enqueues the job.
func (q *Queue) Submit(id, selection string, totalPages int) error {
	ps, err := pages.Parse(selection, totalPages)
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, Job{ID: id, Pages: ps})
	return nil
}

// Next removes and returns the oldest job.
func (q *Queue) Next() (Job, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.jobs) == 0 {
		return Job{}, ErrEmpty
	}
	j := q.jobs[0]
	q.jobs = q.jobs[1:]
	return j, nil
}
