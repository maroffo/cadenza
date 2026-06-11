// ABOUTME: Cloud Tasks enqueuer: named tasks for dedup, OIDC-authenticated HTTP target.
// ABOUTME: BuildTaskRequest is pure and unit-tested; the client call is a thin shell.

package task

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type CloudTasks struct {
	Client    *cloudtasks.Client
	QueuePath string // projects/<p>/locations/<l>/queues/<q>
	TargetURL string // https://<service>/internal/execute
	Audience  string // the bare service URL the executor validates against
	InvokerSA string // cadenza-invoker@<p>.iam.gserviceaccount.com

	// createFn overrides the API call in tests; nil means the real client.
	createFn func(context.Context, *cloudtaskspb.CreateTaskRequest) error
}

func (c *CloudTasks) Enqueue(ctx context.Context, e Envelope) error {
	req, err := BuildTaskRequest(c.QueuePath, c.TargetURL, c.Audience, c.InvokerSA, e)
	if err != nil {
		return err
	}
	return c.create(ctx, req)
}

// EnqueueAt schedules the task for future dispatch (the +45min HRV retry).
func (c *CloudTasks) EnqueueAt(ctx context.Context, e Envelope, at time.Time) error {
	req, err := BuildTaskRequest(c.QueuePath, c.TargetURL, c.Audience, c.InvokerSA, e)
	if err != nil {
		return err
	}
	req.Task.ScheduleTime = timestamppb.New(at)
	return c.create(ctx, req)
}

func (c *CloudTasks) create(ctx context.Context, req *cloudtaskspb.CreateTaskRequest) error {
	create := c.createFn
	if create == nil {
		create = func(ctx context.Context, r *cloudtaskspb.CreateTaskRequest) error {
			_, err := c.Client.CreateTask(ctx, r)
			return err
		}
	}
	err := create(ctx, req)
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

// TelegramUpdateID builds the canonical envelope/dedup id for a Telegram
// update. Webhook (prod) and polling (dev) MUST share it: it is both the
// Cloud Tasks task name and the Firestore dedup key.
func TelegramUpdateID(updateID int64) string {
	return fmt.Sprintf("tg-update-%d", updateID)
}

// BuildTaskRequest constructs the CreateTask request: named task (24h-window
// dedup at the queue), OIDC token for the in-app executor validation. The
// audience must be the bare service URL: paths or query params in the
// audience cause silent 401s at the executor.
func BuildTaskRequest(queuePath, targetURL, audience, invokerSA string, e Envelope) (*cloudtaskspb.CreateTaskRequest, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if queuePath == "" || targetURL == "" || audience == "" || invokerSA == "" {
		return nil, fmt.Errorf("cloudtasks: queuePath, targetURL, audience and invokerSA are all required")
	}
	body, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("cloudtasks: marshal envelope: %w", err)
	}
	return &cloudtaskspb.CreateTaskRequest{
		Parent: queuePath,
		Task: &cloudtaskspb.Task{
			Name: queuePath + "/tasks/" + taskName(e.ID),
			MessageType: &cloudtaskspb.Task_HttpRequest{
				HttpRequest: &cloudtaskspb.HttpRequest{
					Url:        targetURL,
					HttpMethod: cloudtaskspb.HttpMethod_POST,
					Headers:    map[string]string{"Content-Type": "application/json"},
					Body:       body,
					AuthorizationHeader: &cloudtaskspb.HttpRequest_OidcToken{
						OidcToken: &cloudtaskspb.OidcToken{
							ServiceAccountEmail: invokerSA,
							Audience:            audience,
						},
					},
				},
			},
		},
	}, nil
}

// taskName sanitizes an envelope id into the Cloud Tasks charset. When
// sanitization changes anything, a hash suffix keeps distinct ids distinct:
// a lossy collision would be silently swallowed as a named-task duplicate.
func taskName(id string) string {
	clean := taskNameSanitizer.ReplaceAllString(id, "-")
	if clean == id {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%s-%x", clean, sum[:4])
}
