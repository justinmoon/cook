package events

import (
	"testing"
)

func TestSubjectFor(t *testing.T) {
	b := &Bus{active: true}

	tests := []struct {
		event Event
		want  string
	}{
		// Branch events
		{
			Event{Type: EventBranchCreated, Repo: "alice/myrepo", Branch: "feature-x"},
			"cook.branch.alice.myrepo.feature-x.branch.created",
		},
		{
			Event{Type: EventBranchMerged, Repo: "org/project", Branch: "fix-123"},
			"cook.branch.org.project.fix-123.branch.merged",
		},

		// Gate events
		{
			Event{Type: EventGateStarted, Repo: "alice/myrepo", Branch: "feature-x", GateName: "lint"},
			"cook.gate.alice.myrepo.feature-x.lint.gate.started",
		},
		{
			Event{Type: EventGatePassed, Repo: "alice/myrepo", Branch: "feature-x", GateName: "test"},
			"cook.gate.alice.myrepo.feature-x.test.gate.passed",
		},
		{
			Event{Type: EventGateFailed, Repo: "alice/myrepo", Branch: "feature-x", GateName: "build"},
			"cook.gate.alice.myrepo.feature-x.build.gate.failed",
		},

		// Agent events
		{
			Event{Type: EventAgentStarted, Repo: "alice/myrepo", Branch: "feature-x"},
			"cook.agent.alice.myrepo.feature-x.agent.started",
		},

		// Task events
		{
			Event{Type: EventTaskCreated, Repo: "alice/myrepo", TaskID: "task-1"},
			"cook.task.alice.myrepo.task-1.task.created",
		},
	}

	for _, tc := range tests {
		t.Run(string(tc.event.Type), func(t *testing.T) {
			got := b.subjectFor(tc.event)
			if got != tc.want {
				t.Errorf("subjectFor(%+v) = %q, want %q", tc.event, got, tc.want)
			}
		})
	}
}
