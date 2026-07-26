package api

import (
	"strings"
	"testing"
)

func TestPluginAutomationPathFilterIsLimitedToPush(t *testing.T) {
	base := pluginAutomationReq{
		ServiceID:      "service",
		Name:           "filtered",
		PromptTemplate: "handle event",
		SCM: &pluginSCMReq{
			PathPattern: "src/**",
			Actions:     []pluginSCMActionReq{{EventFamily: "pull_request", Action: "synchronized"}},
		},
	}
	_, _, _, _, _, message := pluginAutomationFromReq(base, "automation")
	if !strings.Contains(message, "only for push.updated") {
		t.Fatalf("validation message = %q", message)
	}
	base.SCM.Actions = []pluginSCMActionReq{{EventFamily: "push", Action: "updated"}}
	_, scm, actions, _, _, message := pluginAutomationFromReq(base, "automation")
	if message != "" || scm == nil || len(actions) != 1 {
		t.Fatalf("valid push path filter rejected: message=%q scm=%+v actions=%+v", message, scm, actions)
	}
}

func TestPluginAutomationTypedFiltersRejectIncompatibleActions(t *testing.T) {
	tests := []struct {
		name    string
		scm     pluginSCMReq
		message string
	}{
		{
			name: "conclusion on push",
			scm: pluginSCMReq{Conclusion: "failure",
				Actions: []pluginSCMActionReq{{EventFamily: "push", Action: "updated"}}},
			message: "only for check.completed",
		},
		{
			name: "branch on comment",
			scm: pluginSCMReq{Branch: "main",
				Actions: []pluginSCMActionReq{{EventFamily: "comment", Action: "created"}}},
			message: "only for push, pull_request, and check",
		},
		{
			name: "invalid branch glob",
			scm: pluginSCMReq{Branch: "release/[",
				Actions: []pluginSCMActionReq{{EventFamily: "push", Action: "updated"}}},
			message: "invalid glob pattern",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := pluginAutomationReq{ServiceID: "service", Name: "filtered", PromptTemplate: "handle", SCM: &tt.scm}
			_, _, _, _, _, message := pluginAutomationFromReq(req, "automation")
			if !strings.Contains(message, tt.message) {
				t.Fatalf("validation message = %q, want %q", message, tt.message)
			}
		})
	}
}
