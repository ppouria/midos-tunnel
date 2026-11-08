package main

import (
	"io"
	"time"
)

const defaultStall = 120 * time.Second

type writeDeadliner interface{ SetWriteDeadline(time.Time) error }

type stallWriter struct {
	w     io.Writer
	d     writeDeadliner
	limit time.Duration
}

func (s stallWriter) Write(p []byte) (int, error) {
	_ = s.d.SetWriteDeadline(time.Now().Add(s.limit))
	return s.w.Write(p)
}

func stallGuard(w io.Writer, target any, limit time.Duration) io.Writer {
	d, ok := target.(writeDeadliner)
	if !ok || limit <= 0 {
		return w
	}
	return stallWriter{w: w, d: d, limit: limit}
}

func (c *Config) stallLimit() time.Duration {
	switch {
	case c.StallSec < 0:
		return 0
	case c.StallSec == 0:
		return defaultStall
	}
	return time.Duration(c.StallSec) * time.Second
}
