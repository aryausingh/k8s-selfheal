package safety

import "time"

// Clock keeps the production code on real time while allowing instant unit tests.
type Clock interface {
	Now() time.Time
	After(time.Duration) <-chan time.Time
}

type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now()
}

func (RealClock) After(delay time.Duration) <-chan time.Time {
	return time.After(delay)
}
