package api

import (
	"testing"

	"github.com/cnjack/jcloud/internal/domain"
)

func TestWorkflowContractProfileInstructionsAreMemberOnly(t *testing.T) {
	run := &domain.Run{ID: "run", ExecutionContract: &domain.WorkflowContract{
		Profile: domain.AgentProfileSnapshot{ID: domain.BuiltinReviewerProfileID, Instructions: "private bounded instructions"},
	}}
	viewer := projectRunForRole(run, domain.RoleViewer)
	if viewer.ExecutionContract.Profile.Instructions != "" {
		t.Fatal("viewer projection leaked profile instructions")
	}
	member := projectRunForRole(run, domain.RoleMember)
	if member.ExecutionContract.Profile.Instructions != "private bounded instructions" {
		t.Fatal("member projection dropped profile instructions")
	}
	if run.ExecutionContract.Profile.Instructions != "private bounded instructions" {
		t.Fatal("projection mutated the stored Run")
	}
	lists := nonNilRuns([]domain.Run{*run})
	if lists[0].ExecutionContract.Profile.Instructions != "" {
		t.Fatal("list projection leaked profile instructions")
	}
}
