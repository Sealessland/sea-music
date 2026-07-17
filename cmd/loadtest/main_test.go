package main

import "testing"

func TestParseOutboxBacklogIncludesPublishingLeases(t *testing.T) {
	pending, failed, err := parseOutboxMetrics([]byte(`
sea_music_outbox_events{state="pending"} 0
sea_music_outbox_events{state="publishing"} 19
sea_music_outbox_events{state="failed"} 0
`))
	if err != nil {
		t.Fatalf("parseOutboxMetrics() error = %v", err)
	}
	if pending != 19 || failed != 0 {
		t.Fatalf("parseOutboxMetrics() = (%d, %d), want (19, 0)", pending, failed)
	}
}

func TestParseOutboxBacklogRequiresAllStates(t *testing.T) {
	_, _, err := parseOutboxMetrics([]byte(`sea_music_outbox_events{state="pending"} 1`))
	if err == nil {
		t.Fatal("parseOutboxMetrics() error = nil, want incomplete metrics error")
	}
}
