package cv

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/AlexxIT/go2rtc/pkg/core"
	h264pkg "github.com/AlexxIT/go2rtc/pkg/h264"
	h265pkg "github.com/AlexxIT/go2rtc/pkg/h265"
	mpegts "github.com/AlexxIT/go2rtc/pkg/mpegts"
	"github.com/google/uuid"
)

// RealtimeStream interface for dependency injection
type RealtimeStream interface {
	GetReceivers() []*core.Receiver
	GetProducer() core.Producer
	GetService() interface{} // Returns node.Service
	GetID() string
	CloseChan() <-chan struct{}
	StartProducer() error
}

const frameReadyBufferSize = 2

// CVFrameExtractor extracts JPEG frames from H.264 or H.265 streams
type CVFrameExtractor struct {
	serviceID       string
	extractInterval time.Duration // per-camera rate (Go-level, may differ from FFmpeg rate)
	eventBus        *CVEventBus
	storageManager  *StorageManager
	codecName       string // "h264" or "h265", detected at Start()

	// Shutdown
	closeChan chan struct{}
	closed    atomic.Bool
	wg        sync.WaitGroup

	// FFmpeg pipeline
	ffmpegCmd    *exec.Cmd
	ffmpegStdin  io.WriteCloser
	ffmpegStdout io.ReadCloser

	frameReady chan []byte

	// Protect stdin writes / batch state / ffmpeg state / interval updates
	mu       sync.Mutex
	stdinMu  sync.Mutex
	ffmpegMu sync.Mutex

	// Frame batching for batch events
	frameBatch     []string
	batchSize      int
	batchStartTime time.Time

	// Flush batch even if batchSize not reached
	batchMaxWait time.Duration
	vlmFPSHint   float64

	// Per-camera rate limiting (independent of FFmpeg rate)
	lastSavedAt time.Time

	// Friendly camera name included in frame_batch metadata so Python workers
	// can overlay it on saved snapshots.
	serviceName string

	// Aggregated logging for dropped JPEG frames when downstream channel is full.
	dropLogMu        sync.Mutex
	droppedSinceLog  int
	lastDroppedLogAt time.Time
}

// NewCVFrameExtractor creates a new frame extractor
func NewCVFrameExtractor(serviceID string, extractInterval time.Duration, eventBus *CVEventBus, storageManager *StorageManager, batchSize int) *CVFrameExtractor {
	if batchSize <= 0 {
		batchSize = 1
	}
	return &CVFrameExtractor{
		serviceID:       serviceID,
		extractInterval: extractInterval,
		eventBus:        eventBus,
		storageManager:  storageManager,
		closeChan:       make(chan struct{}),
		frameReady:      make(chan []byte, frameReadyBufferSize),
		frameBatch:      make([]string, 0, batchSize),
		batchSize:       batchSize,
		// default: "don't let batch hang forever"
		batchMaxWait: time.Duration(batchSize) * extractInterval,
		vlmFPSHint:   5.0,
	}
}

// SetServiceName stores the human-readable camera name that is included in
// every frame_batch metadata payload so workers can label snapshots.
func (e *CVFrameExtractor) SetServiceName(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.serviceName = name
}

func (e *CVFrameExtractor) SetFPSHints(vlmFPS float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if vlmFPS > 0 {
		e.vlmFPSHint = vlmFPS
	}
}

// DoneChan closes when extractor shutdown has begun.
func (e *CVFrameExtractor) DoneChan() <-chan struct{} {
	return e.closeChan
}

// SetInterval updates the per-camera frame extraction rate without restarting FFmpeg.
// Takes effect immediately: frames are dropped in Go if the new interval is larger.
// If the new interval is smaller than the FFmpeg output rate, frames are not dropped
// (effective fps is capped at the FFmpeg rate).
func (e *CVFrameExtractor) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.extractInterval = d
	if e.batchSize > 0 {
		e.batchMaxWait = time.Duration(e.batchSize) * d
	}
	log.Printf("[CVFrameExtractor] Updated interval for service %s to %v (%.2f fps)", e.serviceID, d, 1.0/d.Seconds())
}

// Start begins extracting frames from the realtime stream
func (e *CVFrameExtractor) Start(stream RealtimeStream) error {
	log.Printf("[CVFrameExtractor] Starting frame extraction for service %s (interval=%v)", e.serviceID, e.extractInterval)

	producer := stream.GetProducer()

	// Detect codec (H264 or H265) before starting FFmpeg
	codecName := core.CodecH264 // default
	for _, media := range producer.GetMedias() {
		if media.Kind != core.KindVideo {
			continue
		}
		for _, codec := range media.Codecs {
			if codec.Name == core.CodecH265 {
				codecName = core.CodecH265
				break
			} else if codec.Name == core.CodecH264 {
				codecName = core.CodecH264
				break
			}
		}
		if codecName != "" {
			break
		}
	}
	e.codecName = codecName
	log.Printf("[CVFrameExtractor] Detected video codec %s for service %s", codecName, e.serviceID)

	// Start FFmpeg process
	if err := e.startFFmpeg(codecName); err != nil {
		return fmt.Errorf("failed to start FFmpeg: %w", err)
	}

	// 1) Consume video to FFmpeg stdin (handles H264 and H265 RTP)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.consumeVideoToFFmpeg(stream, producer, codecName)
	}()

	// 2) Read JPEG frames from FFmpeg stdout
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.readFramesFromFFmpeg()
	}()

	// 3) Save frames to disk + emit events
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.saveFrameLoop()
	}()

	// 4) Batch flush loop (size OR max-wait)
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		e.batchFlushLoop()
	}()

	// 5) When stream closes, stop extractor
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		select {
		case <-stream.CloseChan():
			e.Close()
		case <-e.closeChan:
			return
		}
	}()

	return nil
}

// startFFmpeg starts the FFmpeg process for H.264 or H.265 to JPEG conversion
func (e *CVFrameExtractor) startFFmpeg(codecName string) error {
	e.ffmpegMu.Lock()
	defer e.ffmpegMu.Unlock()

	// Calculate fps from interval (e.g., 5s interval = 1/5 fps = 0.2 fps)
	secs := e.extractInterval.Seconds()
	if secs <= 0 {
		secs = 1
	}
	fps := 1.0 / secs

	// Pipe MPEG-TS instead of raw H264/H265. The RTSP depayloaders emit AVCC
	// access units; muxing them into TS preserves access-unit boundaries and
	// timestamps, and lets FFmpeg handle the elementary stream parsing.
	inputFmt := "mpegts"

	cmd := exec.Command(
		"ffmpeg",
		"-loglevel", "error",
		"-fflags", "+genpts+discardcorrupt", // genpts + discard corrupt packets (helps with high-res streams)
		"-thread_queue_size", "1024", // larger input thread queue; prevents stalls with 720p+ bitrates
		"-f", inputFmt,
		"-i", "pipe:0",
		"-vf", fmt.Sprintf("fps=%.6f", fps),
		"-f", "image2pipe",
		"-c:v", "mjpeg",
		"-q:v", "2",
		"pipe:1",
	)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return fmt.Errorf("start: %w", err)
	}

	e.ffmpegCmd = cmd
	e.ffmpegStdin = stdin
	e.ffmpegStdout = stdout

	go func() {
		if err := cmd.Wait(); err != nil && !e.closed.Load() {
			log.Printf("[CVFrameExtractor] FFmpeg exited for service %s: %v", e.serviceID, err)
		}
	}()

	log.Printf("[CVFrameExtractor] Started FFmpeg for service %s (codec=%s, fps=%.6f)", e.serviceID, codecName, fps)
	return nil
}

// safeWrite writes to ffmpeg stdin and closes extractor if stdin is broken.
func (e *CVFrameExtractor) safeWrite(b []byte) {
	if len(b) == 0 {
		return
	}

	e.stdinMu.Lock()
	defer e.stdinMu.Unlock()

	if e.closed.Load() || e.ffmpegStdin == nil {
		return
	}

	if err := writeAll(e.ffmpegStdin, b); err != nil {
		// Broken pipe / process exit -> shutdown extractor
		log.Printf("[CVFrameExtractor] ffmpeg stdin write failed for service %s: %v", e.serviceID, err)
		e.Close()
	}
}

func writeAll(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// consumeVideoToFFmpeg reads H.264 or H.265 packets and pipes MPEG-TS to FFmpeg stdin.
// RTP/AVCC quirks are handled by go2rtc's codec helpers so the extractor uses
// the same packet path as the working live stream consumers.
func (e *CVFrameExtractor) consumeVideoToFFmpeg(stream RealtimeStream, producer core.Producer, codecName string) {
	isH265 := codecName == core.CodecH265
	defer log.Printf("[CVFrameExtractor] Stopped %s consumer for service %s", codecName, e.serviceID)

	// Close stdin when done
	defer func() {
		e.stdinMu.Lock()
		if e.ffmpegStdin != nil {
			_ = e.ffmpegStdin.Close()
		}
		e.stdinMu.Unlock()
	}()

	// Get video media
	var videoMedia *core.Media
	for _, media := range producer.GetMedias() {
		if media.Kind == core.KindVideo {
			videoMedia = media
			break
		}
	}
	if videoMedia == nil {
		log.Printf("[CVFrameExtractor] No video media found for service %s", e.serviceID)
		return
	}

	// Find the matching codec (H264 or H265)
	var videoCodec *core.Codec
	for _, codec := range videoMedia.Codecs {
		if codec.Name == codecName {
			videoCodec = codec
			break
		}
	}
	// Fallback: if exact match not found, use the first video codec
	if videoCodec == nil {
		for _, codec := range videoMedia.Codecs {
			videoCodec = codec
			break
		}
	}
	if videoCodec == nil {
		log.Printf("[CVFrameExtractor] No video codec found for service %s", e.serviceID)
		return
	}

	receiver, err := producer.GetTrack(videoMedia, videoCodec)
	if err != nil {
		log.Printf("[CVFrameExtractor] Failed to get track for service %s: %v", e.serviceID, err)
		return
	}

	seenKeyframe := false
	droppedBeforeKeyframeLogged := false
	lastSequenceGapLog := time.Time{}

	tsMuxer := mpegts.NewMuxer()
	streamType := byte(mpegts.StreamTypeH264)
	if isH265 {
		streamType = byte(mpegts.StreamTypeH265)
	}
	tsPID := tsMuxer.AddTrack(streamType)
	e.safeWrite(tsMuxer.GetHeader())

	var accessUnitHandler core.HandlerFunc = func(pkt *core.Packet) {
		if e.closed.Load() {
			return
		}

		nals, err := parseLengthPrefixedNALs(codecName, pkt.Payload)
		if err != nil {
			log.Printf("[CVFrameExtractor] Dropped malformed %s access unit for service %s: %v", codecName, e.serviceID, err)
			seenKeyframe = false
			droppedBeforeKeyframeLogged = false
			return
		}

		if accessUnitHasKeyframe(codecName, nals) {
			seenKeyframe = true
		}
		if !seenKeyframe && accessUnitHasVCL(codecName, nals) {
			if !droppedBeforeKeyframeLogged {
				log.Printf("[CVFrameExtractor] Waiting for first %s keyframe before feeding access units to FFmpeg for service %s", codecName, e.serviceID)
				droppedBeforeKeyframeLogged = true
			}
			return
		}

		e.safeWrite(tsMuxer.GetPayload(tsPID, pkt.Timestamp, pkt.Payload))
	}

	var packetHandler core.HandlerFunc
	if videoCodec.IsRTP() {
		var depayHandler core.HandlerFunc
		makeDepayHandler := func() core.HandlerFunc {
			if isH265 {
				return h265pkg.RTPDepay(videoCodec, accessUnitHandler)
			}
			return h264pkg.RTPDepay(videoCodec, accessUnitHandler)
		}
		depayHandler = makeDepayHandler()

		var haveSeq bool
		var lastSeq uint16
		packetHandler = func(pkt *core.Packet) {
			if e.closed.Load() {
				return
			}
			if haveSeq {
				expected := lastSeq + 1
				if pkt.SequenceNumber != expected {
					now := time.Now()
					if lastSequenceGapLog.IsZero() || now.Sub(lastSequenceGapLog) >= 5*time.Second {
						log.Printf("[CVFrameExtractor] RTP sequence gap for service %s (%s): expected=%d got=%d; resetting depayloader and waiting for next keyframe",
							e.serviceID, codecName, expected, pkt.SequenceNumber)
						lastSequenceGapLog = now
					}
					seenKeyframe = false
					droppedBeforeKeyframeLogged = false
					depayHandler = makeDepayHandler()
				}
			}
			haveSeq = true
			lastSeq = pkt.SequenceNumber
			depayHandler(pkt)
		}
	} else if isH265 {
		packetHandler = h265pkg.RepairAVCC(videoCodec, accessUnitHandler)
	} else {
		packetHandler = h264pkg.RepairAVCC(videoCodec, accessUnitHandler)
	}

	// Attach directly to the receiver. core.Sender has a bounded, lossy channel;
	// losing even one RTP packet corrupts the H264/H265 byte stream fed to FFmpeg.
	videoTap := &core.Node{
		Codec: videoCodec,
		Input: packetHandler,
	}
	receiver.AppendChild(videoTap)
	log.Printf("[CVFrameExtractor] Started %s -> FFmpeg MPEG-TS pipeline for service %s", codecName, e.serviceID)

	if err := stream.StartProducer(); err != nil {
		if !e.closed.Load() {
			log.Printf("[CVFrameExtractor] Producer stopped for service %s: %v", e.serviceID, err)
		}
		videoTap.Close()
		e.Close()
		return
	}

	videoTap.Close()
	e.Close()
}

func writeAccessUnitNALs(codecName string, payload []byte, writeNAL func([]byte)) error {
	if len(payload) == 0 {
		return nil
	}

	if hasAnnexBStartCode(payload) {
		for _, nal := range splitAnnexBNALs(payload) {
			writeNAL(nal)
		}
		return nil
	}

	if len(payload) >= 4 {
		if err := writeLengthPrefixedNALs(codecName, payload, writeNAL); err == nil {
			return nil
		} else if !isLikelyRawVideoNAL(codecName, payload) {
			return err
		}
	}

	writeNAL(payload)
	return nil
}

func hasAnnexBStartCode(b []byte) bool {
	if len(b) >= 4 && b[0] == 0x00 && b[1] == 0x00 && b[2] == 0x00 && b[3] == 0x01 {
		return true
	}
	return len(b) >= 3 && b[0] == 0x00 && b[1] == 0x00 && b[2] == 0x01
}

func splitAnnexBNALs(b []byte) [][]byte {
	var nals [][]byte
	for {
		start, startLen := nextAnnexBStartCode(b, 0)
		if start < 0 {
			break
		}
		nalStart := start + startLen
		next, _ := nextAnnexBStartCode(b, nalStart)
		if next < 0 {
			if nalStart < len(b) {
				nals = append(nals, b[nalStart:])
			}
			break
		}
		if nalStart < next {
			nals = append(nals, b[nalStart:next])
		}
		b = b[next:]
	}
	return nals
}

func nextAnnexBStartCode(b []byte, from int) (int, int) {
	for i := from; i+3 <= len(b); i++ {
		if i+4 <= len(b) && b[i] == 0x00 && b[i+1] == 0x00 && b[i+2] == 0x00 && b[i+3] == 0x01 {
			return i, 4
		}
		if b[i] == 0x00 && b[i+1] == 0x00 && b[i+2] == 0x01 {
			return i, 3
		}
	}
	return -1, 0
}

func writeLengthPrefixedNALs(codecName string, payload []byte, writeNAL func([]byte)) error {
	nals, err := parseLengthPrefixedNALs(codecName, payload)
	if err != nil {
		return err
	}
	for _, nal := range nals {
		writeNAL(nal)
	}
	return nil
}

func parseLengthPrefixedNALs(codecName string, payload []byte) ([][]byte, error) {
	offset := 0
	nals := make([][]byte, 0, 4)
	for offset < len(payload) {
		if offset+4 > len(payload) {
			return nil, fmt.Errorf("trailing %d byte(s) before NAL length", len(payload)-offset)
		}
		nalLen := int(binary.BigEndian.Uint32(payload[offset:]))
		offset += 4
		if nalLen <= 0 {
			return nil, fmt.Errorf("invalid NAL length %d", nalLen)
		}
		if offset+nalLen > len(payload) {
			return nil, fmt.Errorf("NAL length %d exceeds remaining %d byte(s)", nalLen, len(payload)-offset)
		}
		nal := payload[offset : offset+nalLen]
		if !isLikelyRawVideoNAL(codecName, nal) {
			return nil, fmt.Errorf("invalid %s NAL header in length-prefixed access unit", codecName)
		}
		nals = append(nals, nal)
		offset += nalLen
	}
	return nals, nil
}

func accessUnitHasKeyframe(codecName string, nals [][]byte) bool {
	for _, nal := range nals {
		if isVideoKeyframeNAL(codecName, nal) {
			return true
		}
	}
	return false
}

func accessUnitHasVCL(codecName string, nals [][]byte) bool {
	for _, nal := range nals {
		if isVideoVCLNAL(codecName, nal) {
			return true
		}
	}
	return false
}

func isLikelyRawVideoNAL(codecName string, nal []byte) bool {
	if len(nal) == 0 {
		return false
	}
	if codecName == core.CodecH265 {
		if len(nal) < 2 {
			return false
		}
		nalType := (nal[0] >> 1) & 0x3F
		temporalIDPlusOne := nal[1] & 0x07
		return nalType <= 40 && temporalIDPlusOne != 0
	}
	nalType := nal[0] & 0x1F
	forbiddenZeroBitSet := nal[0]&0x80 != 0
	return !forbiddenZeroBitSet && nalType >= 1 && nalType <= 23
}

func codecParameterSets(codecName, fmtpLine string) [][]byte {
	switch codecName {
	case core.CodecH264:
		sps, pps := h264pkg.GetParameterSet(fmtpLine)
		return compactNALs(sps, pps)
	case core.CodecH265:
		vps, sps, pps := h265pkg.GetParameterSet(fmtpLine)
		return compactNALs(vps, sps, pps)
	default:
		return nil
	}
}

func compactNALs(nals ...[]byte) [][]byte {
	out := make([][]byte, 0, len(nals))
	for _, nal := range nals {
		if len(nal) > 0 {
			out = append(out, nal)
		}
	}
	return out
}

func isVideoKeyframeNAL(codecName string, nal []byte) bool {
	if len(nal) == 0 {
		return false
	}
	if codecName == core.CodecH265 {
		if len(nal) < 2 {
			return false
		}
		return isH265RandomAccessNAL((nal[0] >> 1) & 0x3F)
	}
	return nal[0]&0x1F == 5
}

func isVideoVCLNAL(codecName string, nal []byte) bool {
	if len(nal) == 0 {
		return false
	}
	if codecName == core.CodecH265 {
		if len(nal) < 2 {
			return false
		}
		return ((nal[0] >> 1) & 0x3F) <= 31
	}
	nalType := nal[0] & 0x1F
	return nalType >= 1 && nalType <= 5
}

func isH265RandomAccessNAL(nalType byte) bool {
	return nalType >= 16 && nalType <= 21
}

// readFramesFromFFmpeg reads JPEG frames from FFmpeg stdout
func (e *CVFrameExtractor) readFramesFromFFmpeg() {
	defer log.Printf("[CVFrameExtractor] Stopped JPEG reader for service %s", e.serviceID)
	defer e.flushDroppedFrameLog(true)
	defer close(e.frameReady)

	e.ffmpegMu.Lock()
	stdout := e.ffmpegStdout
	e.ffmpegMu.Unlock()

	if stdout == nil {
		return
	}

	r := bufio.NewReaderSize(stdout, 256*1024)

	for {
		select {
		case <-e.closeChan:
			return
		default:
			jpeg, err := e.readJPEGFrame(r)
			if err != nil {
				// If ffmpeg ended, we should shutdown
				if !errors.Is(err, io.EOF) {
					log.Printf("[CVFrameExtractor] Failed to read JPEG for service %s: %v", e.serviceID, err)
				}
				e.Close()
				return
			}

			if !e.enqueueLatestFrame(jpeg) {
				return
			}
		}
	}
}

func (e *CVFrameExtractor) enqueueLatestFrame(jpeg []byte) bool {
	select {
	case e.frameReady <- jpeg:
		return true
	case <-e.closeChan:
		return false
	default:
	}

	// Prefer a fresh frame over preserving queued stale frames. At 15 FPS, the
	// old 30-frame buffer could add up to two seconds of overlay latency.
	select {
	case <-e.frameReady:
		e.noteDroppedFrame()
	default:
	}

	select {
	case e.frameReady <- jpeg:
		return true
	case <-e.closeChan:
		return false
	default:
		e.noteDroppedFrame()
		return true
	}
}

func (e *CVFrameExtractor) noteDroppedFrame() {
	e.dropLogMu.Lock()
	defer e.dropLogMu.Unlock()

	now := time.Now()
	e.droppedSinceLog++
	if e.lastDroppedLogAt.IsZero() {
		e.lastDroppedLogAt = now
		return
	}
	if now.Sub(e.lastDroppedLogAt) < 5*time.Second {
		return
	}
	log.Printf("[CVFrameExtractor] Dropped/replaced %d stale frame(s) for service %s", e.droppedSinceLog, e.serviceID)
	e.droppedSinceLog = 0
	e.lastDroppedLogAt = now
}

func (e *CVFrameExtractor) flushDroppedFrameLog(force bool) {
	e.dropLogMu.Lock()
	defer e.dropLogMu.Unlock()
	if e.droppedSinceLog <= 0 {
		return
	}
	if !force && !e.lastDroppedLogAt.IsZero() && time.Since(e.lastDroppedLogAt) < 5*time.Second {
		return
	}
	log.Printf("[CVFrameExtractor] Dropped/replaced %d stale frame(s) for service %s", e.droppedSinceLog, e.serviceID)
	e.droppedSinceLog = 0
	e.lastDroppedLogAt = time.Now()
}

// readJPEGFrame reads a single JPEG frame from a buffered reader
func (e *CVFrameExtractor) readJPEGFrame(r *bufio.Reader) ([]byte, error) {
	// JPEG SOI: FF D8
	// JPEG EOI: FF D9

	var out bytes.Buffer

	// Find SOI
	for {
		b1, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b1 != 0xFF {
			continue
		}
		b2, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		if b2 == 0xD8 {
			out.WriteByte(0xFF)
			out.WriteByte(0xD8)
			break
		}
	}

	// Read until EOI
	var prev byte
	for {
		b, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		out.WriteByte(b)

		if prev == 0xFF && b == 0xD9 {
			return out.Bytes(), nil
		}
		prev = b
	}
}

// saveFrameLoop saves frames as they arrive, rate-limited by extractInterval.
// FFmpeg runs at its configured rate; extra frames are dropped here if extractInterval
// is larger (i.e., desired fps is lower than FFmpeg's output fps).
func (e *CVFrameExtractor) saveFrameLoop() {
	defer log.Printf("[CVFrameExtractor] Stopped frame saver for service %s", e.serviceID)

	for {
		select {
		case <-e.closeChan:
			return
		case jpeg, ok := <-e.frameReady:
			if !ok {
				return
			}
			// Per-camera rate limit: skip frames that arrive faster than extractInterval
			e.mu.Lock()
			now := time.Now()
			interval := e.extractInterval
			skip := !e.lastSavedAt.IsZero() && now.Sub(e.lastSavedAt) < interval
			if !skip {
				e.lastSavedAt = now
			}
			e.mu.Unlock()

			if !skip {
				e.saveFrame(jpeg)
			}
		}
	}
}

// saveFrame stores a JPEG frame in memory and emits batch events.
func (e *CVFrameExtractor) saveFrame(jpeg []byte) {
	frameUUID := uuid.New().String()
	timestamp := time.Now()

	// Store in memory; no disk I/O
	if err := e.storageManager.RegisterFrameData(frameUUID, e.serviceID, jpeg, timestamp); err != nil {
		log.Printf("[CVFrameExtractor] Failed to register frame %s: %v", frameUUID, err)
		return
	}

	// Add frame to batch
	var shouldEmitBatch bool
	e.mu.Lock()
	if len(e.frameBatch) == 0 {
		e.batchStartTime = timestamp
	}
	e.frameBatch = append(e.frameBatch, frameUUID)
	shouldEmitBatch = len(e.frameBatch) >= e.batchSize
	e.mu.Unlock()

	// Emit frame event
	e.eventBus.EmitFrameEvent(&FrameEvent{
		ServiceID: e.serviceID,
		FrameUUID: frameUUID,
	})

	if shouldEmitBatch {
		e.emitBatchEvent()
	}
}

// batchFlushLoop ensures partial batches still get emitted after max wait.
func (e *CVFrameExtractor) batchFlushLoop() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-e.closeChan:
			return
		case <-ticker.C:
			e.mu.Lock()
			if len(e.frameBatch) == 0 {
				e.mu.Unlock()
				continue
			}
			age := time.Since(e.batchStartTime)
			maxWait := e.batchMaxWait
			e.mu.Unlock()

			if maxWait > 0 && age >= maxWait {
				e.emitBatchEvent()
			}
		}
	}
}

// emitBatchEvent emits a frame_batch event from collected frames
func (e *CVFrameExtractor) emitBatchEvent() {
	e.mu.Lock()
	if len(e.frameBatch) == 0 {
		e.mu.Unlock()
		return
	}

	batchFrames := make([]string, len(e.frameBatch))
	copy(batchFrames, e.frameBatch)
	batchStart := e.batchStartTime

	e.frameBatch = make([]string, 0, e.batchSize)
	e.batchStartTime = time.Time{}
	e.mu.Unlock()

	batchEnd := time.Now()
	duration := batchEnd.Sub(batchStart).Seconds()

	procFPS := 1.0
	if e.extractInterval.Seconds() > 0 {
		procFPS = 1.0 / e.extractInterval.Seconds()
	}

	e.mu.Lock()
	svcName := e.serviceName
	e.mu.Unlock()

	e.eventBus.EmitFrameBatchEvent(&FrameBatchEvent{
		ServiceID: e.serviceID,
		Frames:    batchFrames,
		Metadata: map[string]interface{}{
			"duration_seconds": duration,
			"vlm_fps":          e.vlmFPSHint,
			"processing_fps":   procFPS,
			"service_name":     svcName,
		},
	})
}

// Close stops frame extraction (idempotent)
func (e *CVFrameExtractor) Close() {
	if e.closed.Swap(true) {
		return
	}

	close(e.closeChan)

	// Stop ffmpeg
	e.ffmpegMu.Lock()
	cmd := e.ffmpegCmd
	stdin := e.ffmpegStdin
	e.ffmpegStdout = nil
	e.ffmpegStdin = nil
	e.ffmpegCmd = nil
	e.ffmpegMu.Unlock()

	if stdin != nil {
		_ = stdin.Close()
	}

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
