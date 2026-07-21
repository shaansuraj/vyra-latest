import { createSignal, onCleanup, onMount, Show, untrack, type Component, createEffect, For, on } from 'solid-js';
import { FaSolidSpinner } from 'solid-icons/fa';
import { configState, connectAgentEventsWebSocket, subscribeToAgentEvents, subscribeToAgentOverlayEvents, relayFetch } from '../shared';

interface Props {
  nodeId: string;
  serviceId?: string;
  name?: string;
  autoStart?: boolean;
  /** Show small overlay controls (start/stop/retry). Default: true */
  showControls?: boolean;
  /** If true, clicking the tile starts the stream when not running. Default: true when autoStart=false */
  startOnClick?: boolean;
  /** Show bounding box overlays. Default: true */
  showBBoxes?: boolean;
  /** Show detection count badges (e.g. "With Helmet: 5 | Without Helmet: 2"). Default: false */
  showDetectionCounts?: boolean;
  /** Called on double-click */
  onDblClick?: () => void;
  /** Visual mode for layout styling */
  displayMode?: 'tile' | 'fullscreen';
}

type BBox = {
  label: string;
  confidence: number;
  x_min: number; // normalized 0..1
  y_min: number; // normalized 0..1
  x_max: number; // normalized 0..1
  y_max: number; // normalized 0..1
  track_id?: number; // ByteTrack persistent ID (-1 = untracked)
  global_person_id?: string; // Cross-camera stable person identity
  person_id?: string; // Face recognition match
  person_name?: string; // Display name for matched person
  match_score?: number; // Cosine similarity (0-1)
  candidate_person_id?: string; // Weak face recognition candidate
  candidate_person_name?: string; // Weak candidate display name
  candidate_score?: number; // Weak candidate cosine similarity
  confidence_tier?: string; // strong | weak | weak_promoted | unknown
};

type OverlayIdentity = Pick<
  BBox,
  'global_person_id' | 'person_id' | 'person_name' | 'match_score' |
  'candidate_person_id' | 'candidate_person_name' | 'candidate_score' | 'confidence_tier'
> & {
  ts: number;
};

const isViolation = (label: string) => {
  const l = (label || '').toLowerCase();
  return l.startsWith('no_') || l.includes('no-') || l.includes('without');
};

const unknownLabelFromGlobalId = (globalId?: string) => {
  const match = String(globalId || '').trim().match(/^unknown:(\d+)$/);
  return match?.[1] ? `Unknown #${match[1]}` : '';
};

const cleanOverlayName = (b: BBox) => {
  const label = (b.label || 'person').replace(/_/g, ' ');
  const trackName = b.track_id != null && b.track_id >= 0 ? `Unknown #${b.track_id}` : 'Unknown';
  const personName = String(b.person_name || '').trim();
  const candidateName = String(b.candidate_person_name || '').trim();
  const globalUnknownName = unknownLabelFromGlobalId(b.global_person_id);

  if (personName && !personName.toLowerCase().startsWith('unknown')) return personName;
  if (globalUnknownName) return globalUnknownName;
  if (personName.toLowerCase().startsWith('unknown')) return personName;
  if (candidateName && b.confidence_tier !== 'unknown') return `${candidateName}?`;
  if (label.toLowerCase() === 'person') return trackName;
  return b.track_id != null && b.track_id >= 0 ? `#${b.track_id} ${label}` : label;
};

const waitForIceGatheringComplete = (pc: RTCPeerConnection, timeoutMs = 2000) =>
  new Promise<void>((resolve) => {
    if (pc.iceGatheringState === 'complete') {
      resolve();
      return;
    }

    let settled = false;
    const finish = () => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      pc.removeEventListener('icegatheringstatechange', onStateChange);
      resolve();
    };

    const onStateChange = () => {
      if (pc.iceGatheringState === 'complete') finish();
    };

    const timer = setTimeout(finish, timeoutMs);
    pc.addEventListener('icegatheringstatechange', onStateChange);
  });

const VideoTile: Component<Props> = (props) => {
  const [videoRef, setVideoRef] = createSignal<HTMLVideoElement>();
  const [pc, setPc] = createSignal<RTCPeerConnection>();
  const initialAutoStart = props.autoStart !== false;
  const [loading, setLoading] = createSignal(initialAutoStart);
  const [error, setError] = createSignal<string | null>(null);
  const [started, setStarted] = createSignal(initialAutoStart);

  // Auto-reconnect state
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  const clearRetryTimer = () => { if (retryTimer) { clearTimeout(retryTimer); retryTimer = undefined; } };

  // Bounding box overlay (driven by AgentEvents)
  const [containerRef, setContainerRef] = createSignal<HTMLDivElement>();
  const [boxes, setBoxes] = createSignal<BBox[]>([]);

  // Detection counts overlay
  const [detectionCounts, setDetectionCounts] = createSignal<Record<string, number>>({});

  // Wellness check moments overlay
  const [wellnessMoments, setWellnessMoments] = createSignal<any[]>([]);
  const [wellnessExpanded, setWellnessExpanded] = createSignal(false);
  const [wellnessHidden, setWellnessHidden] = createSignal(false);

  // Per-agent bbox tracking: merges boxes from PPE + person detection simultaneously
  const agentBoxMap = new Map<string, { boxes: BBox[]; ts: number }>();
  const identityMemory = new Map<string, OverlayIdentity>();

  const BBOX_STALE_MS = 3500;
  const BBOX_IDENTITY_MEMORY_MS = 120000;

  // Throttling for bbox updates - 33ms = ~30fps for smooth rendering
  let lastBboxUpdate = 0;
  let bboxRenderTimer: ReturnType<typeof setTimeout> | undefined;
  const BBOX_UPDATE_INTERVAL = 33;

  const [layout, setLayout] = createSignal({ cw: 0, ch: 0, vw: 0, vh: 0 });

  const bboxEnabled = () => props.showBBoxes !== false;
  const countsEnabled = () => props.showDetectionCounts === true;

  const boxIdentityKey = (agentId: string, b: BBox) => {
    const stableId = b.global_person_id || b.person_id || (b.track_id != null && b.track_id >= 0 ? `track:${b.track_id}` : '');
    return stableId ? `${props.serviceId || 'default'}:${agentId}:${stableId}` : '';
  };

  const hasDisplayIdentity = (b: BBox) => Boolean(
    b.global_person_id || b.person_id || b.person_name || b.match_score != null ||
    b.candidate_person_id || b.candidate_person_name || b.candidate_score != null || b.confidence_tier
  );

  const hydrateBoxIdentity = (agentId: string, raw: BBox, now: number): BBox => {
    const box: BBox = { ...raw };
    const key = boxIdentityKey(agentId, box);
    if (!key) return box;

    const remembered = identityMemory.get(key);
    if (remembered && now - remembered.ts < BBOX_IDENTITY_MEMORY_MS) {
      if (!box.global_person_id && remembered.global_person_id) box.global_person_id = remembered.global_person_id;
      if (!box.person_id && remembered.person_id) box.person_id = remembered.person_id;
      if (!box.person_name && remembered.person_name) box.person_name = remembered.person_name;
      if (box.match_score == null && remembered.match_score != null) box.match_score = remembered.match_score;
      if (!box.candidate_person_id && remembered.candidate_person_id) box.candidate_person_id = remembered.candidate_person_id;
      if (!box.candidate_person_name && remembered.candidate_person_name) box.candidate_person_name = remembered.candidate_person_name;
      if (box.candidate_score == null && remembered.candidate_score != null) box.candidate_score = remembered.candidate_score;
      if (!box.confidence_tier && remembered.confidence_tier) box.confidence_tier = remembered.confidence_tier;
    }

    if (hasDisplayIdentity(box)) {
      identityMemory.set(key, {
        global_person_id: box.global_person_id,
        person_id: box.person_id,
        person_name: box.person_name,
        match_score: box.match_score,
        candidate_person_id: box.candidate_person_id,
        candidate_person_name: box.candidate_person_name,
        candidate_score: box.candidate_score,
        confidence_tier: box.confidence_tier,
        ts: now,
      });
    }
    return box;
  };

  const rebuildMergedBoxes = (now = Date.now()) => {
    for (const [key, value] of identityMemory) {
      if (now - value.ts >= BBOX_IDENTITY_MEMORY_MS) identityMemory.delete(key);
    }

    const merged: BBox[] = [];
    for (const entry of agentBoxMap.values()) {
      if (now - entry.ts < BBOX_STALE_MS) merged.push(...entry.boxes);
    }

    if (bboxEnabled()) setBoxes(merged);

    if (countsEnabled()) {
      const counts: Record<string, number> = {};
      for (const box of merged) {
        const label = box.label || 'unknown';
        counts[label] = (counts[label] || 0) + 1;
      }
      setDetectionCounts(counts);
    } else if (merged.length === 0) {
      setDetectionCounts({});
    }

    lastBboxUpdate = now;
  };

  const scheduleBboxRender = () => {
    const now = Date.now();
    const elapsed = now - lastBboxUpdate;
    if (elapsed >= BBOX_UPDATE_INTERVAL) {
      if (bboxRenderTimer) {
        clearTimeout(bboxRenderTimer);
        bboxRenderTimer = undefined;
      }
      rebuildMergedBoxes(now);
      return;
    }
    if (!bboxRenderTimer) {
      bboxRenderTimer = setTimeout(() => {
        bboxRenderTimer = undefined;
        rebuildMergedBoxes();
      }, BBOX_UPDATE_INTERVAL - elapsed);
    }
  };

  const updateLayout = () => {
    const c = untrack(containerRef);
    const v = untrack(videoRef);
    if (!c || !v) return;
    const vw = v.videoWidth || 0;
    const vh = v.videoHeight || 0;
    setLayout({ cw: c.clientWidth || 0, ch: c.clientHeight || 0, vw, vh });
  };

  createEffect(() => {
    const c = containerRef();
    const v = videoRef();
    if (!c || !v) return;

    const ro = new ResizeObserver(() => updateLayout());
    ro.observe(c);
    const onMeta = () => updateLayout();
    v.addEventListener('loadedmetadata', onMeta);
    v.addEventListener('resize', onMeta);

    // first pass
    updateLayout();

    onCleanup(() => {
      ro.disconnect();
      v.removeEventListener('loadedmetadata', onMeta);
      v.removeEventListener('resize', onMeta);
    });
  });

  const boxStyle = (b: BBox) => {
    const { cw, ch, vw, vh } = layout();
    if (!cw || !ch || !vw || !vh) return {} as any;

    // We use object-contain, so compute the displayed video rectangle (letterboxed).
    const scale = Math.min(cw / vw, ch / vh);
    const dw = vw * scale;
    const dh = vh * scale;
    const ox = (cw - dw) / 2;
    const oy = (ch - dh) / 2;

    const left = ox + b.x_min * dw;
    const top = oy + b.y_min * dh;
    const width = Math.max(0, (b.x_max - b.x_min) * dw);
    const height = Math.max(0, (b.y_max - b.y_min) * dh);

    return {
      left: `${left}px`,
      top: `${top}px`,
      width: `${width}px`,
      height: `${height}px`,
      transition: 'left 60ms linear, top 60ms linear, width 60ms linear, height 60ms linear',
    };
  };

  const stopStream = (opts?: { silent?: boolean }) => {
    clearRetryTimer();
    const peerConnection = untrack(pc);
    if (peerConnection) {
      // Null out the ICE handler BEFORE closing so the 'closed' event doesn't
      // trigger auto-reconnect for intentional stops.
      peerConnection.oniceconnectionstatechange = null;
      try { peerConnection.close(); } catch {}
    }
    setPc(undefined);

    const videoEl = untrack(videoRef);
    if (videoEl) {
      try { videoEl.srcObject = null; } catch {}
    }

    setLoading(false);
    if (!opts?.silent) setError(null);
    setStarted(false);
  };

  // Schedule an auto-reconnect. Only fires if autoStart is on (or user manually started).
  const scheduleReconnect = (delayMs: number) => {
    clearRetryTimer();
    retryTimer = setTimeout(() => {
      retryTimer = undefined;
      if (!untrack(pc)) startStream();
    }, delayMs);
  };

  const startStream = async () => {
    // Prevent duplicate connections
    if (untrack(pc)) return;

    const videoEl = untrack(videoRef);
    if (!videoEl) {
      setError('Video element not found');
      setLoading(false);
      return;
    }

    let newPc: RTCPeerConnection | null = null;
    try {
      setError(null);
      setLoading(true);
      setStarted(true);

      const iceServers = configState().WEBRTC_ICE_SERVERS?.length
        ? configState().WEBRTC_ICE_SERVERS
        : [{ urls: 'stun:stun.l.google.com:19302' }];

      newPc = new RTCPeerConnection({
        iceServers,
      });

      newPc.ontrack = (event) => {
        console.log(`Got ${event.track.kind} track:`, event.track.label || event.track.id);
        videoEl.srcObject = event.streams[0];
        setLoading(false);
      };

      newPc.oniceconnectionstatechange = () => {
        const state = newPc!.iceConnectionState;
        console.log('ICE state:', state);

        if (state === 'connected' || state === 'completed') {
          // Healthy — cancel any pending reconnect timer
          clearRetryTimer();
          setError(null);
        } else if (state === 'failed') {
          // Unrecoverable — clean up, show error, then auto-reconnect
          stopStream({ silent: true });
          setError('Connection failed');
          if (props.autoStart !== false) scheduleReconnect(3000);
        } else if (state === 'closed') {
          // Server closed our session (ICE handler was nulled before intentional closes).
          // Auto-reconnect if this tile should be playing.
          stopStream({ silent: true });
          if (props.autoStart !== false) scheduleReconnect(1500);
        }
        // 'disconnected' is left alone: ICE can self-recover back to 'connected'
        // within ~5s; if it can't, it transitions to 'failed' which we handle above.
      };

      // Add transceivers for video and audio
      newPc.addTransceiver('video', { direction: 'recvonly' });
      newPc.addTransceiver('audio', { direction: 'recvonly' });

      const offer = await newPc.createOffer();
      await newPc.setLocalDescription(offer);
      await waitForIceGatheringComplete(newPc);

      const response = await relayFetch(`/nodes/${props.nodeId}/offer`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          sdp: newPc.localDescription?.sdp,
          serviceId: props.serviceId || '',
        }),
      });

      if (!response.ok) {
        const details = (await response.text().catch(() => '')).trim();
        throw new Error(details || `Failed to connect to stream (HTTP ${response.status})`);
      }

      const answer = await response.json();
      await newPc.setRemoteDescription(
        new RTCSessionDescription({
          type: 'answer',
          sdp: answer.sdp,
        })
      );

      setPc(newPc);
    } catch (e: any) {
      console.error(e);
      setError(e.message || 'Stream error');
      setLoading(false);
      setStarted(false);

      if (newPc) {
        try {
          newPc.close();
        } catch {}
      }
      setPc(undefined);
    }
  };

  onMount(() => {
    // Ensure agent-events WS is up so the tile can render bounding boxes.
    connectAgentEventsWebSocket();

    const handleEvent = (evt: any) => {
      if (!props.serviceId) return;

      // CRITICAL: Check metadata.service_id first (event origin) for multi-camera isolation
      const metadataServiceId = (evt as any)?.metadata?.service_id;
      if (metadataServiceId && typeof metadataServiceId === 'string') {
        // Only process if metadata.service_id matches this VideoTile's camera
        if (metadataServiceId !== props.serviceId) return;
      } else {
        // Fallback: check service_ids array (agent assignment)
        const serviceIds = Array.isArray((evt as any)?.service_ids)
          ? ((evt as any).service_ids as string[])
          : (typeof (evt as any)?.service_id === 'string' ? [String((evt as any).service_id)] : []);

        if (!serviceIds.includes(props.serviceId)) return;
      }

      const meta = (evt as any)?.metadata ?? {};
      const agentType = (meta as any)?.agent_type;
      const isRealtimeOverlay = (meta as any)?.realtime_only === true;

      // Wellness moments go to overlay
      if (agentType === 'situational' || agentType === 'wellness') {
        setWellnessMoments(prev => [evt, ...prev].slice(0, 10));
      }

      // Bounding boxes + detection counts with per-agent merge and throttling
      const b = (meta as any)?.bboxes;
      if (isRealtimeOverlay && Array.isArray(b) && b.length > 0) {
        const now = Date.now();
        // Use agent_id or agent_type as per-agent key so multiple workers coexist.
        const agentId = (evt as any)?.agent_id || (meta?.agent_type as string) || 'default';
        const hydrated = (b as BBox[]).map((box) => hydrateBoxIdentity(agentId, box, now));
        agentBoxMap.set(agentId, { boxes: hydrated, ts: now });
        scheduleBboxRender();
      }
    };

    const unsubscribe = subscribeToAgentEvents(handleEvent);
    const unsubscribeOverlay = subscribeToAgentOverlayEvents(handleEvent);

    if (props.autoStart !== false) {
      startStream();
    }

    // Prune stale per-agent bboxes every 500ms and rebuild merged set
    const boxPruneInterval = setInterval(() => {
      const now = Date.now();
      let anyRemoved = false;
      for (const [key, entry] of agentBoxMap) {
        if (now - entry.ts >= BBOX_STALE_MS) {
          agentBoxMap.delete(key);
          anyRemoved = true;
        }
      }
      for (const [key, value] of identityMemory) {
        if (now - value.ts >= BBOX_IDENTITY_MEMORY_MS) {
          identityMemory.delete(key);
          anyRemoved = true;
        }
      }
      if (anyRemoved) {
        rebuildMergedBoxes(now);
      }
    }, 250);

    onCleanup(() => {
      clearInterval(boxPruneInterval);
      if (bboxRenderTimer) clearTimeout(bboxRenderTimer);
      unsubscribe();
      unsubscribeOverlay();
    });
  });

  // React to autoStart prop changes after initial mount
  // (e.g. Bird Eye "Auto-play" toggle switching from false → true)
  createEffect(on(
    () => props.autoStart !== false,
    (shouldPlay) => {
      if (shouldPlay && !untrack(started) && !untrack(pc)) {
        startStream();
      }
    },
    { defer: true }
  ));

  onCleanup(() => {
    clearRetryTimer();
    stopStream();
  });

  const controlsEnabled = () => props.showControls !== false;
  const clickToStartEnabled = () => {
    if (props.startOnClick !== undefined) return props.startOnClick;
    return props.autoStart === false; // default true when autoStart=false
  };

  const canStart = () => !started() && !loading() && !error();
  const canRetry = () => !!error() && !loading();

  // Wellness moments filtered to last 5 minutes (stale ones auto-hidden)
  const visibleMoments = () => {
    const now = Date.now();
    return wellnessMoments().filter(
      m => now - new Date(m.created_at).getTime() < 5 * 60_000
    );
  };

  // Latest wellness severity for the compact badge
  const latestWellness = () => {
    const moments = visibleMoments();
    if (!moments.length) return null;
    return moments[0];
  };

  const severityBadgeColor = (severity: string) => {
    switch (severity) {
      case 'high': return 'bg-red-500/90 text-white';
      case 'medium': return 'bg-yellow-500/90 text-black';
      default: return 'bg-green-500/90 text-black';
    }
  };

  // Format detection counts for display
  const formattedCounts = () => {
    const counts = detectionCounts();
    const entries = Object.entries(counts);
    if (entries.length === 0) return [];
    // Sort: violations first, then by count desc
    return entries.sort((a, b) => {
      const aViol = isViolation(a[0]) ? 0 : 1;
      const bViol = isViolation(b[0]) ? 0 : 1;
      if (aViol !== bViol) return aViol - bViol;
      return b[1] - a[1];
    });
  };

  const formatLabel = (label: string) => {
    return label.replace(/_/g, ' ').replace(/\b\w/g, c => c.toUpperCase());
  };

  return (
    <div
      ref={setContainerRef}
      class={`relative w-full h-full bg-black flex items-center justify-center overflow-hidden ${props.displayMode === 'fullscreen' ? 'rounded-none' : 'rounded-2xl'}`}
      onClick={() => {
        if (!clickToStartEnabled()) return;
        if (canStart() || canRetry()) startStream();
      }}
      onDblClick={(e) => {
        if (props.onDblClick) {
          e.stopPropagation();
          props.onDblClick();
        }
      }}
      onKeyDown={(e) => {
        if (!clickToStartEnabled()) return;
        if (e.key === 'Enter' || e.key === ' ') {
          e.preventDefault();
          if (canStart() || canRetry()) startStream();
        }
      }}
      role={clickToStartEnabled() ? 'button' : undefined}
      tabIndex={clickToStartEnabled() ? 0 : undefined}
    >
      <Show
        when={error()}
        fallback={
          <>
            <Show when={loading()}>
              <div class="absolute inset-0 flex items-center justify-center">
                <FaSolidSpinner class="w-8 h-8 text-neu-400 animate-spin" />
              </div>
            </Show>

            <video ref={setVideoRef} autoplay playsinline muted class="w-full h-full object-contain" />

            {/* Bounding boxes overlay (from agent events) - with smooth CSS transitions */}
            <Show when={bboxEnabled() && boxes().length > 0}>
              <div class="pointer-events-none absolute inset-0">
                <For each={boxes()}>
	                  {(b) => {
	                    const viol = isViolation(b.label);
	                    const borderColor = viol ? 'rgba(248,113,113,0.9)' : 'rgba(74,222,128,0.9)';
	                    const tagBg = viol ? 'rgba(239,68,68,0.9)' : 'rgba(34,197,94,0.9)';
	                    const tagColor = viol ? '#fff' : '#000';
	                    const displayName = cleanOverlayName(b);
	                    const displayScore = b.match_score ?? b.candidate_score;
	                    const title = [
	                      b.person_name ? `person_name: ${b.person_name}` : '',
	                      b.global_person_id ? `global_person_id: ${b.global_person_id}` : '',
	                      b.candidate_person_name ? `candidate: ${b.candidate_person_name}` : '',
	                      b.confidence_tier ? `tier: ${b.confidence_tier}` : '',
	                      displayScore != null ? `score: ${Number(displayScore).toFixed(3)}` : '',
	                    ].filter(Boolean).join(' | ') || undefined;

	                    return (
                      <div
                        class="absolute rounded-sm"
                        style={{
                          ...(boxStyle(b) as any),
                          border: `2px solid ${borderColor}`,
                          'box-shadow': `0 0 6px ${borderColor}`,
                        }}
                      >
                        <div
                          class="absolute left-0 bottom-0 px-1.5 py-0.5 rounded-tr-sm text-[10px] font-semibold whitespace-nowrap"
                          style={{
                            background: tagBg,
                            color: tagColor,
                          }}
	                          title={title}
	                        >
	                          <div class="flex items-center gap-1 min-w-0">
	                            <span>
	                              {displayName}{' '}{Math.round((b.confidence ?? 0) * 100)}%
	                            </span>
	                          </div>
                        </div>
                      </div>
                    );
                  }}
                </For>
              </div>
            </Show>

            {/* Detection counts overlay */}
            <Show when={countsEnabled() && formattedCounts().length > 0}>
              <div class="pointer-events-none absolute top-2 left-2 z-10 flex flex-wrap gap-1">
                <For each={formattedCounts()}>
                  {([label, count]) => {
                    const viol = isViolation(label);
                    return (
                      <div
                        class={`px-2 py-0.5 rounded text-[11px] font-semibold ${
                          viol
                            ? 'bg-red-500/85 text-white'
                            : 'bg-green-500/85 text-black'
                        }`}
                      >
                        {formatLabel(label)}: {count}
                      </div>
                    );
                  }}
                </For>
              </div>
            </Show>

            {/* Wellness Check - Compact bottom-right overlay */}
            <Show when={latestWellness() && !wellnessHidden()}>
              <div class="absolute bottom-2 right-2 z-10 pointer-events-auto" style={{ 'max-width': '360px' }}>
                {/* Compact badge - always visible */}
                <div
                  class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-black/60 backdrop-blur-sm cursor-pointer border border-white/10 hover:border-white/25 transition-all"
                  onClick={(e) => {
                    e.stopPropagation();
                    setWellnessExpanded(!wellnessExpanded());
                  }}
                >
                  <span
                    class={`inline-flex items-center px-1.5 py-0.5 rounded text-[10px] font-bold uppercase ${severityBadgeColor(
                      (latestWellness()?.data as any)?.severity || 'low'
                    )}`}
                  >
                    {(latestWellness()?.data as any)?.severity || 'low'}
                  </span>
                  <span class="text-white text-xs truncate max-w-[180px]">
                    {(latestWellness()?.data as any)?.summary?.slice(0, 60) ||
                      (latestWellness()?.data as any)?.response?.slice(0, 60) ||
                      'Wellness update'}
                  </span>
                  <span class="text-white/50 text-[10px] ml-auto">
                    {wellnessExpanded() ? '\u25BC' : '\u25B2'} {visibleMoments().length}
                  </span>
                  <button
                    class="ml-1 text-white/60 hover:text-white text-[11px]"
                    onClick={(e) => {
                      e.stopPropagation();
                      setWellnessExpanded(false);
                      setWellnessHidden(true);
                    }}
                    title="Hide moments overlay"
                  >
                    x
                  </button>
                </div>

                {/* Expanded panel */}
                <Show when={wellnessExpanded()}>
                  <div class="mt-1 bg-black/65 backdrop-blur-sm rounded-lg border border-white/10 max-h-44 overflow-y-auto">
                    <div class="sticky top-0 z-10 bg-black/70 px-3 py-1.5 border-b border-white/10 flex items-center justify-between">
                      <span class="text-[11px] text-white/80">Situational Moments</span>
                      <button
                        class="text-[11px] text-white/70 hover:text-white"
                        onClick={(e) => {
                          e.stopPropagation();
                          setWellnessExpanded(false);
                        }}
                      >
                        Close
                      </button>
                    </div>
                    <For each={visibleMoments().slice(0, 3)}>
                      {(moment) => {
                        const severity = (moment.data as any)?.severity || 'low';
                        return (
                          <div class="px-3 py-2 border-b border-white/5 last:border-0">
                            <div class="flex items-center gap-2 mb-1">
                              <span
                                class={`inline-flex px-1 py-0.5 rounded text-[9px] font-bold uppercase ${severityBadgeColor(severity)}`}
                              >
                                {severity}
                              </span>
                              <span class="text-white/50 text-[10px]">
                                {new Date(moment.created_at).toLocaleTimeString()}
                              </span>
                            </div>
                            <div class="text-white text-xs leading-relaxed">
                              {(moment.data as any)?.summary || (moment.data as any)?.response || 'No summary'}
                            </div>
                          </div>
                        );
                      }}
                    </For>
                  </div>
                </Show>
              </div>
            </Show>

            <Show when={wellnessHidden() && latestWellness()}>
              <button
                class="absolute bottom-2 right-2 z-10 px-2.5 py-1 rounded-lg bg-black/65 border border-white/10 text-white/80 text-[11px] hover:text-white"
                onClick={(e) => {
                  e.stopPropagation();
                  setWellnessHidden(false);
                }}
              >
                Show moments
              </button>
            </Show>

            {/* Click-to-start overlay */}
            <Show when={!loading() && !error() && !started()}>
              <div class="absolute inset-0 flex items-center justify-center">
                <div class="px-3 py-2 rounded-xl bg-black/70 text-white text-sm">Click to start</div>
              </div>
            </Show>

            {/* Minimal overlay controls */}
            <Show when={controlsEnabled()}>
              <div class="absolute top-2 right-2 flex gap-2">
                <Show when={started()}>
                  <button
                    class="px-2 py-1 rounded-lg bg-black/70 text-white text-xs hover:bg-black/80"
                    onClick={(e) => {
                      e.stopPropagation();
                      stopStream();
                    }}
                  >
                    Stop
                  </button>
                </Show>
                <Show when={!started() && !loading() && !error()}>
                  <button
                    class="px-2 py-1 rounded-lg bg-black/70 text-white text-xs hover:bg-black/80"
                    onClick={(e) => {
                      e.stopPropagation();
                      startStream();
                    }}
                  >
                    Start
                  </button>
                </Show>
              </div>
            </Show>

            <Show when={props.name}>
              <div class="absolute bottom-2 left-2 px-2 py-1 bg-black/70 text-white text-sm rounded">
                {props.name}
              </div>
            </Show>
          </>
        }
      >
        <div class="absolute inset-0 flex flex-col items-center justify-center gap-2 p-3">
          <div class="text-red-400 text-sm text-center">{error()}</div>
          <Show when={controlsEnabled() || clickToStartEnabled()}>
            <button
              class="px-3 py-2 rounded-xl bg-black/70 text-white text-sm hover:bg-black/80"
              onClick={(e) => {
                e.stopPropagation();
                startStream();
              }}
            >
              Retry
            </button>
          </Show>
        </div>
      </Show>
    </div>
  );
};

export default VideoTile;
