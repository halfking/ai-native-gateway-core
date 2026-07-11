package main

import "testing"

func TestValidateAnalysisEndpoint(t *testing.T) {
	tests := []struct {
		name               string
		url                string
		allowInsecureLocal bool
		wantErr            bool
	}{
		{name: "public https", url: "https://api.openai.com/v1"},
		{name: "reject http", url: "http://example.com/v1", wantErr: true},
		{name: "reject loopback https", url: "https://127.0.0.1/v1", wantErr: true},
		{name: "allow local dev", url: "http://127.0.0.1:8080/v1", allowInsecureLocal: true},
		{name: "reject credentials", url: "https://user:pass@example.com/v1", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := validateAnalysisEndpoint(test.url, test.allowInsecureLocal)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
		})
	}
}
