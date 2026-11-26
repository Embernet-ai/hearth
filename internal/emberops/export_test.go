package emberops

import "time"

// SetClock lets a test move time.
func SetClock(c *ClientCredentials, now func() time.Time) { c.now = now }

// SetTokenRetryDelays swaps the waits between token attempts and returns the old ones.
func SetTokenRetryDelays(d []time.Duration) []time.Duration {
	old := tokenRetryDelays
	tokenRetryDelays = d
	return old
}
