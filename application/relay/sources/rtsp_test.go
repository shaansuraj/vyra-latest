package sources

import (
	"strings"
	"testing"

	"github.com/unblink/unblink/node"
)

func TestBuildRTSPURLEscapesCredentialsAndKeepsQuery(t *testing.T) {
	svc := node.Service{
		Path: "/cam/realmonitor?channel=1&subtype=1",
		Auth: &node.Auth{
			Username: "admin",
			Password: "Admin@123",
		},
	}

	got, err := buildRTSPURL(svc, "127.0.0.1:4554")
	if err != nil {
		t.Fatalf("buildRTSPURL returned error: %v", err)
	}

	want := "rtsp://admin:Admin%40123@127.0.0.1:4554/cam/realmonitor?channel=1&subtype=1"
	if got != want {
		t.Fatalf("unexpected URL\nwant: %s\ngot:  %s", want, got)
	}
}

func TestBuildRTSPURLAddsMissingLeadingSlash(t *testing.T) {
	svc := node.Service{Path: "stream1"}

	got, err := buildRTSPURL(svc, "127.0.0.1:4554")
	if err != nil {
		t.Fatalf("buildRTSPURL returned error: %v", err)
	}

	want := "rtsp://127.0.0.1:4554/stream1"
	if got != want {
		t.Fatalf("unexpected URL\nwant: %s\ngot:  %s", want, got)
	}
}

func TestBuildRTSPURLRejectsInvalidPathEncoding(t *testing.T) {
	svc := node.Service{Path: "/cam/%zz"}

	_, err := buildRTSPURL(svc, "127.0.0.1:4554")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "parse rtsp url") {
		t.Fatalf("unexpected error: %v", err)
	}
}
