package audit_test

import (
	"context"
	"errors"
	"testing"

	"github.com/martinsuchenak/exemplar/internal/apperr"
	"github.com/martinsuchenak/exemplar/internal/audit"
	"github.com/martinsuchenak/exemplar/internal/dataset"
	"github.com/martinsuchenak/exemplar/internal/testutil"
)

var ctx = context.Background()

func TestListFilters(t *testing.T) {
	a := testutil.NewApp(t)
	admin := testutil.Admin(t, a)
	alice := testutil.Member(t, a, admin, "alice")
	d, err := a.Datasets.Create(ctx, alice, dataset.CreateInput{Name: "mine"})
	if err != nil {
		t.Fatalf("create dataset: %v", err)
	}

	all, err := a.Audit.List(ctx, admin, audit.Filter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("all events = %d, %v; want 4 (bootstrap, account, key, dataset)", len(all), err)
	}
	for i := 1; i < len(all); i++ {
		if all[i].Seq <= all[i-1].Seq {
			t.Errorf("events out of order: %d after %d", all[i].Seq, all[i-1].Seq)
		}
	}
	last := all[len(all)-1]
	if last.Action != audit.ActionDatasetCreate || last.AccountID != alice.AccountID || last.TargetID != d.ID {
		t.Errorf("last event = %+v", last)
	}
	if last.Details["name"] != "mine" || last.At.IsZero() || last.ID == "" {
		t.Errorf("event details/time/id not stored: %+v", last)
	}

	tests := []struct {
		name string
		f    audit.Filter
		want int
	}{
		{"by account", audit.Filter{AccountID: alice.AccountID}, 1},
		{"by action", audit.Filter{Action: audit.ActionKeyCreate}, 1},
		{"by target type", audit.Filter{TargetType: "account"}, 2},
		{"by target id", audit.Filter{TargetID: d.ID}, 1},
		{"after seq", audit.Filter{AfterSeq: all[1].Seq}, 2},
		{"limit", audit.Filter{Limit: 2}, 2},
		{"limit over max is capped", audit.Filter{Limit: 1_000_000}, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := a.Audit.List(ctx, admin, tt.f)
			if err != nil || len(got) != tt.want {
				t.Errorf("List(%+v) = %d events, %v; want %d", tt.f, len(got), err, tt.want)
			}
		})
	}

	if _, err := a.Audit.List(ctx, alice, audit.Filter{}); !errors.Is(err, apperr.ErrForbidden) {
		t.Errorf("member list err = %v, want ErrForbidden", err)
	}
}
