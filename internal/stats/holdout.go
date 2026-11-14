package stats

import (
	"fmt"
	"hash/fnv"
)

// Draw reports whether a prompt is held out. A variable so tests can force the outcome.
var Draw = func(session, digest string, index int, share float64) bool {
	h := fnv.New64a()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%d", session, digest, index)
	return h.Sum64()%10000 < uint64(share*10000)
}
