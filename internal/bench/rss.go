package bench

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// readRSSMB reads this process's own resident set size from /proc/self/status
// and returns it in megabytes (MiB, but named the way an operator reads a
// ceiling — "32MB" — rather than "32MiB").
//
// A failure to read or parse it (the file does not exist, a non-Linux box, an
// unexpected format) is reported as 0 rather than an error: RSS is one of
// several numbers this package reports, and a box that cannot answer it should
// still get p50/p99/overrun numbers rather than no report at all.
func readRSSMB() float64 {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		return 0
	}
	defer f.Close() // #nosec G307 -- a read-only /proc file; a close error carries nothing to act on

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "VmRSS:") {
			continue
		}
		fields := strings.Fields(line)
		// "VmRSS:", "<kB value>", "kB"
		if len(fields) < 2 {
			return 0
		}
		kb, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			return 0
		}
		return kb / 1024.0
	}
	return 0
}
