package main

import (
	"net"
	"testing"
)

func TestValidateAnalysisEndpoint(t *testing.T) {
	oldLookup := lookupAnalysisIP
	lookupAnalysisIP = func(host string) ([]net.IP, error) {
		switch host {
		case "api.openai.com", "example.com":
			return []net.IP{net.ParseIP("8.8.8.8")}, nil
		case "private.example":
			return []net.IP{net.ParseIP("10.0.0.10")}, nil
		case "127.0.0.1":
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		default:
			return []net.IP{net.ParseIP("8.8.4.4")}, nil
		}
	}
	t.Cleanup(func() { lookupAnalysisIP = oldLookup })

	tests := []struct {
		name               string
		url                string
		allowInsecureLocal bool
		wantErr            bool
	}{
		{name: "public https", url: "https://api.openai.com/v1"},
		{name: "reject http", url: "http://example.com/v1", wantErr: true},
		{name: "reject loopback https", url: "https://127.0.0.1/v1", wantErr: true},
		{name: "reject private dns https", url: "https://private.example/v1", wantErr: true},
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
