// question_compose_intent_test.go — ADR-195 WS7 (D7) runCompose intent-source.
// composeIntent resolves the compose discriminant from (in precedence):
//  1. the persisted job.Intent (WS3 + migration 0022 backfill) — the canonical
//     source, available on the loaded row;
//  2. an explicit intent on the event (the .v2 contract).
//
// The legacy legacyJobTypeIntent(job_type) shim that previously backed a third
// tier was retired in WS9 step 3, so an unresolved intent now fails loud in
// runCompose rather than falling back to a job_type.
//
// Internal test (package pubsub) — composeIntent is unexported.
package pubsub

import (
	"testing"

	"github.com/apollo-chora/chora-creation/internal/domain/question"
)

func TestComposeIntent_PrefersJobThenEvent(t *testing.T) {
	cases := []struct {
		name      string
		jobIntent question.Intent
		evtIntent string
		want      question.Intent
	}{
		{"persisted job.Intent wins", question.IntentModelAnswerFill, "", question.IntentModelAnswerFill},
		{"no job intent -> explicit event intent (v2)", "", "image_regen", question.IntentImageRegen},
		{"invalid job intent ignored -> event intent", question.Intent("not_an_intent"), "image_regen", question.IntentImageRegen},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			job := &question.ComposeJob{Intent: c.jobIntent}
			evt := QuestionGenerationRequestedEvent{Intent: c.evtIntent}
			if got := composeIntent(job, evt); got != c.want {
				t.Errorf("composeIntent = %q; want %q", got, c.want)
			}
		})
	}
}
