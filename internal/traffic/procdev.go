package traffic

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// procNetDevColumns is how many numbers follow the interface name in
// /proc/net/dev: eight received, eight transmitted. The layout has been the
// same since Linux 2.2 and the header line above the rows names every one of
// them, but the header is not parsed — a file whose columns had moved would
// need a new parser, not a re-reading of its own header.
const procNetDevColumns = 16

// ParseProcNetDev reads the interface counter table the kernel exports at
// /proc/net/dev.
//
// The file is two header lines and then one row per interface:
//
//	Inter-|   Receive                    |  Transmit
//	 face |bytes packets errs drop fifo frame compressed multicast|bytes ...
//	    lo: 100973976361 19198517 0 0 0 0 0 0 100973976361 19198517 0 0 0 0 0 0
//	enp44s0: 103428362057 89935609 106952 45 0 101333 0 1159178 27584617236 ...
//
// Two things about that layout are worth knowing, because both are places a
// naive split goes wrong. The name is separated from the first number by a
// colon and not by a space, and on a long interface name there is no space
// after it at all, so the row is cut at the first colon rather than
// tokenised. And the columns are right-aligned into a fixed width the kernel
// chose decades ago, so a machine that has moved a petabyte overflows the
// field and runs its first two numbers together; a row whose numbers do not
// parse is skipped rather than failing the whole read, because one broken
// interface must not cost the user the other twenty.
func ParseProcNetDev(data string) ([]Interface, error) {
	var (
		interfaces []Interface
		seen       = map[string]bool{}
		rows       int
	)
	for line := range strings.Lines(data) {
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			// The two header lines, and any trailing blank one.
			continue
		}
		name = strings.TrimSpace(name)
		// An interface name is one word. The header's own second line
		// carries a "|" and would otherwise look like a row named
		// "face |bytes ... multicast", and a name with whitespace inside it —
		// which the fuzzer found before a machine did — would break the row
		// it is drawn into.
		if name == "" || strings.ContainsFunc(name, unicode.IsSpace) ||
			strings.ContainsRune(name, '|') {
			continue
		}
		rows++

		fields := strings.Fields(rest)
		if len(fields) < procNetDevColumns {
			continue
		}
		values := make([]uint64, procNetDevColumns)
		malformed := false
		for i := range procNetDevColumns {
			value, err := strconv.ParseUint(fields[i], 10, 64)
			if err != nil {
				malformed = true
				break
			}
			values[i] = value
		}
		if malformed || seen[name] {
			continue
		}
		seen[name] = true
		interfaces = append(interfaces, Interface{
			Name: name,
			RX: Counters{
				Bytes: values[0], Packets: values[1], Errors: values[2],
				Dropped: values[3], Multicast: values[7],
			},
			TX: Counters{
				Bytes: values[8], Packets: values[9], Errors: values[10],
				Dropped: values[11],
			},
		})
	}
	// A file with rows in it and nothing parsable out of them is a parser
	// that has met a layout it does not know, and saying so is better than
	// reporting a machine with no interfaces at all. A file with no rows —
	// which is what an empty or truncated read looks like — is reported the
	// same way for the same reason: this file always has at least loopback.
	if len(interfaces) == 0 {
		return nil, fmt.Errorf("no interface rows in /proc/net/dev (%d candidate lines)", rows)
	}
	return interfaces, nil
}
