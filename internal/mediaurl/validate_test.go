package mediaurl

import (
	"net/url"
	"testing"
)

func TestIsPublicHTTPS(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{raw: "https://assets.example/a.png", want: true},
		{raw: "https://cdn.example.com:8443/path/to/video.mp4?sig=1", want: true},
		{raw: "https://8.8.8.8/a.png", want: true},
		{raw: "https://[2606:4700:4700::1111]/a.png", want: true},
		{raw: "http://assets.example/a.png", want: false},
		{raw: "https://intranet/a.png", want: false},
		{raw: "https://localhost/a.png", want: false},
		{raw: "https://sub.localhost/a.png", want: false},
		{raw: "https://service.local/a.png", want: false},
		{raw: "https://pod.default.svc/a.png", want: false},
		{raw: "https://pod.default.svc.cluster.local/a.png", want: false},
		{raw: "https://router.home.arpa/a.png", want: false},
		{raw: "https://metadata.google.internal/a.png", want: false},
		{raw: "https://0.1.2.3/a.png", want: false},
		{raw: "https://127.0.0.1/a.png", want: false},
		{raw: "https://10.0.0.5/a.png", want: false},
		{raw: "https://100.64.0.1/a.png", want: false},
		{raw: "https://169.254.169.254/a.png", want: false},
		{raw: "https://240.0.0.1/a.png", want: false},
		{raw: "https://[::1]/a.png", want: false},
		{raw: "https://[::127.0.0.1]/a.png", want: false},
		{raw: "https://[::ffff:127.0.0.1]/a.png", want: false},
		{raw: "https://[64:ff9b::7f00:1]/a.png", want: false},
		{raw: "https://[2002:7f00:1::]/a.png", want: false},
		{raw: "https://2130706433/a.png", want: false},
		{raw: "https://0x7f000001/a.png", want: false},
		{raw: "https://127.1/a.png", want: false},
		{raw: "https://0177.0.0.1/a.png", want: false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			u, err := url.Parse(tc.raw)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.raw, err)
			}
			if got := IsPublicHTTPS(u); got != tc.want {
				t.Fatalf("IsPublicHTTPS(%q) = %t, want %t", tc.raw, got, tc.want)
			}
		})
	}
}
