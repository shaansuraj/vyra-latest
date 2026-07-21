import { Component, For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import {
  agents,
  cameras,
  fetchAgents,
  fetchCameras,
  nodes,
  fetchNodes,
  connectAgentEventsWebSocket,
  subscribeToAgentEvents,
  requestAgentEvents,
  searchAgentEventsRest,
  fetchEventsForDate,
  setTab,
  relay,
  getToken,
  type AgentEvent
} from '../../shared';
import { FiCamera, FiClock, FiFilter, FiAlertTriangle, FiCheckCircle, FiActivity, FiX, FiZoomIn, FiSearch, FiUser, FiCalendar } from 'solid-icons/fi';

interface MomentWithSnapshot extends AgentEvent {
  snapshot_url?: string;
}

// ── Snapshot URL builder ─────────────────────────────────────────────────────
// <img> tags cannot set Authorization headers, so we pass the JWT as a query param.
const buildSnapshotUrl = (path: string) => {
  const base = relay(`/storage/snapshots/${path}`);
  const token = getToken();
  return token ? `${base}?token=${encodeURIComponent(token)}` : base;
};

// ── Image viewer ─────────────────────────────────────────────────────────────
const ImageViewer: Component<{ src: string; onClose: () => void }> = (props) => {
  const [scale, setScale] = createSignal(1);
  const [pos, setPos] = createSignal({ x: 0, y: 0 });
  const [dragging, setDragging] = createSignal(false);
  const [dragStart, setDragStart] = createSignal({ x: 0, y: 0 });

  const handleWheel = (e: WheelEvent) => {
    e.preventDefault();
    setScale((s) => Math.max(0.5, Math.min(5, s + (e.deltaY > 0 ? -0.15 : 0.15))));
  };

  return (
    <div
      class="fixed inset-0 z-[100] bg-black/95 backdrop-blur-sm flex items-center justify-center"
      onClick={props.onClose}
      onWheel={handleWheel}
    >
      <div class="absolute top-4 right-4 flex gap-2 z-10">
        <button class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20 transition-colors"
          onClick={(e) => { e.stopPropagation(); setScale((s) => Math.min(5, s + 0.5)); }}>
          + Zoom
        </button>
        <button class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20 transition-colors"
          onClick={(e) => { e.stopPropagation(); setScale((s) => Math.max(0.5, s - 0.5)); }}>
          − Zoom
        </button>
        <button class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20 transition-colors"
          onClick={(e) => { e.stopPropagation(); setScale(1); setPos({ x: 0, y: 0 }); }}>
          Reset
        </button>
        <button class="px-3 py-1.5 rounded-lg bg-red-500/80 text-white text-sm hover:bg-red-500 transition-colors"
          onClick={props.onClose}>
          <FiX size={14} class="inline mr-1" />Close
        </button>
      </div>
      <div class="absolute bottom-4 left-1/2 -translate-x-1/2 text-white/40 text-xs">
        Scroll to zoom · Click outside to close
      </div>
      <img
        src={props.src}
        alt="Snapshot"
        class="max-w-[90vw] max-h-[85vh] object-contain select-none rounded-lg"
        style={{
          transform: `scale(${scale()}) translate(${pos().x / scale()}px, ${pos().y / scale()}px)`,
          cursor: scale() > 1 ? (dragging() ? 'grabbing' : 'grab') : 'zoom-in',
          transition: dragging() ? 'none' : 'transform 0.15s ease-out',
        }}
        onClick={(e) => e.stopPropagation()}
        onMouseDown={(e) => {
          if (scale() <= 1) return;
          e.preventDefault();
          setDragging(true);
          setDragStart({ x: e.clientX - pos().x, y: e.clientY - pos().y });
        }}
        onMouseMove={(e) => {
          if (!dragging()) return;
          setPos({ x: e.clientX - dragStart().x, y: e.clientY - dragStart().y });
        }}
        onMouseUp={() => setDragging(false)}
        onMouseLeave={() => setDragging(false)}
        draggable={false}
      />
    </div>
  );
};

// ── Helpers ──────────────────────────────────────────────────────────────────
const SEVERITY_CONFIG = {
  high:    { bar: 'bg-red-500',    badge: 'bg-red-500/20 text-red-400 border-red-500/30',    icon: '🔴', label: 'High' },
  medium:  { bar: 'bg-amber-500',  badge: 'bg-amber-500/20 text-amber-400 border-amber-500/30',  icon: '🟡', label: 'Medium' },
  low:     { bar: 'bg-green-500',  badge: 'bg-green-500/20 text-green-400 border-green-500/30',  icon: '🟢', label: 'Low' },
  unknown: { bar: 'bg-neu-700',    badge: 'bg-neu-800 text-neu-400 border-neu-700',              icon: '⚪', label: '' },
};

const AGENT_TYPE_CONFIG: Record<string, { label: string; color: string }> = {
  ppe:  { label: 'PPE Detection',   color: 'bg-violet-500/20 text-violet-300 border-violet-500/30' },
  person: { label: 'Person Detection', color: 'bg-blue-500/20 text-blue-300 border-blue-500/30' },
  situational: { label: 'Situational Awareness', color: 'bg-cyan-500/20 text-cyan-300 border-cyan-500/30' },
  face: { label: 'Face Recognition', color: 'bg-green-500/20 text-green-300 border-green-500/30' },
  unknown:  { label: 'Agent',          color: 'bg-neu-800 text-neu-400 border-neu-700' },
};

const normSeverity = (s?: string): keyof typeof SEVERITY_CONFIG =>
  s === 'high' || s === 'medium' || s === 'low' ? s : 'unknown';

const normAgentType = (t?: string): keyof typeof AGENT_TYPE_CONFIG => {
  if (!t) return 'unknown';
  const l = t.toLowerCase();
  if (l === 'ppe' || l === 'helmet' || l === 'helmet_detection' || l.includes('ppe')) return 'ppe';
  if (l === 'person' || l === 'security') return 'person';
  if (l === 'situational' || l === 'wellness') return 'situational';
  if (l === 'face' || l.includes('face_recog')) return 'face';
  return 'unknown';
};

const isViolLabel = (label: string) =>
  label.startsWith('no_') || label.startsWith('NO-') ||
  label.includes('without') || label.includes('Without') ||
  label === 'Fall-Detected';

const humanizeLabel = (label: string) =>
  label.replace(/[-_]/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());

const relTime = (iso: string) => {
  const diff = Date.now() - new Date(iso).getTime();
  if (diff < 60_000) return `${Math.floor(diff / 1000)}s ago`;
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)}m ago`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)}h ago`;
  return new Date(iso).toLocaleDateString();
};

const localDateStr = (d = new Date()) =>
  `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;

const todayStr = () => localDateStr();

// ── Main component ───────────────────────────────────────────────────────────
const TabMoments: Component = () => {
  const [events, setEvents] = createSignal<MomentWithSnapshot[]>([]);
  const [filterServiceId, setFilterServiceId] = createSignal('all');
  const [filterAgentId, setFilterAgentId] = createSignal('all');
  const [filterAgentType, setFilterAgentType] = createSignal('all');
  const [filterSeverity, setFilterSeverity] = createSignal('all');
  const [filterText, setFilterText] = createSignal('');
  const [filterTimeFrom, setFilterTimeFrom] = createSignal('');
  const [filterTimeTo, setFilterTimeTo] = createSignal('');
  const [selectedDate, setSelectedDate] = createSignal(todayStr());
  const [isHistorical, setIsHistorical] = createSignal(false);
  const [dateLoading, setDateLoading] = createSignal(false);
  const [viewingImage, setViewingImage] = createSignal<string | null>(null);
  const [dbSearching, setDbSearching] = createSignal(false);
  const [showAdvancedFilters, setShowAdvancedFilters] = createSignal(false);

  let unsubscribe: (() => void) | null = null;

  const ingestEvent = (e: AgentEvent) => {
    const m = e as MomentWithSnapshot;
    const snapshotPath = (m.metadata as any)?.snapshot_path || (m.data as any)?.snapshot_path;
    if (snapshotPath) m.snapshot_url = buildSnapshotUrl(snapshotPath);
    setEvents((prev) => [m, ...prev].slice(0, 500));
  };

  const loadDateEvents = async (date: string) => {
    const isToday = date === todayStr();
    setIsHistorical(!isToday);
    setDateLoading(true);
    try {
      const results = await fetchEventsForDate(date, { limit: 500 });
      const mapped = results.map((e) => {
        const m = e as MomentWithSnapshot;
        const sp = (m.metadata as any)?.snapshot_path || (m.data as any)?.snapshot_path;
        if (sp) m.snapshot_url = buildSnapshotUrl(sp);
        return m;
      });
      setEvents(mapped);
    } finally {
      setDateLoading(false);
    }
  };

  const runDbSearch = async () => {
    const q = filterText().trim();
    if (!q) return;
    setDbSearching(true);
    try {
      const results = await searchAgentEventsRest(q, { limit: 200 });
      if (results.length > 0) {
        setEvents((prev) => {
          const seen = new Set(prev.map((e) => e.id));
          const fresh = results.filter((e) => !seen.has(e.id)).map((e) => {
            const m = e as MomentWithSnapshot;
            const sp = (m.metadata as any)?.snapshot_path || (m.data as any)?.snapshot_path;
            if (sp) m.snapshot_url = buildSnapshotUrl(sp);
            return m;
          });
          return [...fresh, ...prev].slice(0, 1000);
        });
      }
    } finally {
      setDbSearching(false);
    }
  };

  onMount(async () => {
    await Promise.all([fetchAgents(), fetchNodes(), fetchCameras()]);
    connectAgentEventsWebSocket();
    // Subscribe to live events — ingestEvent skips when historical mode is on
    unsubscribe = subscribeToAgentEvents((e) => {
      if (!isHistorical()) ingestEvent(e);
    });
    // Load today's events from REST on mount for initial data
    await loadDateEvents(todayStr());
  });

  onCleanup(() => { unsubscribe?.(); unsubscribe = null; });

  const allServices = createMemo(() => {
    // Merge node-announced services with DB cameras (deduped by id)
    const fromNodes = nodes().flatMap((n) => n.services || []);
    const fromCameras = cameras().map((c) => ({ id: c.id, name: c.name, type: c.type }));
    const seen = new Set<string>();
    const result: { id: string; name: string; type?: string }[] = [];
    for (const s of [...fromNodes, ...fromCameras]) {
      if (!seen.has(s.id)) {
        seen.add(s.id);
        result.push(s);
      }
    }
    return result;
  });

  const serviceName = (sid: string) => {
    // Check managed cameras (DB) first — these have human-readable names
    const cam = cameras().find((c) => c.id === sid);
    if (cam) return cam.name;
    // Fall back to services announced by connected nodes
    for (const n of nodes()) {
      const s = (n.services || []).find((x) => x.id === sid);
      if (s) return s.name || s.id;
    }
    return sid.slice(0, 8);
  };

  const nodeIdForService = (sid: string) => {
    for (const n of nodes()) {
      if ((n.services || []).some((s) => s.id === sid)) return n.id;
    }
    return '';
  };

  const filteredEvents = createMemo(() => {
    const sid = filterServiceId();
    const aid = filterAgentId();
    const atype = filterAgentType();
    const sev = filterSeverity();
    const text = filterText().toLowerCase().trim();
    const timeFrom = filterTimeFrom();
    const timeTo = filterTimeTo();
    return events().filter((e) => {
      const meta: any = e.metadata || {};
      const data: any = e.data || {};
      if (sid !== 'all' && !(e.service_ids || []).includes(sid)) return false;
      if (aid !== 'all' && e.agent_id !== aid) return false;
      if (atype !== 'all' && normAgentType(meta.agent_type) !== atype) return false;
      const severity = normSeverity(data.severity || meta.severity);
      if (sev !== 'all' && severity !== sev) return false;
      // Time range filter (HH:MM strings)
      if (timeFrom || timeTo) {
        const hhmm = new Date(e.created_at).toLocaleTimeString('en-GB', { hour: '2-digit', minute: '2-digit', hour12: false });
        if (timeFrom && hhmm < timeFrom) return false;
        if (timeTo && hhmm > timeTo) return false;
      }
      if (text) {
        const haystack = [
          data.vlm_summary || '',
          meta.vlm_summary || '',
          data.summary || '',
          data.event_brief || '',
          data.brief || '',
          meta.brief || '',
          (data.violation_labels || []).join(' '),
          (e.service_ids || []).map((s: string) => serviceName(s)).join(' '),
          e.agent_name || '',
        ].join(' ').toLowerCase();
        if (!haystack.includes(text)) return false;
      }
      return true;
    });
  });

  // Summary stats across ALL events (not filtered)
  const stats = createMemo(() => {
    const all = events();
    let high = 0, medium = 0, low = 0, withSnap = 0;
    for (const e of all) {
      const meta: any = e.metadata || {};
      const data: any = e.data || {};
      const s = normSeverity(data.severity || meta.severity);
      if (s === 'high') high++;
      else if (s === 'medium') medium++;
      else low++;
      if ((meta as any)?.snapshot_path || (data as any)?.snapshot_path || (e as MomentWithSnapshot).snapshot_url) withSnap++;
    }
    return { total: all.length, high, medium, low, withSnap };
  });

  const detections = (e: MomentWithSnapshot) => {
    const meta: any = e.metadata || {};
    const data: any = e.data || {};
    if (meta.bboxes && Array.isArray(meta.bboxes) && meta.bboxes.length > 0)
      return meta.bboxes.map((b: any) => ({ label: b.label, confidence: b.confidence }));
    if (data.detections && Array.isArray(data.detections))
      return data.detections.map((d: any) => ({ label: d.label, confidence: d.confidence }));
    return [];
  };

  const summary = (e: MomentWithSnapshot) => {
    const meta: any = e.metadata || {};
    const data: any = e.data || {};
    const brief = data.brief || data.event_brief || meta.brief || '';
    const aiSummary =
      data.ai_summary || data.summary || data.vlm_summary || meta.vlm_summary ||
      data.answer || data.result || data.message || brief;
    return { brief, aiSummary };
  };

  const hasFilters = () => filterServiceId() !== 'all' || filterAgentId() !== 'all' ||
    filterAgentType() !== 'all' || filterSeverity() !== 'all' || filterText() !== '' ||
    filterTimeFrom() !== '' || filterTimeTo() !== '';

  const activeFilterCount = createMemo(() => {
    let count = 0;
    if (filterServiceId() !== 'all') count++;
    if (filterAgentId() !== 'all') count++;
    if (filterAgentType() !== 'all') count++;
    if (filterSeverity() !== 'all') count++;
    if (filterText().trim() !== '') count++;
    if (filterTimeFrom() !== '') count++;
    if (filterTimeTo() !== '') count++;
    return count;
  });

  const clearFilters = () => {
    setFilterServiceId('all');
    setFilterAgentId('all');
    setFilterAgentType('all');
    setFilterSeverity('all');
    setFilterText('');
    setFilterTimeFrom('');
    setFilterTimeTo('');
  };

  return (
    <div class="h-full flex flex-col bg-neu-925/20 overflow-hidden">
      {/* ── Image viewer ───────────────────────────────────── */}
      <Show when={viewingImage()}>
        <ImageViewer src={viewingImage()!} onClose={() => setViewingImage(null)} />
      </Show>

      {/* ── Header ────────────────────────────────────────── */}
      <div class="flex-none border-b border-neu-800/60 bg-neu-900/55">
        <div class="px-5 pt-4 pb-4">
          <div class="flex flex-col xl:flex-row xl:items-start xl:justify-between gap-3">
            <div>
              <h2 class="text-lg font-semibold text-white tracking-tight flex items-center gap-2">
                <FiActivity size={18} class="text-violet-400" />
                Events Timeline
              </h2>
              <p class="text-xs text-neu-500 mt-1">Browse detections and AI outputs with focused, progressive filters.</p>
            </div>
            <div class="grid grid-cols-2 sm:grid-cols-4 gap-2 xl:min-w-[520px]">
              <div class="rounded-xl border border-neu-800 bg-neu-900/70 px-3 py-2">
                <div class="text-[10px] text-neu-500 uppercase tracking-[0.08em]">Total</div>
                <div class="text-sm font-semibold text-white mt-0.5">{stats().total}</div>
              </div>
              <div class="rounded-xl border border-red-500/25 bg-red-500/10 px-3 py-2">
                <div class="text-[10px] text-red-300 uppercase tracking-[0.08em]">High</div>
                <div class="text-sm font-semibold text-red-200 mt-0.5">{stats().high}</div>
              </div>
              <div class="rounded-xl border border-amber-500/25 bg-amber-500/10 px-3 py-2">
                <div class="text-[10px] text-amber-300 uppercase tracking-[0.08em]">Medium</div>
                <div class="text-sm font-semibold text-amber-200 mt-0.5">{stats().medium}</div>
              </div>
              <div class="rounded-xl border border-violet-500/25 bg-violet-500/10 px-3 py-2">
                <div class="text-[10px] text-violet-300 uppercase tracking-[0.08em]">Snapshots</div>
                <div class="text-sm font-semibold text-violet-200 mt-0.5">{stats().withSnap}</div>
              </div>
            </div>
          </div>

          <div class="mt-3 flex flex-col lg:flex-row lg:items-center gap-2">
            <div class="flex flex-wrap items-center gap-2">
              <div class="flex items-center gap-1.5 px-2.5 h-8 rounded-lg bg-neu-900 border border-neu-800">
                <FiCalendar size={13} class="text-neu-600" />
                <input
                  type="date"
                  class="bg-transparent text-xs text-neu-200 outline-none"
                  value={selectedDate()}
                  max={todayStr()}
                  onInput={(e) => {
                    const d = e.currentTarget.value;
                    setSelectedDate(d);
                    loadDateEvents(d);
                  }}
                />
              </div>
              {(['Today', 'Yesterday'] as const).map((label) => {
                const d = label === 'Today'
                  ? todayStr()
                  : localDateStr(new Date(Date.now() - 86400_000));
                return (
                  <button
                    class={`px-2.5 h-8 rounded-lg text-[11px] font-medium border transition-all ${
                      selectedDate() === d
                        ? 'bg-violet-600 text-white border-violet-600'
                        : 'bg-neu-900/80 text-neu-400 border-neu-800 hover:border-neu-600 hover:text-neu-200'
                    }`}
                    onClick={() => { setSelectedDate(d); loadDateEvents(d); }}
                  >
                    {label}
                  </button>
                );
              })}
              <Show when={dateLoading()}>
                <svg class="animate-spin w-3.5 h-3.5 text-violet-400" viewBox="0 0 24 24" fill="none">
                  <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                  <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z" />
                </svg>
              </Show>
              <Show when={isHistorical()}>
                <span class="text-[11px] text-amber-400">Historical mode</span>
              </Show>
            </div>

            <div class="flex-1 min-w-0 flex items-center gap-2">
              <div class="flex-1 min-w-0 flex items-center gap-1.5 px-2.5 h-8 rounded-lg bg-neu-900 border border-neu-800 focus-within:border-violet-500/50 transition-colors">
                <FiSearch size={12} class="text-neu-600 flex-shrink-0" />
                <input
                  type="text"
                  placeholder="Search summaries, labels, cameras, and agent names..."
                  class="bg-transparent text-xs text-neu-200 placeholder:text-neu-600 outline-none w-full"
                  value={filterText()}
                  onInput={(e) => setFilterText(e.currentTarget.value)}
                  onKeyDown={(e) => e.key === 'Enter' && runDbSearch()}
                />
                <Show when={filterText()}>
                  <button class="text-neu-600 hover:text-neu-400" onClick={() => setFilterText('')}>
                    <FiX size={10} />
                  </button>
                </Show>
              </div>
              <Show when={filterText()}>
                <button
                  class={`flex items-center gap-1.5 px-3 h-8 rounded-lg border text-[11px] whitespace-nowrap transition-colors ${
                    dbSearching()
                      ? 'bg-neu-800 text-neu-500 border-neu-700 cursor-not-allowed'
                      : 'bg-violet-600/20 text-violet-300 border-violet-500/30 hover:bg-violet-600/30'
                  }`}
                  onClick={runDbSearch}
                  disabled={dbSearching()}
                  title="Search historical database"
                >
                  <Show when={dbSearching()} fallback={<FiSearch size={11} />}>
                    <svg class="animate-spin w-2.5 h-2.5" viewBox="0 0 24 24" fill="none">
                      <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
                      <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z" />
                    </svg>
                  </Show>
                  {dbSearching() ? 'Searching...' : 'Search DB'}
                </button>
              </Show>
            </div>

            <div class="flex items-center gap-2">
              <button
                class={`flex items-center gap-1.5 h-8 px-3 rounded-lg border text-[11px] transition-colors ${
                  showAdvancedFilters()
                    ? 'bg-violet-600/20 border-violet-500/40 text-violet-200'
                    : 'bg-neu-900 border-neu-800 text-neu-400 hover:text-neu-200 hover:border-neu-600'
                }`}
                onClick={() => setShowAdvancedFilters((v) => !v)}
              >
                <FiFilter size={12} />
                {showAdvancedFilters() ? 'Hide Filters' : 'Filters'}
                <Show when={activeFilterCount() > 0}>
                  <span class="inline-flex items-center justify-center min-w-[18px] h-[18px] px-1 rounded-full bg-violet-500/30 text-violet-100 text-[10px] font-semibold">
                    {activeFilterCount()}
                  </span>
                </Show>
              </button>
              <Show when={hasFilters()}>
                <button
                  class="flex items-center gap-1 h-8 px-2.5 rounded-lg text-[11px] text-neu-400 border border-neu-750 hover:text-red-400 hover:border-red-500/30 transition-colors"
                  onClick={clearFilters}
                >
                  <FiX size={10} />
                  Clear
                </button>
              </Show>
            </div>
          </div>

          <Show when={showAdvancedFilters()}>
            <div class="mt-3 rounded-xl border border-neu-800/80 bg-neu-950/55 p-3">
              <div class="grid grid-cols-1 md:grid-cols-2 xl:grid-cols-5 gap-3">
                <div>
                  <label class="text-[11px] text-neu-500">Severity</label>
                  <div class="mt-1 flex gap-1.5 flex-wrap">
                    {(['all', 'high', 'medium', 'low'] as const).map((s) => (
                      <button
                        class={`px-2.5 h-7 rounded-lg text-[11px] font-medium border transition-all ${
                          filterSeverity() === s
                            ? s === 'all'
                              ? 'bg-violet-600 text-white border-violet-600'
                              : s === 'high'
                                ? 'bg-red-500/30 text-red-300 border-red-500/50'
                                : s === 'medium'
                                  ? 'bg-amber-500/30 text-amber-300 border-amber-500/50'
                                  : 'bg-green-500/30 text-green-300 border-green-500/50'
                            : 'bg-neu-900/80 text-neu-500 border-neu-800 hover:border-neu-600 hover:text-neu-300'
                        }`}
                        onClick={() => setFilterSeverity(s)}
                      >
                        {s === 'all' ? 'All' : s.charAt(0).toUpperCase() + s.slice(1)}
                      </button>
                    ))}
                  </div>
                </div>

                <div>
                  <label class="text-[11px] text-neu-500">Agent Type</label>
                  <select
                    class="select text-xs h-8 py-0 leading-8 mt-1 w-full"
                    value={filterAgentType()}
                    onChange={(e) => setFilterAgentType(e.currentTarget.value)}
                  >
                    <option value="all">All types</option>
                    <option value="ppe">{AGENT_TYPE_CONFIG.ppe.label}</option>
                    <option value="person">{AGENT_TYPE_CONFIG.person.label}</option>
                    <option value="situational">{AGENT_TYPE_CONFIG.situational.label}</option>
                    <option value="face">{AGENT_TYPE_CONFIG.face.label}</option>
                  </select>
                </div>

                <div>
                  <label class="text-[11px] text-neu-500">Camera</label>
                  <select
                    class="select text-xs h-8 py-0 leading-8 mt-1 w-full"
                    value={filterServiceId()}
                    onChange={(e) => setFilterServiceId(e.currentTarget.value)}
                  >
                    <option value="all">All cameras</option>
                    <For each={allServices()}>{(s) => <option value={s.id}>{s.name || s.id}</option>}</For>
                  </select>
                </div>

                <div>
                  <label class="text-[11px] text-neu-500">Agent</label>
                  <select
                    class="select text-xs h-8 py-0 leading-8 mt-1 w-full"
                    value={filterAgentId()}
                    onChange={(e) => setFilterAgentId(e.currentTarget.value)}
                  >
                    <option value="all">All agents</option>
                    <For each={agents()}>{(a) => <option value={a.id}>{a.name}</option>}</For>
                  </select>
                </div>

                <div class="md:col-span-2 xl:col-span-1">
                  <label class="text-[11px] text-neu-500">Time Range</label>
                  <div class="mt-1 flex items-center gap-1.5">
                    <input
                      type="time"
                      class="h-8 w-full px-2 rounded-lg bg-neu-900 border border-neu-800 text-[11px] text-neu-300 outline-none focus:border-violet-500/50 transition-colors"
                      value={filterTimeFrom()}
                      onInput={(e) => setFilterTimeFrom(e.currentTarget.value)}
                      title="From time"
                    />
                    <span class="text-[11px] text-neu-600">-</span>
                    <input
                      type="time"
                      class="h-8 w-full px-2 rounded-lg bg-neu-900 border border-neu-800 text-[11px] text-neu-300 outline-none focus:border-violet-500/50 transition-colors"
                      value={filterTimeTo()}
                      onInput={(e) => setFilterTimeTo(e.currentTarget.value)}
                      title="To time"
                    />
                  </div>
                </div>
              </div>

              <div class="mt-3 text-[11px] text-neu-500">
                {filteredEvents().length} event{filteredEvents().length !== 1 ? 's' : ''} shown
              </div>
            </div>
          </Show>
        </div>
      </div>

      {/* ── Feed ──────────────────────────────────────────── */}
      <div class="flex-1 overflow-y-auto">
        <Show when={filteredEvents().length === 0}>
          <div class="flex flex-col items-center justify-center h-64 gap-3 text-neu-600">
            <FiActivity size={40} class="opacity-20" />
            <div class="text-center">
              <div class="text-sm font-medium text-neu-500">No events yet</div>
              <div class="text-xs mt-1">Start nodes, connect workers, and assign agents to cameras.</div>
            </div>
          </div>
        </Show>

        <Show when={filteredEvents().length > 0}>
          <div class="p-4 md:p-6 max-w-6xl mx-auto w-full space-y-4">
            <For each={filteredEvents()}>
              {(e) => {
                const meta: any = e.metadata || {};
                const data: any = e.data || {};
                const agentType = normAgentType(meta.agent_type);
                const severity = normSeverity(data.severity || meta.severity);
                const sevCfg = SEVERITY_CONFIG[severity];
                const typeCfg = AGENT_TYPE_CONFIG[agentType];
                const dets = detections(e);
                const mainSummary = summary(e);
                const counts: Record<string, number> = {};
                for (const d of dets) counts[d.label || 'unknown'] = (counts[d.label || 'unknown'] || 0) + 1;
                const countEntries = Object.entries(counts)
                  .sort((a, b) => {
                    const av = isViolLabel(a[0]) ? 0 : 1;
                    const bv = isViolLabel(b[0]) ? 0 : 1;
                    return av !== bv ? av - bv : b[1] - a[1];
                  })
                  .slice(0, 5);

                const violationCount = Number(data.violation_count ?? meta.violation_count ?? 0);
                const personCount = Number(data.person_count ?? meta.person_count ?? 0);
                const primaryText = String(mainSummary.brief || mainSummary.aiSummary || 'Event detected');
                const secondaryText = String(mainSummary.aiSummary || '');
                const firstServiceId = (e.service_ids || [])[0];
                const resolvedServiceId = String(meta.service_id || data.service_id || firstServiceId || '').trim();
                const explicitCameraName = String(data.camera_name || meta.camera_name || '').trim();
                const cameraLabel = explicitCameraName || (resolvedServiceId ? serviceName(resolvedServiceId) : 'Unknown camera');
                const violationLabels = Array.isArray(data.violation_labels)
                  ? (data.violation_labels as string[]).slice(0, 3).map(humanizeLabel)
                  : [];
                const personNames: string[] = Array.isArray(meta.person_names)
                  ? (meta.person_names as string[]).filter((n) => typeof n === 'string' && n.trim()).slice(0, 4)
                  : [];

                return (
                  <article class="group bg-neu-900/85 border border-neu-800/75 hover:border-violet-600/45 rounded-2xl overflow-hidden transition-all duration-200 shadow-sm hover:shadow-md hover:shadow-violet-900/10">
                    <div class={`h-1 ${sevCfg.bar}`} />
                    <div class="p-4 md:p-5">
                      <div class="flex items-center gap-2 flex-wrap">
                        <span class={`inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-semibold border ${typeCfg.color}`}>
                          {typeCfg.label}
                        </span>
                        <Show when={severity !== 'unknown'}>
                          <span class={`inline-flex items-center px-2 py-0.5 rounded-full text-[10px] font-bold uppercase border ${sevCfg.badge}`}>
                            {severity}
                          </span>
                        </Show>
                        <span class="ml-auto flex items-center gap-1 text-[10px] text-neu-500">
                          <FiClock size={9} />
                          {relTime(e.created_at)}
                        </span>
                      </div>

                      <div class={`mt-3 grid gap-4 ${e.snapshot_url ? 'md:grid-cols-[220px_minmax(0,1fr)]' : 'grid-cols-1'}`}>
                        <Show when={e.snapshot_url}>
                          <button
                            class="relative group/thumb rounded-xl overflow-hidden border border-neu-750 hover:border-violet-500/60 transition-colors h-[132px] md:h-[148px]"
                            onClick={() => setViewingImage(e.snapshot_url!)}
                          >
                            <img
                              src={e.snapshot_url}
                              alt="Snapshot"
                              class="w-full h-full object-cover"
                              loading="lazy"
                              onError={(ev) => { (ev.currentTarget as HTMLImageElement).parentElement!.style.display = 'none'; }}
                            />
                            <div class="absolute inset-0 bg-black/0 group-hover/thumb:bg-black/35 transition-colors flex items-center justify-center">
                              <FiZoomIn size={18} class="text-white opacity-0 group-hover/thumb:opacity-100 transition-opacity" />
                            </div>
                          </button>
                        </Show>

                        <div class="min-w-0">
                          <p class="text-[15px] font-semibold text-neu-100 leading-relaxed">{primaryText}</p>
                          <Show when={secondaryText && secondaryText !== primaryText}>
                            <p class="mt-1.5 text-xs text-neu-400 leading-relaxed line-clamp-3">{secondaryText}</p>
                          </Show>
                          <div class="mt-3 flex flex-wrap items-center gap-1.5">
                            <button
                              class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] bg-neu-850 text-neu-300 border border-neu-700 hover:text-violet-300 hover:border-violet-500/40 transition-colors"
                              onClick={() => {
                                if (!resolvedServiceId) return;
                                const nid = nodeIdForService(resolvedServiceId);
                                if (nid) setTab({ type: 'view', serviceId: resolvedServiceId, nodeId: nid } as any);
                              }}
                            >
                              <FiCamera size={9} />
                              {cameraLabel}
                            </button>
                            <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] bg-neu-850 border border-neu-700 text-neu-300">
                              <FiCheckCircle size={9} />
                              {personCount} person{personCount !== 1 ? 's' : ''}
                            </span>
                            <Show when={violationCount > 0}>
                              <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] bg-red-900/20 border border-red-700/45 text-red-300">
                                <FiAlertTriangle size={9} />
                                {violationCount} violation{violationCount !== 1 ? 's' : ''}
                              </span>
                            </Show>
                            <For each={violationLabels.length > 0 ? violationLabels : countEntries.slice(0, 3).map(([label, count]) => `${humanizeLabel(label)}${count > 1 ? ` ×${count}` : ''}`)}>
                              {(label) => (
                                <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] border bg-red-900/15 text-red-300 border-red-700/35">
                                  {label}
                                </span>
                              )}
                            </For>
                            <For each={personNames}>
                              {(name) => (
                                <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] font-semibold bg-blue-900/20 border border-blue-700/35 text-blue-300">
                                  <FiUser size={9} />
                                  {name}
                                </span>
                              )}
                            </For>
                          </div>
                        </div>
                      </div>

                      <div class="mt-3 flex items-center justify-between text-[10px] text-neu-500 pt-2 border-t border-neu-800/60">
                        <span class="truncate">Agent: {e.agent_name}</span>
                        <span class="ml-3 shrink-0">{new Date(e.created_at).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })}</span>
                      </div>
                    </div>
                  </article>
                );
              }}
            </For>
          </div>
        </Show>
      </div>
    </div>
  );
};

export default TabMoments;
