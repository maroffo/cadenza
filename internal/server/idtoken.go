// ABOUTME: Production TokenValidator backed by Google's idtoken verification.
// ABOUTME: The classic silent failure is an audience mismatch; keep audience = bare service URL.

package server

import (
	"context"
	"fmt"

	"google.golang.org/api/idtoken"
)

type GoogleValidator struct{}

func (GoogleValidator) Validate(ctx context.Context, token, audience string) (string, error) {
	payload, err := idtoken.Validate(ctx, token, audience)
	if err != nil {
		return "", err
	}
	email, _ := payload.Claims["email"].(string)
	if email == "" {
		return "", fmt.Errorf("idtoken: no email claim")
	}
	if verified, _ := payload.Claims["email_verified"].(bool); !verified {
		return "", fmt.Errorf("idtoken: email claim not verified")
	}
	return email, nil
}
