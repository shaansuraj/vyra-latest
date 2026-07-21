package relay

import (
	"fmt"
	"log"
	"sync"

	"github.com/AlexxIT/go2rtc/pkg/core"
	go2webrtc "github.com/AlexxIT/go2rtc/pkg/webrtc"
	"github.com/google/uuid"
	"github.com/pion/webrtc/v4"
	"github.com/unblink/unblink/node"
	"github.com/unblink/unblink/relay/sources"
)

func relayICEServers(cfg *Config) []webrtc.ICEServer {
	servers := make([]webrtc.ICEServer, 0, len(cfg.WebRTCIceServers))
	for _, srv := range cfg.WebRTCIceServers {
		if len(srv.URLs) == 0 {
			continue
		}
		servers = append(servers, webrtc.ICEServer{
			URLs:       srv.URLs,
			Username:   srv.Username,
			Credential: srv.Credential,
		})
	}
	if len(servers) == 0 {
		return []webrtc.ICEServer{{URLs: []string{"stun:stun.l.google.com:19302"}}}
	}
	return servers
}

// WebRTCSession manages a single WebRTC streaming session
type WebRTCSession struct {
	id          string
	service     node.Service
	relay       *Relay
	webrtcConn  *go2webrtc.Conn
	source      sources.Source
	bridgeProxy *BridgeTCPProxy
	closeOnce   sync.Once
	closeChan   chan struct{}
	onClose     func() // Called when session closes to remove from manager
}

// WebRTCSessionManager manages all active WebRTC sessions
type WebRTCSessionManager struct {
	sessions map[string]*WebRTCSession
	mu       sync.RWMutex
}

// NewWebRTCSessionManager creates a new session manager
func NewWebRTCSessionManager() *WebRTCSessionManager {
	return &WebRTCSessionManager{
		sessions: make(map[string]*WebRTCSession),
	}
}

// sourceHasOnlyH265Video returns true if all video tracks from the source are H265.
func sourceHasOnlyH265Video(medias []*core.Media) bool {
	for _, m := range medias {
		if m.Kind != core.KindVideo {
			continue
		}
		for _, c := range m.Codecs {
			if c.Name == core.CodecH265 {
				return true
			}
		}
	}
	return false
}

// browserOffersH265 returns true if the browser's WebRTC offer includes H265.
func browserOffersH265(webrtcMedias []*core.Media) bool {
	for _, m := range webrtcMedias {
		for _, c := range m.Codecs {
			if c.Name == core.CodecH265 {
				return true
			}
		}
	}
	return false
}

// findBrowserH264Media finds the H264 video media and codec in the browser's WebRTC offer.
func findBrowserH264Media(webrtcMedias []*core.Media) (*core.Media, *core.Codec) {
	for _, m := range webrtcMedias {
		if m.Kind == core.KindVideo && m.Direction == core.DirectionSendonly {
			for _, c := range m.Codecs {
				if c.Name == core.CodecH264 {
					return m, c
				}
			}
		}
	}
	return nil, nil
}

// NewSession creates a new WebRTC session from an offer and service
func (m *WebRTCSessionManager) NewSession(offerSDP string, service node.Service, relay *Relay) (string, string, error) {
	// Find node that provides the service
	nodeID := relay.Services().GetNodeID(service.ID)
	if nodeID == "" {
		return "", "", fmt.Errorf("service %s not found on any node", service.ID)
	}

	nc := relay.GetNode(nodeID)
	if nc == nil {
		return "", "", fmt.Errorf("node %s not connected", nodeID)
	}

	// Create WebRTC API
	api, err := go2webrtc.NewAPI()
	if err != nil {
		return "", "", fmt.Errorf("create WebRTC API: %w", err)
	}

	// Create peer connection
	pc, err := api.NewPeerConnection(webrtc.Configuration{
		ICEServers: relayICEServers(relay.config),
	})
	if err != nil {
		return "", "", fmt.Errorf("create peer connection: %w", err)
	}

	// Wrap with go2rtc webrtc.Conn
	wconn := go2webrtc.NewConn(pc)
	wconn.Mode = core.ModePassiveProducer

	// Parse the offer
	if err := wconn.SetOffer(offerSDP); err != nil {
		pc.Close()
		return "", "", fmt.Errorf("set offer: %w", err)
	}

	// Collect browser WebRTC medias
	webrtcMedias := wconn.GetMedias()

	var (
		source         sources.Source
		bridgeProxy    *BridgeTCPProxy
		producer       core.Producer
		receivers      []*core.Receiver
		sharedRealtime bool
	)

	// Prefer the already-running realtime stream. Opening another RTSP client
	// for the same camera can overload small cameras/NVRs and corrupt the CV
	// extractor's decode path. Reusing the receiver keeps one upstream RTSP
	// connection per service and fans out inside go2rtc.
	if relay.realtimeStreamManager != nil {
		if stream, err := relay.realtimeStreamManager.GetStream(service.ID); err == nil {
			if extractor := stream.GetFrameExtractor(); extractor != nil {
				select {
				case <-extractor.DoneChan():
				default:
					candidateProducer := stream.GetProducer()
					candidateMedias := candidateProducer.GetMedias()
					if len(candidateMedias) > 0 && !(sourceHasOnlyH265Video(candidateMedias) && !browserOffersH265(webrtcMedias)) {
						producer = candidateProducer
						receivers = stream.GetReceivers()
						sharedRealtime = true
						log.Printf("[WebRTC] Reusing realtime stream for service %s", service.ID)
					}
				}
			}
		}
	}

	if producer == nil {
		// Open bridge through node to the service
		bridgeID, err := nc.OpenBridge(service)
		if err != nil {
			pc.Close()
			return "", "", fmt.Errorf("open bridge: %w", err)
		}

		log.Printf("[WebRTC] Opened bridge %s to service %s", bridgeID, service.ID)

		// Create TCP proxy for the bridge
		bridgeProxy, err = NewBridgeTCPProxy(nc, bridgeID, service)
		if err != nil {
			nc.CloseBridge(bridgeID)
			pc.Close()
			return "", "", fmt.Errorf("create bridge proxy: %w", err)
		}

		proxyAddr := bridgeProxy.Addr()
		log.Printf("[WebRTC] Bridge proxy listening on: %s", proxyAddr)

		// Create source (RTSP or MJPEG based on service type)
		source, err = sources.New(service, proxyAddr)
		if err != nil {
			bridgeProxy.Close()
			pc.Close()
			return "", "", fmt.Errorf("create source: %w", err)
		}

		producer = source.GetProducer()
		receivers = source.GetReceivers()
	}

	// Get medias from source for codec detection
	medias := producer.GetMedias()
	if len(medias) == 0 {
		pc.Close()
		if source != nil {
			source.Close()
		}
		if bridgeProxy != nil {
			bridgeProxy.Close()
		}
		return "", "", fmt.Errorf("no media streams")
	}

	// Log available codecs
	for _, md := range medias {
		var names []string
		for _, c := range md.Codecs {
			names = append(names, fmt.Sprintf("%s/%dHz", c.Name, c.ClockRate))
		}
		log.Printf("[WebRTC] Source media: %s, codecs: %v", md.Kind, names)
	}
	for _, md := range webrtcMedias {
		var names []string
		for _, c := range md.Codecs {
			names = append(names, fmt.Sprintf("%s/%dHz", c.Name, c.ClockRate))
		}
		log.Printf("[WebRTC] Browser offer: %s (%s), codecs: %v", md.Kind, md.Direction, names)
	}

	// Check if H265 -> H264 transcoding is needed.
	// H265 is not supported in WebRTC by most browsers; transcode via FFmpeg.
	var ffmpegSrc *sources.FFmpegTranscodeSource
	needsTranscode := sourceHasOnlyH265Video(medias) && !browserOffersH265(webrtcMedias)

	if needsTranscode {
		log.Printf("[WebRTC] H265 source detected but browser doesn't support H265 - transcoding to H264 via FFmpeg")

		// Find H264 in the browser's offer so we can add the track with the correct codec.
		h264Media, h264Codec := findBrowserH264Media(webrtcMedias)
		if h264Media == nil || h264Codec == nil {
			source.Close()
			bridgeProxy.Close()
			pc.Close()
			return "", "", fmt.Errorf("source is H265 but browser doesn't support H265 or H264 - cannot stream")
		}
		log.Printf("[WebRTC] Browser H264 codec: PT=%d", h264Codec.PayloadType)

		// Close the initial RTSP source and bridge (used only for codec detection)
		source.Close()
		bridgeProxy.Close()

		// Open a fresh bridge for FFmpeg to connect to
		bridgeID2, err := nc.OpenBridge(service)
		if err != nil {
			pc.Close()
			return "", "", fmt.Errorf("open bridge for transcoding: %w", err)
		}

		bridgeProxy2, err := NewBridgeTCPProxy(nc, bridgeID2, service)
		if err != nil {
			nc.CloseBridge(bridgeID2)
			pc.Close()
			return "", "", fmt.Errorf("create bridge proxy for transcoding: %w", err)
		}

		// NewFFmpegTranscode outputs MPEG-TS to stdout, parsed by go2rtc magic.Open()
		// into a native producer - same pipeline as the MJPEG source.
		ffmpegSrc, err = sources.NewFFmpegTranscode(service, bridgeProxy2.Addr())
		if err != nil {
			bridgeProxy2.Close()
			pc.Close()
			return "", "", fmt.Errorf("start ffmpeg transcoder: %w", err)
		}

		// Replace source and bridgeProxy with the transcoding ones
		source = ffmpegSrc
		bridgeProxy = bridgeProxy2

		txReceivers := ffmpegSrc.GetReceivers()
		if len(txReceivers) == 0 {
			ffmpegSrc.Close()
			bridgeProxy.Close()
			pc.Close()
			return "", "", fmt.Errorf("ffmpeg transcoder returned no receivers")
		}

		// Add track using the browser's H264 codec so SDP and PT match what the browser expects
		if err := wconn.AddTrack(h264Media, h264Codec, txReceivers[0]); err != nil {
			ffmpegSrc.Close()
			bridgeProxy.Close()
			pc.Close()
			return "", "", fmt.Errorf("add transcoded H264 track: %w", err)
		}
		log.Printf("[WebRTC] Added transcoded H264 track (FFmpeg H265->H264, browser PT=%d)", h264Codec.PayloadType)

	} else {
		// Normal codec matching - add tracks from source receivers
		for _, sourceMedia := range medias {
			// Find matching WebRTC media by kind
			var webrtcMedia *core.Media
			for _, md := range webrtcMedias {
				if md.Kind == sourceMedia.Kind && md.Direction == core.DirectionSendonly {
					webrtcMedia = md
					break
				}
			}
			if webrtcMedia == nil {
				log.Printf("[WebRTC] No matching WebRTC media for kind=%s", sourceMedia.Kind)
				continue
			}

			// Find matching codec
			for _, codec := range sourceMedia.Codecs {
				var webrtcCodec *core.Codec
				for _, c := range webrtcMedia.Codecs {
					if c.Name == codec.Name {
						webrtcCodec = c
						break
					}
				}
				if webrtcCodec == nil {
					log.Printf("[WebRTC] Codec %s not in WebRTC offer for %s", codec.Name, sourceMedia.Kind)
					continue
				}

				// Add track - use receiver from RTSP, or get track from MJPEG producer
				var receiver *core.Receiver
				if len(receivers) > 0 {
					// RTSP: find receiver matching this codec
					for _, r := range receivers {
						if r != nil && r.Codec.Name == codec.Name {
							receiver = r
							break
						}
					}
				}

				if receiver == nil {
					// No pre-created receivers: get track from producer
					track, err := producer.GetTrack(sourceMedia, codec)
					if err != nil {
						log.Printf("[WebRTC] GetTrack error for %s/%s: %v", sourceMedia.Kind, codec.Name, err)
						continue
					}
					receiver = track
				}

				if err := wconn.AddTrack(webrtcMedia, webrtcCodec, receiver); err != nil {
					log.Printf("[WebRTC] AddTrack error for %s/%s: %v", sourceMedia.Kind, codec.Name, err)
					continue
				}

				log.Printf("[WebRTC] Added track: %s/%s", sourceMedia.Kind, codec.Name)
				break // Use first codec per media
			}
		}
	}

	// Create answer with ICE candidates
	answerSDP, err := wconn.GetCompleteAnswer(nil, nil)
	if err != nil {
		pc.Close()
		if source != nil {
			source.Close()
		}
		if bridgeProxy != nil {
			bridgeProxy.Close()
		}
		return "", "", fmt.Errorf("create answer: %w", err)
	}

	// Create session
	sessionID := uuid.New().String()
	closeChan := make(chan struct{})
	session := &WebRTCSession{
		id:          sessionID,
		service:     service,
		relay:       relay,
		webrtcConn:  wconn,
		source:      source,
		bridgeProxy: bridgeProxy,
		closeChan:   closeChan,
	}

	// Set onClose to remove session from manager
	session.onClose = func() {
		m.mu.Lock()
		delete(m.sessions, sessionID)
		m.mu.Unlock()
		log.Printf("[WebRTC] Removed session %s from manager", sessionID)
	}

	// Store session
	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	// Setup cleanup when WebRTC connection closes
	pc.OnICEConnectionStateChange(func(state webrtc.ICEConnectionState) {
		log.Printf("[WebRTC] ICE state changed: %s", state)
		if state == webrtc.ICEConnectionStateClosed ||
			state == webrtc.ICEConnectionStateDisconnected ||
			state == webrtc.ICEConnectionStateFailed {
			session.Close()
		}
	})

	// Start media handling goroutine - drives the session lifetime
	go func() {
		if sharedRealtime {
			log.Printf("[WebRTC] Session %s is using shared realtime producer for service %s", sessionID, service.ID)
			if err := wconn.Start(); err != nil {
				log.Printf("[WebRTC] WebRTC connection ended for shared session %s: %v", sessionID, err)
			}
		} else if ffmpegSrc != nil {
			// Transcoding path: drive the MPEG-TS producer (same as MJPEG source)
			ffmpegProd := ffmpegSrc.GetProducer()
			log.Printf("[WebRTC] FFmpeg transcoder producer.Start() for session %s", sessionID)
			if err := ffmpegProd.Start(); err != nil {
				log.Printf("[WebRTC] FFmpeg producer error for session %s: %v", sessionID, err)
			}
			log.Printf("[WebRTC] FFmpeg transcoder ended for session %s", sessionID)
		} else {
			// Normal path: run the go2rtc producer (blocks until stream ends).
			// For RTSP sources, call Play() now - right before Start() so the camera
			// only starts sending data when the consumer goroutine is ready to read it.
			if rtspSrc, ok := source.(*sources.RTSPSource); ok {
				if err := rtspSrc.Play(); err != nil {
					log.Printf("[WebRTC] RTSP Play error for session %s: %v", sessionID, err)
					session.Close()
					return
				}
				log.Printf("[WebRTC] RTSP Play sent for session %s", sessionID)
			}
			log.Printf("[WebRTC] Starting %s producer.Start() for session %s", service.Type, sessionID)
			if err := producer.Start(); err != nil {
				log.Printf("[WebRTC] %s Handle error for session %s: %v", service.Type, sessionID, err)
			}
			log.Printf("[WebRTC] %s Handle() ended for session %s", service.Type, sessionID)
		}
		session.Close()
	}()

	log.Printf("[WebRTC] Created session %s for service %s (type=%s, transcoding=%v, shared_realtime=%v)", sessionID, service.ID, service.Type, needsTranscode, sharedRealtime)

	return sessionID, answerSDP, nil
}

// Close closes a session
func (s *WebRTCSession) Close() {
	s.closeOnce.Do(func() {
		log.Printf("[WebRTC] Closing session %s", s.id)

		close(s.closeChan)

		if s.webrtcConn != nil {
			_ = s.webrtcConn.Stop()
			_ = s.webrtcConn.Close()
		}

		if s.source != nil {
			s.source.Close()
		}

		if s.bridgeProxy != nil {
			s.bridgeProxy.Close()
		}

		// Remove from session manager
		if s.onClose != nil {
			s.onClose()
		}
	})
}

// GetSession retrieves a session by ID
func (m *WebRTCSessionManager) GetSession(id string) *WebRTCSession {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

// CloseSession closes and removes a session
func (m *WebRTCSessionManager) CloseSession(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if session, exists := m.sessions[id]; exists {
		session.Close()
		delete(m.sessions, id)
	}
}
