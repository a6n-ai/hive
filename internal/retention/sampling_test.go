package retention_test

import (
	"fmt"
	"testing"
)

// TestOldPromptSampledBeforeDelete: an event may only be deleted once its prompt
// text has been sampled and cleared, whatever the id order or clock.
func TestOldPromptSampledBeforeDelete(t *testing.T) {
	for _, clock := range []string{"original", "stored"} {
		t.Run(clock, func(t *testing.T) {
			f := newFixture(t)
			var ids []int64
			// Newer-stored, older-original prompts, inserted out of time order.
			ids = append(ids, f.event(now.Add(-200*day), now.Add(-1*day), "stored recently").ID)
			ids = append(ids, f.event(now.Add(-400*day), now.Add(-400*day), "stored long ago").ID)
			ids = append(ids, f.event(now.Add(-500*day), now.Add(-1*day), "oldest by ts").ID)
			cfg := f.conf
			cfg.PromptClock = clock
			f.apply(cfg, false)
			for _, id := range ids {
				exists := f.count(fmt.Sprintf(`SELECT count(*) FROM events WHERE id = %d`, id)) == 1
				sampled := f.count(fmt.Sprintf(`SELECT count(*) FROM replay_prompts WHERE event_id = %d`, id)) == 1
				if !exists && !sampled {
					t.Errorf("event %d deleted without its prompt text being sampled", id)
				}
			}
		})
	}
}
