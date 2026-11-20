package emberops

import "time"

// SetClock lets a test move time.
func SetClock(c *ClientCredentials, now func() time.Time) { c.now = now }
