package domain

import "time"

type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time {
	return time.Now().UTC()
}

type FixedClock struct {
	At time.Time
}

func (c FixedClock) Now() time.Time {
	return UTC(c.At)
}
