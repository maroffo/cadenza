// ABOUTME: Tests for BuildTaskRequest: named-task dedup, OIDC audience, name sanitization.
// ABOUTME: Pure construction only; the CreateTask call is a thin shell over the client.

package task

import (
	"encoding/json"
	"strings"
	"testing"
)

const (
	queuePath = "projects/p/locations/europe-west1/queues/cadenza-exec"
	targetURL = "https://cadenza-x.run.app/internal/execute"
	invokerSA = "cadenza-invoker@p.iam.gserviceaccount.com"
)

func TestBuildTaskRequest(t *testing.T) {
	e := Envelope{V: 1, Type: TypeTelegramUpdate, ID: "tg-update-777", Payload: json.RawMessage(`{"update_id":777}`)}
	req, err := BuildTaskRequest(queuePath, targetURL, invokerSA, e)
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
	req, err := BuildTaskRequest(queuePath, targetURL, invokerSA, e)
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
	if _, err := BuildTaskRequest("", targetURL, invokerSA, valid); err == nil {
		t.Error("empty queuePath accepted")
	}
	if _, err := BuildTaskRequest(queuePath, targetURL, invokerSA, Envelope{}); err == nil {
		t.Error("invalid envelope accepted")
	}
}
