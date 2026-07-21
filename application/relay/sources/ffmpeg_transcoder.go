package sources

import (
	"bufio"
	"fmt"
	"log"
	"os/exec"

	"github.com/AlexxIT/go2rtc/pkg/core"
	"github.com/AlexxIT/go2rtc/pkg/magic"
	"github.com/unblink/unblink/node"
)

// FFmpegTranscodeSource transcodes H265 → H264 via FFmpeg subprocess.
// Follows the same pattern as MJPEGSource: FFmpeg outputs MPEG-TS on stdout,
// parsed by go2rtc's magic.Open() into a native producer. This guarantees
// compatibility with go2rtc's WebRTC pipeline without manual RTP wrangling.
type FFmpegTranscodeSource struct {
	producer  core.Producer
	cmd       *exec.Cmd
	receivers []*core.Receiver
}

// NewFFmpegTranscode starts FFmpeg to transcode H265→H264 from the given RTSP proxy address.
// proxyAddr is the local TCP address of the bridge proxy (e.g. "127.0.0.1:PORT").
func NewFFmpegTranscode(service node.Service, proxyAddr string) (*FFmpegTranscodeSource, error) {
	// Build RTSP URL that FFmpeg will read from (via the bridge TCP proxy)
	rtspURL := "rtsp://"
	if service.Auth != nil {
		rtspURL += fmt.Sprintf("%s:%s@", service.Auth.Username, service.Auth.Password)
	}
	rtspURL += proxyAddr + service.Path

	// FFmpeg: H265 RTSP → H264 720p MPEG-TS to stdout (same output format as MJPEG source).
	// Scale to 720p so the output behaves identically to a native subtype=1 stream:
	// reduced bitrate, no pixelation in tiles, lower encoding CPU load.
	cmd := exec.Command("ffmpeg",
		"-loglevel", "error",
		"-rtsp_transport", "tcp",
		"-i", rtspURL,
		"-c:v", "libx264",
		"-preset", "ultrafast",
		"-tune", "zerolatency",
		"-vf", "scale=-2:720", // scale down to 720p height, keep aspect ratio
		"-g", "30",            // keyframe every 30 frames (~1 sec at 30fps)
		"-b:v", "2M",
		"-maxrate", "2M",
		"-bufsize", "4M",
		"-an",          // skip audio
		"-f", "mpegts", // output as MPEG-TS (same as MJPEG source)
		"-",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg stderr pipe: %w", err)
	}

	log.Printf("[FFmpegTranscode] Starting H265→H264 transcoder via RTSP %s%s", proxyAddr, service.Path)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start ffmpeg: %w", err)
	}

	// Forward FFmpeg stderr to relay log for diagnostics
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			log.Printf("[FFmpegTranscode] %s", scanner.Text())
		}
	}()

	// Parse the MPEG-TS stream from FFmpeg stdout using go2rtc's magic parser.
	// This creates a native producer identical to what the MJPEG source uses.
	rd := bufio.NewReaderSize(stdout, core.BufferSize)
	prod, err := magic.Open(rd)
	if err != nil {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return nil, fmt.Errorf("magic.Open H264 MPEG-TS: %w", err)
	}

	medias := prod.GetMedias()
	if len(medias) == 0 {
		prod.Stop()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return nil, fmt.Errorf("no media from FFmpeg transcoder")
	}

	// Get H264 receivers from the producer (same logic as MJPEG source)
	var receivers []*core.Receiver
	for _, media := range medias {
		for _, codec := range media.Codecs {
			if codec.Name != core.CodecH264 {
				continue
			}
			receiver, err := prod.GetTrack(media, codec)
			if err != nil {
				log.Printf("[FFmpegTranscode] GetTrack error: %v", err)
				continue
			}
			receivers = append(receivers, receiver)
			log.Printf("[FFmpegTranscode] H264 track ready: %s/%s (PT=%d)", media.Kind, codec.Name, codec.PayloadType)
			break
		}
	}

	if len(receivers) == 0 {
		prod.Stop()
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		return nil, fmt.Errorf("no H264 codec in FFmpeg transcoder output")
	}

	return &FFmpegTranscodeSource{
		producer:  prod,
		cmd:       cmd,
		receivers: receivers,
	}, nil
}

// GetProducer returns the go2rtc producer. The caller must call producer.Start()
// in a goroutine to drive the stream (blocks until stream ends).
func (s *FFmpegTranscodeSource) GetProducer() core.Producer {
	return s.producer
}

// GetReceivers returns the H264 receiver(s) obtained from the producer.
func (s *FFmpegTranscodeSource) GetReceivers() []*core.Receiver {
	return s.receivers
}

// Wait is kept for interface compatibility but is a no-op; use producer.Start() instead.
func (s *FFmpegTranscodeSource) Wait() {}

// Close stops the FFmpeg process and producer.
func (s *FFmpegTranscodeSource) Close() {
	if s.producer != nil {
		_ = s.producer.Stop()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
}
