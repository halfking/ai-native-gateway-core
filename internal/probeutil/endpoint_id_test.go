package probeutil

import "testing"

func TestIsEndpointIDRequiredError_VolcanoArk(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "explicit InvalidEndpointOrModel code",
			body: `{"error":{"code":"InvalidEndpointOrModel.NotFound","message":"The model or endpoint glm-5.1 does not exist or you do not have access to it."}}`,
			want: true,
		},
		{
			name: "no access wording",
			body: `{"error":{"message":"The endpoint glm-5.1 does not exist or you do not have access to it."}}`,
			want: true,
		},
		{
			name: "endpoint not found",
			body: `endpoint glm-5.1 not found`,
			want: true,
		},
		{
			name: "endpoint does not exist",
			body: `endpoint glm-5.1 does not exist`,
			want: true,
		},
		{
			name: "plain model_not_found without endpoint keyword",
			body: `{"error":{"code":"model_not_found","message":"model glm-5.1 not found"}}`,
			want: false,
		},
		{
			name: "generic 404 with different body",
			body: `404 page not found`,
			want: false,
		},
		{
			name: "empty body",
			body: ``,
			want: false,
		},
		{
			name: "endpoint ok but unrelated 404",
			body: `{"error":{"code":"NotFound","message":"resource does not exist"}}`,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsEndpointIDRequiredError(tc.body)
			if got != tc.want {
				t.Errorf("IsEndpointIDRequiredError(%q) = %v, want %v", tc.body, got, tc.want)
			}
		})
	}
}