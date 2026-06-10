// ABOUTME: Cloud Tasks enqueuer: named tasks for dedup, OIDC-authenticated HTTP target.
// ABOUTME: BuildTaskRequest is pure and unit-tested; the client call is a thin shell.

package task

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type CloudTasks struct {
	Client    *cloudtasks.Client
	QueuePath string // projects/<p>/locations/<l>/queues/<q>
	TargetURL string // https://<service>/internal/execute
	InvokerSA string // cadenza-invoker@<p>.iam.gserviceaccount.com
}

func (c *CloudTasks) Enqueue(ctx context.Context, e Envelope) error {
	req, err := BuildTaskRequest(c.QueuePath, c.TargetURL, c.InvokerSA, e)
	if err != nil {
		return err
	}
	_, err = c.Client.CreateTask(ctx, req)
	if status.Code(err) == codes.AlreadyExists {
		// Named-task dedup: this delivery already happened. Success.
		return nil
	}
	if err != nil {
		return fmt.Errorf("cloudtasks create: %w", err)
	}
	return nil
}

// Task ids must match [A-Za-z0-9_-]; anything else becomes '-'.
var taskNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// BuildTaskRequest constructs the CreateTask request: named task (24h-window
// dedup at the queue), OIDC token for the in-app executor validation.
func BuildTaskRequest(queuePath, targetURL, invokerSA string, e Envelope) (*cloudtaskspb.CreateTaskRequest, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if queuePath == "" || targetURL == "" || invokerSA == "" {
		return nil, fmt.Errorf("cloudtasks: queuePath, targetURL and invokerSA are all required")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("cloudtasks: marshal envelope: %w", err)
	}
	return &cloudtaskspb.CreateTaskRequest{
		Parent: queuePath,
		Task: &cloudtaskspb.Task{
			Name: queuePath + "/tasks/" + taskNameSanitizer.ReplaceAllString(e.ID, "-"),
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{
					Url:        targetURL,
					HttpMethod: cloudtaskspb.HttpMethod_POST,
					Headers:    map[string]string{"Content-Type": "application/json"},
					Body:       body,
					AuthorizationHeader: &cloudtaskspb.HttpRequest_OidcToken{
						OidcToken: &cloudtaskspb.OidcToken{
							ServiceAccountEmail: invokerSA,
							Audience:            audienceFromURL(targetURL),
						},
					},
				},
			},
		},
	}, nil
}

// audienceFromURL strips the path: the executor validates against the bare
// service URL (query params or paths in the audience cause silent 401s).
func audienceFromURL(targetURL string) string {
	re := regexp.MustCompile(`^(https?://[^/]+)`)
	if m := re.FindString(targetURL); m != "" {
		return m
	}
	return targetURL
}
