package main

import "testing"

// The dashboard's content search includes automated sessions only while its
// Automated switch is on, so they never crowd the limit when they cannot show.
func TestDashboardSearchOpts_AutomatedFollowsTheSwitch(t *testing.T) {
	if o := dashboardSearchOpts(false); o.IncludeAutomated || o.Limit != 50 {
		t.Fatalf("switch off: %+v", o)
	}
	if o := dashboardSearchOpts(true); !o.IncludeAutomated || o.Limit != 50 {
		t.Fatalf("switch on: %+v", o)
	}
}
