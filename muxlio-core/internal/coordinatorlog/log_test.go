package coordinatorlog

import (
	"bytes"
	"strings"
	"testing"
)

func TestCoordinatorLogLifecycleFieldsAndSecretRedaction(t *testing.T) {
	resetForTest()
	var buf bytes.Buffer
	SetOutput(&buf)
	RegisterSecrets(map[string]string{"apiKey": "super-secret-value-12345"})

	RunAccepted("run_abc", "demo")
	RunStateChanged("run_abc", "created", "running")
	ActivationStarted("run_abc", "run_abc:node1")
	AttemptStarted("run_abc", "run_abc:node1", "fake/echo", 1)
	WorkerAssigned("run_abc", "run_abc:node1", "local", "fake/echo", "run_abc:node1-attempt-1")
	WorkerCompleted("run_abc", "run_abc:node1", "local", "fake/echo", "run_abc:node1-attempt-1", "succeeded")
	AttemptFinished("run_abc", "run_abc:node1", "fake/echo", 1, "succeeded", "")
	ActivationFinished("run_abc", "run_abc:node1", "succeeded")
	RunFailed("run_abc", "boom token=super-secret-value-12345 Bearer deadbeef")

	log := buf.String()
	for _, needle := range []string{
		`"runId":"run_abc"`,
		`"activationId":"run_abc:node1"`,
		`"workerId":"local"`,
		`"executor":"fake/echo"`,
		`"attempt":1`,
		"run.accepted",
		"run.state_changed",
		"activation.started",
		"activation.finished",
		"attempt.started",
		"attempt.finished",
		"worker.assigned",
		"worker.completed",
		"run.failed",
	} {
		if !strings.Contains(log, needle) {
			t.Fatalf("log missing %q:\n%s", needle, log)
		}
	}
	if strings.Contains(log, "super-secret-value-12345") {
		t.Fatalf("secret value leaked into coordinator log:\n%s", log)
	}
	if strings.Contains(log, "Bearer deadbeef") {
		t.Fatalf("bearer token leaked into coordinator log:\n%s", log)
	}
}
