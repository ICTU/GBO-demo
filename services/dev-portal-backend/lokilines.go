package main

import (
	"regexp"
	"strings"
)

// dockerLogChunk is the size at which Docker's json-file driver splits a
// stdout line into partial messages.
const dockerLogChunk = 16 * 1024

var dockerTimestampPrefix = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z `)

// repairDockerSplits undoes what promtail's Docker target does to a line
// longer than one Docker log chunk. It rejoins the partial messages but
// leaves each continuation's timestamp in between, so a console decision-log
// line of more than 16 KiB reaches Loki with "<RFC 3339 time> " spliced in
// at every chunk boundary. Where that lands between a key and its colon the
// line no longer parses; where it lands inside a string, the line parses
// with a corrupted value.
func repairDockerSplits(line string) string {
	if len(line) <= dockerLogChunk {
		return line
	}
	var b strings.Builder
	b.Grow(len(line))
	for len(line) > dockerLogChunk {
		b.WriteString(line[:dockerLogChunk])
		line = line[dockerLogChunk:]
		if loc := dockerTimestampPrefix.FindStringIndex(line); loc != nil {
			line = line[loc[1]:]
		}
	}
	b.WriteString(line)
	return b.String()
}
