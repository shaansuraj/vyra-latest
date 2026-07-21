package sources

import (
	"fmt"
	"log"
	"net/url"
	"strings"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/rtsp"
	"github.com/unblink/unblink/node"
)

// RTSPSource handles RTSP stream sources
type RTSPSource struct {
	client    *rtsp.Conn
	receivers []*core.Receiver
}

func buildRTSPURL(service node.Service, proxyAddr string) (string, error) {
	if proxyAddr == "" {
		return "", fmt.Errorf("empty proxy address")
	}

	path := service.Path
	if path != "" && !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	u, err := url.Parse("rtsp://" + proxyAddr + path)
	if err != nil {
		return "", fmt.Errorf("parse rtsp url: %w", err)
	}

	if service.Auth != nil {
		u.User = url.UserPassword(service.Auth.Username, service.Auth.Password)
	}

	return u.String(), nil
}

// NewRTSP creates a new RTSP source from the given service and proxy address
func NewRTSP(service node.Service, proxyAddr string) (*RTSPSource, error) {
	rtspURL, err := buildRTSPURL(service, proxyAddr)
	if err != nil {
		return nil, err
	}

	// Create RTSP client
	client := rtsp.NewClient(rtspURL)
	client.Transport = "tcp"

	// Connect to RTSP server
	if err := client.Dial(); err != nil {
		return nil, fmt.Errorf("RTSP dial: %w", err)
	}

	// Describe to get media info
	if err := client.Describe(); err != nil {
		client.Close()
		return nil, fmt.Errorf("RTSP describe: %w", err)
	}

	// Get medias from RTSP
	rtspMedias := client.GetMedias()
	if len(rtspMedias) == 0 {
		client.Close()
		return nil, fmt.Errorf("no media streams in RTSP")
	}

	// Get receivers for all media/codecs (first codec per media)
	var receivers []*core.Receiver
	for _, media := range rtspMedias {
		for _, codec := range media.Codecs {
			receiver, err := client.GetTrack(media, codec)
			if err != nil {
				continue
			}
			receivers = append(receivers, receiver)
			log.Printf("[RTSP] Added track: %s/%s", media.Kind, codec.Name)
			break // Use first codec per media
		}
	}

	if len(receivers) == 0 {
		client.Close()
		return nil, fmt.Errorf("no codecs found")
	}

	// NOTE: Play() is NOT called here intentionally.
	// Calling Play() in the constructor starts the camera data stream before any
	// consumer goroutine is ready to read it. This fills the bridge TCP receive buffer
	// in ~180ms for HD H265 streams, causing handleConn.Write() to block and the
	// bridge channel to overflow. Call Play() explicitly via RTSPSource.Play() just
	// before producer.Start() so data flows only when the consumer is ready.

	return &RTSPSource{
		client:    client,
		receivers: receivers,
	}, nil
}

// Play sends the RTSP PLAY request, starting the camera data stream.
// Call this in a goroutine just before producer.Start().
func (s *RTSPSource) Play() error {
	return s.client.Play()
}

// GetProducer returns the RTSP client as a producer
func (s *RTSPSource) GetProducer() core.Producer {
	return s.client
}

// GetReceivers returns the receivers for the RTSP tracks
func (s *RTSPSource) GetReceivers() []*core.Receiver {
	return s.receivers
}

// Close stops the RTSP client
func (s *RTSPSource) Close() {
	if s.client != nil {
		s.client.Stop()
	}
}
