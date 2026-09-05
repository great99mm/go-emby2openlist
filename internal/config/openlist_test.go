package config

import "testing"

func TestMatchOpenlistInstance(t *testing.T) {
	instances := []OpenlistInstance{
		{Host: "https://first.example.com", Token: "first-token"},
		{Host: "https://SECOND.example.com.", Token: "second-token"},
	}

	tests := []struct {
		name      string
		rawURL    string
		wantHost  string
		wantToken string
		wantMatch bool
	}{
		{
			name:      "first instance",
			rawURL:    "https://first.example.com/d/video.mkv",
			wantHost:  "https://first.example.com",
			wantToken: "first-token",
			wantMatch: true,
		},
		{
			name:      "second instance",
			rawURL:    "https://second.example.com/d/video.mkv",
			wantHost:  "https://SECOND.example.com.",
			wantToken: "second-token",
			wantMatch: true,
		},
		{
			name:   "unknown host",
			rawURL: "https://other.example.com/d/video.mkv",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, matched, err := MatchOpenlistInstance(tt.rawURL, instances)
			if err != nil {
				t.Fatalf("MatchInstance() error = %v", err)
			}
			if matched != tt.wantMatch {
				t.Fatalf("MatchInstance() matched = %v, want %v", matched, tt.wantMatch)
			}
			if got.Host != tt.wantHost || got.Token != tt.wantToken {
				t.Fatalf("MatchInstance() = %+v, want host %q token %q", got, tt.wantHost, tt.wantToken)
			}
		})
	}
}
