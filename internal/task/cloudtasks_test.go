// ABOUTME: Tests for BuildTaskRequest: named-task dedup, OIDC audience, name sanitization.
// ABOUTME: Pure construction only; the CreateTask call is a thin shell over the client.

package task

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	queuePath = "projects/p/locations/europe-west1/queues/cadenza-exec"
	targetURL = "https://cadenza-x.run.app/internal/execute"
	audience  = "https://cadenza-x.run.app"
	invokerSA = "cadenza-invoker@p.iam.gserviceaccount.com"
)

func TestBuildTaskRequest(t *testing.T) {
	e := Envelope{V: 1, Type: TypeTelegramUpdate, ID: "tg-update-777", Payload: json.RawMessage(`{"update_id":777}`)}
	req, err := BuildTaskRequest(queuePath, targetURL, audience, invokerSA, e)
	if err != nil {
		t.Fatalf("BuildTaskRequest: %v", err)
	}
	if req.Parent != queuePath {
		t.Errorf("Parent = %q", req.Parent)
	}
	if want := queuePath + "/tasks/tg-update-777"; req.Task.Name != want {
		t.Errorf("Name = %q, want %q (named task = queue-level dedup)", req.Task.Name, want)
	}
	httpReq := req.Task.GetHttpRequest()
	if httpReq.Url != targetURL {
		t.Errorf("Url = %q", httpReq.Url)
	}
	oidc := httpReq.GetOidcToken()
	if oidc.ServiceAccountEmail != invokerSA {
		t.Errorf("SA = %q", oidc.ServiceAccountEmail)
	}
	if oidc.Audience != "https://cadenza-x.run.app" {
		t.Errorf("Audience = %q, want bare service URL (paths cause silent 401s)", oidc.Audience)
	}
	var back Envelope
	if err := json.Unmarshal(httpReq.Body, &back); err != nil || back.ID != e.ID {
		t.Errorf("body round trip failed: %v %+v", err, back)
	}
}

func TestBuildTaskRequest_SanitizesName(t *testing.T) {
	e := Envelope{V: 1, Type: TypeTelegramUpdate, ID: "send-morning:2026-06-10"}
	req, err := BuildTaskRequest(queuePath, targetURL, audience, invokerSA, e)
	if err != nil {
		t.Fatalf("BuildTaskRequest: %v", err)
	}
	name := req.Task.Name[strings.LastIndex(req.Task.Name, "/")+1:]
	if strings.ContainsAny(name, ":") {
		t.Errorf("task name %q contains invalid chars", name)
	}
}

func TestBuildTaskRequest_RejectsInvalid(t *testing.T) {
	valid := Envelope{V: 1, Type: TypeMorningCheck, ID: "x"}
	if _, err := BuildTaskRequest("", targetURL, audience, invokerSA, valid); err == nil {
		t.Error("empty queuePath accepted")
	}
	if _, err := BuildTaskRequest(queuePath, targetURL, audience, invokerSA, Envelope{}); err == nil {
		t.Error("invalid envelope accepted")
	}
}

func TestTaskName_InjectiveAfterSanitization(t *testing.T) {
	// "a:b" and "a-b" must NOT collide: a collision is silently swallowed
	// as a named-task duplicate and the second envelope is lost.
	a := Envelope{V: 1, Type: TypeMorningCheck, ID: "send-morning:2026-06-10"}
	b := Envelope{V: 1, Type: TypeMorningCheck, ID: "send-morning-2026-06-10"}
	ra, err := BuildTaskRequest(queuePath, targetURL, audience, invokerSA, a)
	if err != nil {
		t.Fatalf("a: %v", err)
	}
	rb, err := BuildTaskRequest(queuePath, targetURL, audience, invokerSA, b)
	if err != nil {
		t.Fatalf("b: %v", err)
	}
	if ra.Task.Name == rb.Task.Name {
		t.Fatalf("distinct ids collided on task name %q", ra.Task.Name)
	}
}

func TestEnqueue_AlreadyExistsIsSuccess(t *testing.T) {
	c := &CloudTasks{
		QueuePath: queuePath, TargetURL: targetURL, Audience: audience, InvokerSA: invokerSA,
		createFn: func(context.Context, *cloudtaskspb.CreateTaskRequest) error {
			return status.Error(codes.AlreadyExists, "task exists")
		},
	}
	e := Envelope{V: 1, Type: TypeTelegramUpdate, ID: "tg-update-1"}
	if err := c.Enqueue(context.Background(), e); err != nil {
		t.Fatalf("AlreadyExists must be success (named-task dedup), got %v", err)
	}
}

func TestEnqueue_OtherErrorsWrapped(t *testing.T) {
	c := &CloudTasks{
		QueuePath: queuePath, TargetURL: targetURL, Audience: audience, InvokerSA: invokerSA,
		createFn: func(context.Context, *cloudtaskspb.CreateTaskRequest) error {
			return status.Error(codes.Unavailable, "queue down")
		},
	}
	e := Envelope{V: 1, Type: TypeTelegramUpdate, ID: "tg-update-2"}
	if err := c.Enqueue(context.Background(), e); err == nil {
		t.Fatal("transient queue error swallowed")
	}
}

func TestEnqueueAt_SetsScheduleTime(t *testing.T) {
	var got *cloudtaskspb.CreateTaskRequest
	c := &CloudTasks{
		QueuePath: queuePath, TargetURL: targetURL, Audience: audience, InvokerSA: invokerSA,
		createFn: func(_ context.Context, r *cloudtaskspb.CreateTaskRequest) error {
			got = r
			return nil
		},
	}
	at := time.Date(2026, 6, 11, 7, 45, 0, 0, time.UTC)
	e := Envelope{V: 1, Type: TypeMorningCheck, ID: "morning-2026-06-11-r1"}
	if err := c.EnqueueAt(context.Background(), e, at); err != nil {
		t.Fatalf("EnqueueAt: %v", err)
	}
	if got.Task.ScheduleTime == nil || !got.Task.ScheduleTime.AsTime().Equal(at) {
		t.Errorf("ScheduleTime = %v, want %v", got.Task.ScheduleTime, at)
	}
}
