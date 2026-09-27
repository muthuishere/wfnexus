package store

import (
	"context"
	"errors"
	"testing"
)

func TestAProposalRoundTripsAndIsDecidedOnlyOnce(t *testing.T) {
	st := openSQLite(t)
	ctx := context.Background()
	score := 0.82
	p := &Proposal{Project: "demo", Workflow: "hello", Kind: "edit", Branch: "wfx/edit-hello-1", Base: "main",
		ReviewVerdict: "approve", ReviewReason: "fine", ReviewScore: &score, CreatedBy: "ana"}
	if err := st.CreateProposal(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetProposal(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "pending" || got.ReviewScore == nil || *got.ReviewScore != score || got.CreatedBy != "ana" || got.DecidedAt != nil {
		t.Fatalf("round trip = %+v", got)
	}
	if rows, _ := st.ListProposals(ctx, ProposalFilter{Project: "demo", Workflow: "hello"}); len(rows) != 1 {
		t.Fatalf("listing by project and workflow = %d rows", len(rows))
	}
	if rows, _ := st.ListProposals(ctx, ProposalFilter{Project: "other"}); len(rows) != 0 {
		t.Fatalf("another project's listing shows %d rows", len(rows))
	}
	if err := st.DecideProposal(ctx, p.ID, "rejected", "bo", "not now", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.DecideProposal(ctx, p.ID, "merged", "cy", "", ""); !errors.Is(err, ErrProposalDecided) {
		t.Fatalf("a second decision = %v, want ErrProposalDecided", err)
	}
	got, _ = st.GetProposal(ctx, p.ID)
	if got.Status != "rejected" || got.DecidedBy != "bo" || got.Reason != "not now" || got.DecidedAt == nil {
		t.Fatalf("decided = %+v", got)
	}
}

func TestAnUnreviewedProposalHasNoScore(t *testing.T) {
	st := openSQLite(t)
	p := &Proposal{Project: "demo", Workflow: "hello", Kind: "create", Branch: "b"}
	if err := st.CreateProposal(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetProposal(context.Background(), p.ID)
	if got.ReviewVerdict != "unreviewed" || got.ReviewScore != nil {
		t.Fatalf("got %+v", got)
	}
}
