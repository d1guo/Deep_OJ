package worker

import "testing"

func TestNormalizeMinIOEndpoint(t *testing.T) {
	tests := []struct {
		name          string
		endpoint      string
		initialSecure bool
		wantEndpoint  string
		wantSecure    bool
		expectErr     bool
	}{
		{
			name:          "plain endpoint keeps secure flag",
			endpoint:      "oj-minio:9000",
			initialSecure: false,
			wantEndpoint:  "oj-minio:9000",
			wantSecure:    false,
		},
		{
			name:          "plain endpoint keeps true secure flag",
			endpoint:      "oj-minio:9000",
			initialSecure: true,
			wantEndpoint:  "oj-minio:9000",
			wantSecure:    true,
		},
		{
			name:          "http endpoint parsed as insecure",
			endpoint:      "http://oj-minio:9000",
			initialSecure: true,
			wantEndpoint:  "oj-minio:9000",
			wantSecure:    false,
		},
		{
			name:          "https endpoint parsed as secure",
			endpoint:      "https://minio.example.com:9443",
			initialSecure: false,
			wantEndpoint:  "minio.example.com:9443",
			wantSecure:    true,
		},
		{
			name:      "invalid url",
			endpoint:  "http://%zz",
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotEndpoint, gotSecure, err := normalizeMinIOEndpoint(tc.endpoint, tc.initialSecure)
			if tc.expectErr {
				if err == nil {
					t.Fatalf("expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if gotEndpoint != tc.wantEndpoint {
				t.Fatalf("unexpected endpoint: got=%q want=%q", gotEndpoint, tc.wantEndpoint)
			}
			if gotSecure != tc.wantSecure {
				t.Fatalf("unexpected secure flag: got=%v want=%v", gotSecure, tc.wantSecure)
			}
		})
	}
}
