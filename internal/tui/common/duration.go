package common

import (
	"fmt"
	"time"
)

// FormatCompactDuration renders a short elapsed-time label for the header:
// under a minute as "12s", otherwise "1m 5s".
func FormatCompactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	sec := int(d.Seconds())
	if sec < 60 {
		return fmt.Sprintf("%ds", sec)
	}
	return fmt.Sprintf("%dm %ds", sec/60, sec%60)
}
