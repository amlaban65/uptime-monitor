// Package clock abstracts time so that scheduling logic can be tested
// deterministically with a fake clock.
package clock

import "time"

// Clock is the subset of the time package the application depends on.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Real is a Clock backed by the time package.
type Real struct{}

func (Real) Now() time.Time                         { return time.Now() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }
