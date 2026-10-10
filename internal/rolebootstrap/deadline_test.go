package rolebootstrap_test

import (
	"context"
	"testing"
	"time"

	"github.com/malcolmjefferson/therapy_service_hours_backend/internal/rolebootstrap"
)

func TestWithResponseMargin_EndsBeforeTheLambdaDeadline(t *testing.T) {
	t.Parallel()
	deadline := time.Now().Add(3 * time.Minute)
	parent, cancelParent := context.WithDeadline(context.Background(), deadline)
	defer cancelParent()

	ctx, cancel := rolebootstrap.WithResponseMargin(parent, rolebootstrap.ResponseMargin)
	defer cancel()
	got, ok := ctx.Deadline()
	if !ok || !got.Equal(deadline.Add(-rolebootstrap.ResponseMargin)) {
		t.Errorf("deadline = %v, want %v", got, deadline.Add(-rolebootstrap.ResponseMargin))
	}
}

func TestWithResponseMargin_LeavesContextsWithoutRoomUnchanged(t *testing.T) {
	t.Parallel()
	if ctx, cancel := rolebootstrap.WithResponseMargin(context.Background(), time.Second); ctx != context.Background() {
		t.Error("a context without a deadline should be returned as is")
	} else {
		cancel()
	}
	short, cancelShort := context.WithTimeout(context.Background(), time.Second)
	defer cancelShort()
	if ctx, cancel := rolebootstrap.WithResponseMargin(short, 5*time.Second); ctx != short {
		t.Error("a context with less than the margin left should be returned as is")
	} else {
		cancel()
	}
}
