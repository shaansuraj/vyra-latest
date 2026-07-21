import { Component, For, Show, createEffect, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import {
  addTimelineCaseItems,
  auditTimelineReveal,
  assignTimelineIdentity,
  backfillTimelineSightings,
  connectAgentEventsWebSocket,
  createTimelineCase,
  createTimelineEvidenceLock,
  exportTimelineCase,
  fetchPersons,
  fetchTimelineCases,
  fetchTimelineAppearanceSearch,
  fetchTimelineIdentities,
  fetchTimelineJourney,
  fetchTimelineMoments,
  fetchTimelineTopology,
  getToken,
  mergeTimelineIdentities,
  persons,
  queryAI,
  relay,
  reviewTimelineMoment,
  splitTimelineMoments,
  subscribeToAgentEvents,
  updateTimelineTopology,
  type AgentEvent,
  type PersonMoment,
  type TimelineCase,
  type TimelineAppearanceMatch,
  type TimelineExportManifest,
  type TimelineIdentity,
  type TimelineJourney,
  type TimelineMomentOpts,
  type TimelineTopologyNode,
} from '../../shared';
import {
  FiArchive,
  FiCamera,
  FiChevronDown,
  FiChevronUp,
  FiCheckCircle,
  FiClock,
  FiDownload,
  FiEye,
  FiEyeOff,
  FiFilter,
  FiGitMerge,
  FiLayers,
  FiLock,
  FiMapPin,
  FiRadio,
  FiRefreshCw,
  FiSearch,
  FiShield,
  FiTarget,
  FiUser,
  FiUserCheck,
  FiZap,
} from 'solid-icons/fi';
import { toaster } from '../../ark/ArkToast';

const localDateISO = () => {
  const now = new Date();
  const offsetMs = now.getTimezoneOffset() * 60 * 1000;
  return new Date(now.getTime() - offsetMs).toISOString().slice(0, 10);
};

const buildSnapshotUrl = (path: string) => {
  const clean = String(path || '').trim();
  if (!clean) return '';
  let base = '';
  if (/^https?:\/\//i.test(clean)) {
    base = clean;
  } else {
    const marker = '/storage/snapshots/';
    const markerPos = clean.indexOf(marker);
    if (markerPos >= 0) base = relay(clean.slice(markerPos));
    else if (clean.startsWith('/')) base = relay(clean);
    else base = relay(`/storage/snapshots/${clean.replace(/^\/+/, '')}`);
  }
  const token = getToken();
  if (!token) return base;
  const sep = base.includes('?') ? '&' : '?';
  return `${base}${sep}token=${encodeURIComponent(token)}`;
};

const unknownLabel = (globalPersonId: string) => {
  const id = String(globalPersonId || '').trim();
  const match = id.match(/unknown[:/#\s-]*(\d+)/i);
  if (match?.[1]) return `Unknown #${match[1]}`;
  return id || 'Unknown person';
};

const identityLabel = (item?: TimelineIdentity | PersonMoment | null) => {
  if (!item) return 'No identity selected';
  return item.person_name || unknownLabel(item.global_person_id);
};

const cameraLabel = (m?: PersonMoment | null) => m?.camera_name || m?.service_id || 'Unknown camera';

const formatClock = (raw?: string) => {
  if (!raw) return 'Not seen';
  const d = new Date(raw);
  if (Number.isNaN(d.getTime())) return raw;
  return d.toLocaleString();
};

const formatDuration = (seconds: number) => {
  if (!Number.isFinite(seconds) || seconds <= 0) return '0s';
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  const s = Math.floor(seconds % 60);
  if (h > 0) return `${h}h ${m}m`;
  if (m > 0) return `${m}m ${s}s`;
  return `${s}s`;
};

const confidenceLabel = (value?: number) => {
  if (!value || value <= 0) return 'n/a';
  return `${Math.round(value * 100)}%`;
};

const severityClass = (severity?: string) => {
  switch ((severity || '').toLowerCase()) {
    case 'high':
    case 'critical':
      return 'border-red-500/40 bg-red-500/12 text-red-200';
    case 'medium':
      return 'border-amber-500/40 bg-amber-500/12 text-amber-200';
    case 'low':
      return 'border-blue-500/35 bg-blue-500/10 text-blue-200';
    default:
      return 'border-neu-700 bg-neu-850 text-neu-400';
  }
};

const parseBBoxStyle = (raw?: string) => {
  if (!raw) return null;
  try {
    const data = JSON.parse(raw);
    const x = Number(data.x ?? data.left ?? data[0]);
    const y = Number(data.y ?? data.top ?? data[1]);
    const w = Number(data.w ?? data.width ?? data[2]);
    const h = Number(data.h ?? data.height ?? data[3]);
    if (![x, y, w, h].every(Number.isFinite) || w <= 0 || h <= 0) return null;
    const percent = [x, y, w, h].every((v) => v >= 0 && v <= 1);
    const left = percent ? x * 100 : Math.min(x, 100);
    const top = percent ? y * 100 : Math.min(y, 100);
    const width = percent ? w * 100 : Math.min(w, 100 - left);
    const height = percent ? h * 100 : Math.min(h, 100 - top);
    if (width <= 0 || height <= 0 || left > 100 || top > 100) return null;
    return {
      left: `${left}%`,
      top: `${top}%`,
      width: `${width}%`,
      height: `${height}%`,
    };
  } catch {
    return null;
  }
};

const eventIdentityIds = (event: AgentEvent): string[] => {
  const out = new Set<string>();
  const collect = (source?: Record<string, unknown>) => {
    if (!source) return;
    const direct = source.global_person_id;
    if (typeof direct === 'string' && direct.trim()) out.add(direct.trim());
    const ids = source.global_person_ids;
    if (Array.isArray(ids)) {
      ids.forEach((id) => {
        if (typeof id === 'string' && id.trim()) out.add(id.trim());
      });
    }
    const bboxes = source.bboxes;
    if (Array.isArray(bboxes)) {
      bboxes.forEach((box) => {
        if (box && typeof box === 'object') {
          const id = (box as Record<string, unknown>).global_person_id;
          const personId = (box as Record<string, unknown>).person_id;
          if (typeof id === 'string' && id.trim()) out.add(id.trim());
          if (typeof personId === 'string' && personId.trim()) out.add(`person:${personId.trim()}`);
        }
      });
    }
  };
  collect(event.metadata);
  collect(event.data);
  return Array.from(out);
};

const TabTimeline: Component = () => {
  const [date, setDate] = createSignal('');
  const [known, setKnown] = createSignal<'all' | 'known' | 'unknown'>('all');
  const [cameraId, setCameraId] = createSignal('');
  const [query, setQuery] = createSignal('');
  const [eventKind, setEventKind] = createSignal('');
  const [severity, setSeverity] = createSignal('');
  const [reviewStatus, setReviewStatus] = createSignal('');
  const [laneMode, setLaneMode] = createSignal<'camera' | 'identity' | 'event'>('camera');
  const [zoom, setZoom] = createSignal<'day' | 'hour' | 'minute'>('day');
  const [identities, setIdentities] = createSignal<TimelineIdentity[]>([]);
  const [moments, setMoments] = createSignal<PersonMoment[]>([]);
  const [journey, setJourney] = createSignal<TimelineJourney | null>(null);
  const [topology, setTopology] = createSignal<TimelineTopologyNode[]>([]);
  const [cases, setCases] = createSignal<TimelineCase[]>([]);
  const [selectedIdentity, setSelectedIdentity] = createSignal('');
  const [selectedMomentId, setSelectedMomentId] = createSignal('');
  const [selectedMomentIds, setSelectedMomentIds] = createSignal<Set<string>>(new Set());
  const [selectedCaseId, setSelectedCaseId] = createSignal('');
  const [caseTitle, setCaseTitle] = createSignal('');
  const [caseNote, setCaseNote] = createSignal('');
  const [assignPersonId, setAssignPersonId] = createSignal('');
  const [mergeTargetId, setMergeTargetId] = createSignal('');
  const [liveFollow, setLiveFollow] = createSignal(false);
  const [privacySafe, setPrivacySafe] = createSignal(true);
  const [revealed, setRevealed] = createSignal<Set<string>>(new Set());
  const [loading, setLoading] = createSignal(false);
  const [loadingMoments, setLoadingMoments] = createSignal(false);
  const [aiQuestion, setAiQuestion] = createSignal('Summarize this movement and cite evidence.');
  const [aiAnswer, setAiAnswer] = createSignal('');
  const [aiLoading, setAiLoading] = createSignal(false);
  const [exportManifest, setExportManifest] = createSignal<TimelineExportManifest | null>(null);
  const [appearanceMatches, setAppearanceMatches] = createSignal<TimelineAppearanceMatch[]>([]);
  const [appearanceLoading, setAppearanceLoading] = createSignal(false);
  const [backfillLoading, setBackfillLoading] = createSignal(false);
  const [advancedOpen, setAdvancedOpen] = createSignal(false);

  const filterOpts = (): TimelineMomentOpts => ({
    date: date().trim() || undefined,
    cameraId: cameraId().trim() || undefined,
    known: known(),
    eventKind: eventKind().trim() || undefined,
    severity: severity().trim() || undefined,
    reviewStatus: reviewStatus().trim() || undefined,
    query: query().trim() || undefined,
    limit: 250,
  });

  const selectedMoment = createMemo(() => moments().find((m) => m.id === selectedMomentId()) || moments()[0] || null);
  const selectedCase = createMemo(() => cases().find((c) => c.id === selectedCaseId()) || null);
  const hasSelection = createMemo(() => selectedMomentIds().size > 0);
  let identityRequestID = 0;
  let momentRequestID = 0;

  const loadCases = async () => {
    const list = await fetchTimelineCases();
    setCases(list);
    if (!selectedCaseId() && list[0]) setSelectedCaseId(list[0].id);
  };

  const loadTopology = async () => setTopology(await fetchTimelineTopology());

  const loadIdentities = async () => {
    const requestID = ++identityRequestID;
    setLoading(true);
    const list = await fetchTimelineIdentities({ ...filterOpts(), limit: 150 });
    if (requestID !== identityRequestID) return;
    setIdentities(list);
    const current = selectedIdentity();
    if (!current || !list.some((item) => item.global_person_id === current)) {
      setSelectedIdentity(list[0]?.global_person_id || '');
    }
    setLoading(false);
  };

  const loadMomentsAndJourney = async () => {
    const requestID = ++momentRequestID;
    const id = selectedIdentity();
    if (!id) {
      setMoments([]);
      setJourney(null);
      setLoadingMoments(false);
      return;
    }
    setLoadingMoments(true);
    const opts = { ...filterOpts(), globalPersonId: id, anchorMomentId: selectedMomentId() || undefined, limit: 300 };
    const [momentList, journeyPayload] = await Promise.all([
      fetchTimelineMoments(opts),
      fetchTimelineJourney(id, opts),
    ]);
    if (requestID !== momentRequestID) return;
    setMoments(momentList);
    setJourney(journeyPayload);
    if (!selectedMomentId() || !momentList.some((m) => m.id === selectedMomentId())) {
      setSelectedMomentId(momentList[0]?.id || '');
    }
    setLoadingMoments(false);
  };

  const refreshAll = async () => {
    await Promise.all([loadIdentities(), loadCases(), loadTopology()]);
    await loadMomentsAndJourney();
  };

  onMount(() => {
    void fetchPersons();
    void Promise.all([loadCases(), loadTopology()]);
    connectAgentEventsWebSocket();
    const unsubscribe = subscribeToAgentEvents((event) => {
      if (!liveFollow()) return;
      const ids = eventIdentityIds(event);
      if (ids.includes(selectedIdentity())) {
        void loadMomentsAndJourney();
      }
    });
    onCleanup(unsubscribe);
  });

  createEffect(() => {
    const key = `${date()}|${known()}|${cameraId()}|${query()}|${eventKind()}|${severity()}|${reviewStatus()}`;
    void key;
    const timer = window.setTimeout(() => { void loadIdentities(); }, 180);
    onCleanup(() => window.clearTimeout(timer));
  });

  createEffect(() => {
    const key = `${selectedIdentity()}|${date()}|${cameraId()}|${query()}|${eventKind()}|${severity()}|${reviewStatus()}|${selectedMomentId()}`;
    void key;
    const timer = window.setTimeout(() => { void loadMomentsAndJourney(); }, 120);
    onCleanup(() => window.clearTimeout(timer));
  });

  const lanes = createMemo(() => {
    const map = new Map<string, PersonMoment[]>();
    for (const moment of moments()) {
      const key = laneMode() === 'identity'
        ? identityLabel(moment)
        : laneMode() === 'event'
          ? (moment.event_kind || 'activity')
          : cameraLabel(moment);
      const lane = map.get(key) || [];
      lane.push(moment);
      map.set(key, lane);
    }
    return Array.from(map.entries()).map(([label, items]) => ({ label, items }));
  });

  const timeDomain = createMemo(() => {
    const times = moments().map((m) => new Date(m.created_at).getTime()).filter(Number.isFinite);
    if (times.length === 0) {
      const now = Date.now();
      return { start: now - 3600000, end: now };
    }
    let start = Math.min(...times);
    let end = Math.max(...times);
    const selected = selectedMoment();
    if (zoom() === 'day' && date()) {
      const d = new Date(date());
      start = d.getTime();
      end = start + 24 * 3600000;
    } else if (zoom() === 'hour' && selected) {
      const center = new Date(selected.created_at).getTime();
      start = center - 30 * 60000;
      end = center + 30 * 60000;
    } else if (zoom() === 'minute' && selected) {
      const center = new Date(selected.created_at).getTime();
      start = center - 5 * 60000;
      end = center + 5 * 60000;
    } else if (end === start) {
      start -= 60000;
      end += 60000;
    }
    return { start, end };
  });

  const momentLeft = (moment: PersonMoment) => {
    const t = new Date(moment.created_at).getTime();
    const domain = timeDomain();
    const span = Math.max(domain.end - domain.start, 1);
    return Math.max(0, Math.min(98, ((t - domain.start) / span) * 100));
  };

  const selectedSnapshotVisible = (moment: PersonMoment) => !privacySafe() || revealed().has(moment.id);

  const toggleMoment = (id: string) => {
    setSelectedMomentIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const revealMoment = async (id: string) => {
    const audited = await auditTimelineReveal(id);
    if (!audited) {
      toaster.error('Reveal audit failed');
      return;
    }
    setRevealed((prev) => {
      const next = new Set(prev);
      next.add(id);
      return next;
    });
    toaster.success('Evidence reveal audited');
  };

  const markReviewed = async (moment: PersonMoment, falseMatch = false) => {
    const ok = await reviewTimelineMoment(moment.id, {
      status: falseMatch ? 'false_match' : 'reviewed',
      note: falseMatch ? 'operator marked false match' : 'operator reviewed',
      is_false_match: falseMatch,
    });
    if (ok) {
      toaster.success(falseMatch ? 'Marked false match' : 'Moment reviewed');
      await loadMomentsAndJourney();
    } else {
      toaster.error('Review update failed');
    }
  };

  const assignIdentity = async () => {
    if (!selectedIdentity() || !assignPersonId()) return;
    const res = await assignTimelineIdentity(selectedIdentity(), assignPersonId());
    if (res) {
      toaster.success('Identity assigned');
      setSelectedIdentity(res.global_person_id);
      await refreshAll();
    } else {
      toaster.error('Assign failed');
    }
  };

  const mergeIdentity = async () => {
    if (!selectedIdentity() || !mergeTargetId().trim()) return;
    const ok = await mergeTimelineIdentities(selectedIdentity(), mergeTargetId().trim());
    if (ok) {
      toaster.success('Identity merged');
      setSelectedIdentity(mergeTargetId().trim());
      await refreshAll();
    } else {
      toaster.error('Merge failed');
    }
  };

  const splitSelected = async () => {
    const ids = Array.from(selectedMomentIds());
    if (!selectedIdentity() || ids.length === 0) return;
    const res = await splitTimelineMoments(selectedIdentity(), ids);
    if (res) {
      toaster.success('Moments split into new identity');
      setSelectedMomentIds(new Set());
      setSelectedIdentity(res.global_person_id);
      await refreshAll();
    } else {
      toaster.error('Split failed');
    }
  };

  const runAppearanceSearch = async () => {
    if (!selectedIdentity()) return;
    setAppearanceLoading(true);
    const matches = await fetchTimelineAppearanceSearch(selectedIdentity(), { kind: 'any', limit: 12 });
    setAppearanceMatches(matches);
    setAppearanceLoading(false);
    if (matches.length === 0) toaster.success('No local appearance matches found');
  };

  const createCase = async () => {
    const created = await createTimelineCase({
      title: caseTitle().trim() || `Timeline case ${new Date().toLocaleString()}`,
      notes: caseNote().trim(),
    });
    if (created) {
      toaster.success('Case created');
      setCaseTitle('');
      setCaseNote('');
      setSelectedCaseId(created.id);
      await loadCases();
    } else {
      toaster.error('Case creation failed');
    }
  };

  const pinToCase = async () => {
    const caseId = selectedCaseId();
    const ids = hasSelection() ? Array.from(selectedMomentIds()) : selectedMoment() ? [selectedMoment()!.id] : [];
    if (!caseId || ids.length === 0) return;
    const added = await addTimelineCaseItems(caseId, ids, caseNote().trim());
    if (added.length > 0) {
      toaster.success('Pinned to case');
      setSelectedMomentIds(new Set());
      await loadCases();
    } else {
      toaster.error('Nothing pinned');
    }
  };

  const lockEvidence = async () => {
    const ids = hasSelection() ? Array.from(selectedMomentIds()) : selectedMoment() ? [selectedMoment()!.id] : [];
    if (ids.length === 0) return;
    const ok = await createTimelineEvidenceLock({
      title: selectedCase()?.title ? `Lock: ${selectedCase()!.title}` : 'Timeline evidence lock',
      case_id: selectedCaseId() || undefined,
      moment_ids: ids,
    });
    if (ok) toaster.success('Evidence locked');
    else toaster.error('Evidence lock failed');
  };

  const exportCase = async () => {
    if (!selectedCaseId()) return;
    const manifest = await exportTimelineCase(selectedCaseId());
    if (manifest) {
      setExportManifest(manifest);
      toaster.success('Export manifest generated');
    } else {
      toaster.error('Case export failed');
    }
  };

  const seedTopology = async () => {
    const route = journey()?.route || [];
    if (route.length === 0) return;
    const nodes = route.map((node, index) => ({
      service_id: node.service_id,
      label: node.camera_name || node.service_id,
      zone: node.zone || 'site',
      x: route.length === 1 ? 0.5 : 0.12 + (index / Math.max(route.length - 1, 1)) * 0.76,
      y: 0.35 + ((index % 2) * 0.3),
      adjacent_service_ids: route[index + 1] ? [route[index + 1].service_id] : [],
    }));
    const updated = await updateTimelineTopology(nodes);
    setTopology(updated);
    await loadMomentsAndJourney();
    toaster.success('Topology updated from route');
  };

  const runBackfill = async () => {
    setBackfillLoading(true);
    const result = await backfillTimelineSightings();
    setBackfillLoading(false);
    if (!result) {
      toaster.error('Timeline import failed');
      return;
    }
    toaster.success(`Timeline import processed ${result.processed} event rows`);
    await refreshAll();
  };

  const runNarrator = async () => {
    if (!selectedIdentity()) return;
    setAiLoading(true);
    setAiAnswer('');
    const scopeDate = date() ? `on ${date()}` : 'across all visible dates';
    const prompt = `${aiQuestion().trim()}\n\nTimeline scope: identity ${selectedIdentity()} ${scopeDate}. Include first seen, last seen, camera path, gaps, nearby activity, and cite evidence timestamps.`;
    const res = await queryAI(prompt, date() || undefined);
    setAiAnswer(res.answer || 'No narrative generated.');
    setAiLoading(false);
  };

  const routePoints = createMemo(() => (journey()?.route || []).map((node) => `${node.x * 100},${node.y * 100}`).join(' '));

  return (
    <div class="h-full min-h-0 grid grid-cols-1 xl:grid-cols-[320px_minmax(0,1fr)_360px] gap-3 p-3 overflow-hidden">
      <section class="min-h-[320px] xl:min-h-0 rounded-lg border border-neu-800 bg-neu-925/80 overflow-hidden flex flex-col">
        <div class="h-12 px-3 border-b border-neu-800 flex items-center justify-between">
          <div class="flex items-center gap-2 text-neu-100 font-semibold text-sm">
            <FiSearch size={15} class="text-cyan-300" />
            Identity Search
          </div>
          <button class="h-8 w-8 rounded-lg border border-neu-700 text-neu-300 hover:text-white hover:bg-neu-800 flex items-center justify-center" onClick={refreshAll} title="Refresh timeline">
            <FiRefreshCw size={14} />
          </button>
        </div>

        <div class="p-3 space-y-2 border-b border-neu-800">
          <input class="input w-full h-10 text-sm" value={query()} onInput={(e) => setQuery(e.currentTarget.value)} placeholder="Search person, camera, event..." />
          <div class="grid grid-cols-3 gap-2">
            <button class={`h-9 rounded-md border text-xs ${!date() ? 'border-cyan-500/40 bg-cyan-500/10 text-cyan-100' : 'border-neu-700 text-neu-300 hover:bg-neu-800'}`} onClick={() => setDate('')}>
              All
            </button>
            <button class={`h-9 rounded-md border text-xs ${date() === localDateISO() ? 'border-cyan-500/40 bg-cyan-500/10 text-cyan-100' : 'border-neu-700 text-neu-300 hover:bg-neu-800'}`} onClick={() => setDate(localDateISO())}>
              Today
            </button>
            <button class="h-9 rounded-md border border-neu-700 text-xs text-neu-300 hover:bg-neu-800" onClick={runBackfill} disabled={backfillLoading()}>
              {backfillLoading() ? 'Importing' : 'Import'}
            </button>
          </div>
          <button class={`h-9 w-full rounded-md border text-xs flex items-center justify-center gap-2 ${advancedOpen() ? 'border-cyan-500/40 bg-cyan-500/10 text-cyan-100' : 'border-neu-700 text-neu-300 hover:bg-neu-800'}`} onClick={() => setAdvancedOpen(!advancedOpen())}>
            <FiFilter size={13} />
            Advanced
            {advancedOpen() ? <FiChevronUp size={13} /> : <FiChevronDown size={13} />}
          </button>
          <Show when={advancedOpen()}>
            <div class="space-y-2 pt-1">
              <div class="grid grid-cols-2 gap-2">
                <input class="input h-9 text-xs" type="date" value={date()} onInput={(e) => setDate(e.currentTarget.value)} />
                <select class="input h-9 text-xs" value={known()} onChange={(e) => setKnown(e.currentTarget.value as 'all' | 'known' | 'unknown')}>
                  <option value="all">Known + unknown</option>
                  <option value="known">Known only</option>
                  <option value="unknown">Unknown only</option>
                </select>
              </div>
              <div class="grid grid-cols-2 gap-2">
                <input class="input h-9 text-xs" value={cameraId()} onInput={(e) => setCameraId(e.currentTarget.value)} placeholder="Camera ID" />
                <input class="input h-9 text-xs" value={eventKind()} onInput={(e) => setEventKind(e.currentTarget.value)} placeholder="Event type" />
              </div>
              <div class="grid grid-cols-2 gap-2">
                <input class="input h-9 text-xs" value={severity()} onInput={(e) => setSeverity(e.currentTarget.value)} placeholder="Severity" />
                <select class="input h-9 text-xs" value={reviewStatus()} onChange={(e) => setReviewStatus(e.currentTarget.value)}>
                  <option value="">Any review</option>
                  <option value="unreviewed">Unreviewed</option>
                  <option value="reviewed">Reviewed</option>
                  <option value="needs_review">Needs review</option>
                  <option value="false_match">False match</option>
                </select>
              </div>
            </div>
          </Show>
        </div>

        <div class="flex-1 min-h-0 overflow-y-auto">
          <Show when={!loading()} fallback={<div class="p-4 text-sm text-neu-500">Loading identities...</div>}>
            <For each={identities()} fallback={
              <div class="p-4 space-y-3 text-sm text-neu-500">
                <div>No identities match these filters.</div>
                <button class="h-9 w-full rounded-md border border-cyan-500/40 bg-cyan-500/10 text-xs font-semibold text-cyan-100 hover:bg-cyan-500/15" onClick={runBackfill} disabled={backfillLoading()}>
                  {backfillLoading() ? 'Importing existing events...' : 'Import existing identity events'}
                </button>
                <div class="text-[11px] leading-relaxed text-neu-600">
                  Use this if Identity has sightings but Timeline is empty after an upgrade.
                </div>
              </div>
            }>
              {(item) => {
                const selected = () => selectedIdentity() === item.global_person_id;
                return (
                  <button
                    class={`w-full px-3 py-2.5 border-b border-neu-850 text-left hover:bg-neu-850/80 ${selected() ? 'bg-cyan-500/10' : ''}`}
                    onClick={() => { setSelectedIdentity(item.global_person_id); setSelectedMomentId(''); }}
                  >
                    <div class="flex gap-2">
                      <div class="w-12 h-12 rounded-md overflow-hidden bg-neu-900 border border-neu-800 flex items-center justify-center shrink-0">
                        <Show when={item.snapshot_path} fallback={<FiUser size={18} class="text-neu-600" />}>
                          <img src={buildSnapshotUrl(item.snapshot_path!)} class="w-full h-full object-cover" alt="identity snapshot" />
                        </Show>
                      </div>
                      <div class="min-w-0 flex-1">
                        <div class="text-sm font-semibold text-neu-100 truncate">{identityLabel(item)}</div>
                        <div class="text-[11px] text-neu-500 truncate">{item.last_camera_name || item.last_service_id || 'Unknown camera'}</div>
                        <div class="mt-1 flex items-center gap-2 text-[10px] text-neu-500">
                          <span>{item.moment_count} moments</span>
                          <span>{confidenceLabel(item.identity_confidence)}</span>
                        </div>
                      </div>
                    </div>
                  </button>
                );
              }}
            </For>
          </Show>
        </div>

        <Show when={advancedOpen()}>
          <div class="border-t border-neu-800 p-3 space-y-2">
            <div class="flex items-center justify-between">
              <div class="text-xs font-semibold text-neu-300 flex items-center gap-1.5">
                <FiArchive size={13} class="text-cyan-300" />
                Case Workspace
              </div>
              <button class="h-7 px-2 rounded-md border border-neu-700 text-[11px] text-neu-300 hover:bg-neu-800" onClick={createCase}>New</button>
            </div>
            <input class="input h-8 text-xs w-full" value={caseTitle()} onInput={(e) => setCaseTitle(e.currentTarget.value)} placeholder="Case title" />
            <textarea class="input min-h-[58px] text-xs w-full resize-none" value={caseNote()} onInput={(e) => setCaseNote(e.currentTarget.value)} placeholder="Notes for pins or case export" />
            <select class="input h-8 text-xs w-full" value={selectedCaseId()} onChange={(e) => setSelectedCaseId(e.currentTarget.value)}>
              <option value="">No active case</option>
              <For each={cases()}>
                {(c) => <option value={c.id}>{c.title} ({c.item_count})</option>}
              </For>
            </select>
            <div class="grid grid-cols-3 gap-2">
              <button class="h-8 rounded-md border border-neu-700 text-[11px] text-neu-300 hover:bg-neu-800" onClick={pinToCase}>Pin</button>
              <button class="h-8 rounded-md border border-neu-700 text-[11px] text-neu-300 hover:bg-neu-800" onClick={lockEvidence}><FiLock size={11} class="inline mr-1" />Lock</button>
              <button class="h-8 rounded-md border border-neu-700 text-[11px] text-neu-300 hover:bg-neu-800" onClick={exportCase}>Export</button>
            </div>
          </div>
        </Show>
      </section>

      <section class="min-h-[560px] xl:min-h-0 rounded-lg border border-neu-800 bg-neu-925/70 overflow-hidden flex flex-col">
        <div class="h-12 px-3 border-b border-neu-800 flex flex-wrap items-center justify-between gap-2">
          <div class="min-w-0">
            <div class="text-sm font-semibold text-neu-100 truncate">{identityLabel(journey()?.identity)}</div>
            <div class="text-[11px] text-neu-500 truncate">First seen {formatClock(journey()?.stats.first_seen_at)} · Last seen {formatClock(journey()?.stats.last_seen_at)}</div>
          </div>
          <div class="flex items-center gap-1">
            <button class={`h-8 px-2 rounded-md border text-[11px] ${liveFollow() ? 'border-green-500/40 bg-green-500/10 text-green-200' : 'border-neu-700 text-neu-300 hover:bg-neu-800'}`} onClick={() => setLiveFollow(!liveFollow())}>
              <FiRadio size={12} class="inline mr-1" />Live
            </button>
            <button class={`h-8 px-2 rounded-md border text-[11px] ${privacySafe() ? 'border-cyan-500/40 bg-cyan-500/10 text-cyan-100' : 'border-neu-700 text-neu-300 hover:bg-neu-800'}`} onClick={() => setPrivacySafe(!privacySafe())}>
              <FiShield size={12} class="inline mr-1" />Privacy
            </button>
          </div>
        </div>

        <div class="grid grid-cols-2 lg:grid-cols-5 gap-2 p-3 border-b border-neu-800">
          <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-2">
            <div class="text-[10px] uppercase text-neu-600">Moments</div>
            <div class="text-lg font-semibold text-neu-100">{journey()?.stats.moment_count || 0}</div>
          </div>
          <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-2">
            <div class="text-[10px] uppercase text-neu-600">Cameras</div>
            <div class="text-lg font-semibold text-neu-100">{journey()?.stats.camera_count || 0}</div>
          </div>
          <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-2">
            <div class="text-[10px] uppercase text-neu-600">Dwell</div>
            <div class="text-lg font-semibold text-neu-100">{formatDuration(journey()?.stats.dwell_seconds || 0)}</div>
          </div>
          <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-2">
            <div class="text-[10px] uppercase text-neu-600">Confidence</div>
            <div class="text-lg font-semibold text-neu-100">{confidenceLabel(journey()?.stats.average_confidence)}</div>
          </div>
          <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-2">
            <div class="text-[10px] uppercase text-neu-600">Gaps</div>
            <div class="text-lg font-semibold text-neu-100">{journey()?.stats.gaps_over_120_sec || 0}</div>
          </div>
        </div>

        <div class="h-[210px] border-b border-neu-800 relative bg-neu-950 overflow-hidden">
          <div class="absolute inset-0 opacity-30" style={{ 'background-image': 'linear-gradient(rgba(148,163,184,0.12) 1px, transparent 1px), linear-gradient(90deg, rgba(148,163,184,0.12) 1px, transparent 1px)', 'background-size': '36px 36px' }} />
          <svg class="absolute inset-0 w-full h-full" viewBox="0 0 100 100" preserveAspectRatio="none">
            <Show when={routePoints()}>
              <polyline points={routePoints()} fill="none" stroke="rgba(34,211,238,0.62)" stroke-width="0.9" stroke-linecap="round" stroke-linejoin="round" />
            </Show>
          </svg>
          <For each={journey()?.route || []}>
            {(node, index) => (
              <button
                class="absolute -translate-x-1/2 -translate-y-1/2 min-w-[96px] max-w-[132px] rounded-lg border border-cyan-500/35 bg-neu-900/90 px-2 py-1.5 text-left shadow-lg"
                style={{ left: `${node.x * 100}%`, top: `${node.y * 100}%` }}
                title={node.service_id}
              >
                <div class="flex items-center gap-1 text-[11px] font-semibold text-cyan-100 truncate">
                  <FiMapPin size={11} />
                  <span class="truncate">{node.camera_name || node.service_id}</span>
                </div>
                <div class="text-[10px] text-neu-500">{node.count} sightings · #{index() + 1}</div>
              </button>
            )}
          </For>
          <Show when={advancedOpen()}>
            <button class="absolute right-3 bottom-3 h-8 px-3 rounded-md border border-neu-700 bg-neu-900/90 text-[11px] text-neu-300 hover:bg-neu-800" onClick={seedTopology}>
              <FiLayers size={12} class="inline mr-1" />Seed topology ({topology().length})
            </button>
          </Show>
        </div>

        <Show when={advancedOpen()}>
          <div class="h-12 px-3 border-b border-neu-800 flex flex-wrap items-center justify-between gap-2">
            <div class="flex items-center gap-1">
              <For each={['camera', 'identity', 'event'] as const}>
                {(mode) => (
                  <button class={`h-8 px-3 rounded-md border text-xs ${laneMode() === mode ? 'border-cyan-500/40 bg-cyan-500/10 text-cyan-100' : 'border-neu-700 text-neu-400 hover:bg-neu-850'}`} onClick={() => setLaneMode(mode)}>
                    {mode}
                  </button>
                )}
              </For>
            </div>
            <div class="flex items-center gap-1">
              <For each={['day', 'hour', 'minute'] as const}>
                {(mode) => (
                  <button class={`h-8 px-3 rounded-md border text-xs ${zoom() === mode ? 'border-cyan-500/40 bg-cyan-500/10 text-cyan-100' : 'border-neu-700 text-neu-400 hover:bg-neu-850'}`} onClick={() => setZoom(mode)}>
                    {mode}
                  </button>
                )}
              </For>
            </div>
          </div>
        </Show>

        <div class="flex-1 min-h-0 overflow-auto p-3">
          <Show when={!loadingMoments()} fallback={<div class="text-sm text-neu-500">Loading journey...</div>}>
            <For each={lanes()} fallback={<div class="text-sm text-neu-500">No moments for this identity and filter set.</div>}>
              {(lane) => (
                <div class="mb-3 min-w-[720px]">
                  <div class="h-6 flex items-center justify-between text-[11px] text-neu-500">
                    <span class="font-semibold text-neu-300 truncate">{lane.label}</span>
                    <span>{lane.items.length} moments</span>
                  </div>
                  <div class="relative h-[92px] rounded-lg border border-neu-800 bg-neu-950">
                    <div class="absolute left-0 right-0 top-1/2 h-px bg-neu-800" />
                    <For each={lane.items}>
                      {(moment) => {
                        const selected = () => selectedMomentId() === moment.id;
                        return (
                          <button
                            class={`absolute top-3 w-[74px] h-[66px] -translate-x-1/2 rounded-md border overflow-hidden bg-neu-900 shadow ${selected() ? 'border-cyan-400 ring-1 ring-cyan-400/50' : 'border-neu-700 hover:border-neu-500'}`}
                            style={{ left: `${momentLeft(moment)}%` }}
                            onClick={() => setSelectedMomentId(moment.id)}
                            title={`${cameraLabel(moment)} · ${formatClock(moment.created_at)}`}
                          >
                            <Show when={moment.snapshot_path} fallback={<div class="w-full h-full flex items-center justify-center text-neu-600"><FiCamera size={18} /></div>}>
                              <img src={buildSnapshotUrl(moment.snapshot_path!)} class={`w-full h-full object-cover ${privacySafe() && !revealed().has(moment.id) ? 'blur-sm scale-105' : ''}`} alt="moment snapshot" />
                            </Show>
                            <div class="absolute left-1 bottom-1 right-1 h-4 rounded bg-black/65 text-[9px] text-white truncate px-1">
                              {new Date(moment.created_at).toLocaleTimeString()}
                            </div>
                          </button>
                        );
                      }}
                    </For>
                  </div>
                </div>
              )}
            </For>
          </Show>
        </div>
      </section>

      <aside class="min-h-[420px] xl:min-h-0 rounded-lg border border-neu-800 bg-neu-925/80 overflow-hidden flex flex-col">
        <div class="h-12 px-3 border-b border-neu-800 flex items-center justify-between">
          <div class="flex items-center gap-2 text-sm font-semibold text-neu-100">
            <FiTarget size={15} class="text-cyan-300" />
            Evidence
          </div>
          <Show when={selectedMoment()}>
            {(moment) => (
              <label class="flex items-center gap-2 text-[11px] text-neu-400">
                <input type="checkbox" checked={selectedMomentIds().has(moment().id)} onChange={() => toggleMoment(moment().id)} />
                Select
              </label>
            )}
          </Show>
        </div>

        <div class="flex-1 min-h-0 overflow-y-auto">
          <Show when={selectedMoment()} fallback={<div class="p-4 text-sm text-neu-500">Select a moment to inspect evidence.</div>}>
            {(moment) => {
              const bbox = () => parseBBoxStyle(moment().bbox_json);
              return (
                <div class="p-3 space-y-3">
                  <div class="relative h-[220px] rounded-lg overflow-hidden border border-neu-800 bg-neu-950 flex items-center justify-center">
                    <Show when={moment().snapshot_path} fallback={<FiCamera size={28} class="text-neu-700" />}>
                      <img src={buildSnapshotUrl(moment().snapshot_path!)} class={`w-full h-full object-cover ${selectedSnapshotVisible(moment()) ? '' : 'blur-md scale-105'}`} alt="evidence snapshot" />
                      <Show when={bbox()}>
                        {(style) => <div class="absolute border-2 border-cyan-300 shadow-[0_0_18px_rgba(34,211,238,0.65)]" style={style()} />}
                      </Show>
                    </Show>
                    <Show when={!selectedSnapshotVisible(moment())}>
                      <button class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/20 text-white" onClick={() => void revealMoment(moment().id)}>
                        <FiEye size={20} />
                        <span class="text-xs font-semibold">Reveal audited evidence</span>
                      </button>
                    </Show>
                  </div>

                  <div class="space-y-2">
                    <div class="flex items-center justify-between gap-2">
                      <div class="text-sm font-semibold text-neu-100 truncate">{identityLabel(moment())}</div>
                      <span class={`px-2 py-1 rounded-md border text-[10px] ${severityClass(moment().severity)}`}>{moment().severity || 'none'}</span>
                    </div>
                    <div class="text-xs text-neu-400">{moment().activity_brief || moment().event_kind || 'Activity detected'}</div>
                    <div class="grid grid-cols-2 gap-2 text-[11px] text-neu-500">
                      <div><FiClock size={11} class="inline mr-1" />{formatClock(moment().created_at)}</div>
                      <div><FiCamera size={11} class="inline mr-1" />{cameraLabel(moment())}</div>
                      <div>Confidence {confidenceLabel(moment().identity_confidence)}</div>
                      <div>Review {moment().review_status || 'unreviewed'}</div>
                    </div>
                  </div>

                  <div class="grid grid-cols-2 gap-2">
                    <button class="h-9 rounded-md border border-green-600/40 text-green-200 bg-green-500/10 hover:bg-green-500/15 text-xs" onClick={() => markReviewed(moment(), false)}>
                      <FiCheckCircle size={13} class="inline mr-1" />Reviewed
                    </button>
                    <button class="h-9 rounded-md border border-red-600/40 text-red-200 bg-red-500/10 hover:bg-red-500/15 text-xs" onClick={() => markReviewed(moment(), true)}>
                      <FiEyeOff size={13} class="inline mr-1" />False match
                    </button>
                  </div>

                  <Show when={advancedOpen()}>
                    <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-3 space-y-2">
                      <div class="text-xs font-semibold text-neu-200 flex items-center gap-1.5"><FiUserCheck size={13} class="text-cyan-300" />Identity Correction</div>
                      <select class="input h-8 text-xs w-full" value={assignPersonId()} onChange={(e) => setAssignPersonId(e.currentTarget.value)}>
                        <option value="">Assign registered person...</option>
                        <For each={persons()}>
                          {(p) => <option value={p.id}>{p.name}</option>}
                        </For>
                      </select>
                      <button class="h-8 w-full rounded-md border border-neu-700 text-xs text-neu-300 hover:bg-neu-800" onClick={assignIdentity}>Assign identity</button>
                      <div class="flex gap-2">
                        <input class="input h-8 text-xs flex-1" value={mergeTargetId()} onInput={(e) => setMergeTargetId(e.currentTarget.value)} placeholder="Target global ID" />
                        <button class="h-8 px-2 rounded-md border border-neu-700 text-neu-300 hover:bg-neu-800" title="Merge identity" onClick={mergeIdentity}>
                          <FiGitMerge size={13} />
                        </button>
                      </div>
                      <button class="h-8 w-full rounded-md border border-neu-700 text-xs text-neu-300 hover:bg-neu-800" disabled={!hasSelection()} onClick={splitSelected}>Split selected moments</button>
                    </div>

                    <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-3 space-y-2">
                      <div class="flex items-center justify-between gap-2">
                        <div class="text-xs font-semibold text-neu-200 flex items-center gap-1.5"><FiEye size={13} class="text-cyan-300" />Appearance Search</div>
                        <button class="h-7 px-2 rounded-md border border-neu-700 text-[11px] text-neu-300 hover:bg-neu-800" onClick={runAppearanceSearch} disabled={appearanceLoading()}>
                          {appearanceLoading() ? 'Searching' : 'Find similar'}
                        </button>
                      </div>
                      <For each={appearanceMatches()} fallback={<div class="text-xs text-neu-500">Uses local face/body ReID prototypes when available.</div>}>
                        {(match) => (
                          <button class="w-full flex items-center gap-2 py-1.5 border-b border-neu-800 last:border-0 text-left" onClick={() => { setSelectedIdentity(match.global_person_id); setSelectedMomentId(''); }}>
                            <div class="w-9 h-9 rounded-md overflow-hidden bg-neu-950 border border-neu-800 flex items-center justify-center shrink-0">
                              <Show when={match.snapshot_path} fallback={<FiUser size={14} class="text-neu-600" />}>
                                <img src={buildSnapshotUrl(match.snapshot_path!)} class="w-full h-full object-cover" alt="appearance match" />
                              </Show>
                            </div>
                            <div class="min-w-0 flex-1">
                              <div class="text-xs text-neu-200 truncate">{match.person_name || unknownLabel(match.global_person_id)}</div>
                              <div class="text-[10px] text-neu-500 truncate">{match.match_kind} · {Math.round(match.score * 100)}% · {match.last_camera_name || match.last_service_id || 'camera unknown'}</div>
                            </div>
                          </button>
                        )}
                      </For>
                    </div>

                    <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-3 space-y-2">
                      <div class="text-xs font-semibold text-neu-200 flex items-center gap-1.5"><FiZap size={13} class="text-cyan-300" />AI Incident Narrator</div>
                      <textarea class="input min-h-[70px] text-xs w-full resize-none" value={aiQuestion()} onInput={(e) => setAiQuestion(e.currentTarget.value)} />
                      <button class="h-8 w-full rounded-md border border-cyan-500/40 bg-cyan-500/10 text-cyan-100 text-xs hover:bg-cyan-500/15" onClick={runNarrator} disabled={aiLoading()}>
                        {aiLoading() ? 'Narrating...' : 'Narrate journey'}
                      </button>
                      <Show when={aiAnswer()}>
                        <div class="max-h-[160px] overflow-y-auto text-xs leading-relaxed text-neu-300 border border-neu-800 rounded-md p-2 bg-neu-950 whitespace-pre-wrap">{aiAnswer()}</div>
                      </Show>
                    </div>

                    <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-3">
                      <div class="text-xs font-semibold text-neu-200 mb-2">Nearby Activity</div>
                      <For each={journey()?.nearby_activity || []}>
                        {(nearby) => (
                          <div class="flex items-center justify-between py-1.5 border-b border-neu-800 last:border-0 text-xs">
                            <span class="text-neu-400">{nearby.window_minutes} min window</span>
                            <span class="text-neu-100">{nearby.count} events</span>
                          </div>
                        )}
                      </For>
                    </div>

                    <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-3">
                      <div class="text-xs font-semibold text-neu-200 mb-2">Predictive Handoff</div>
                      <For each={journey()?.predicted_next || []} fallback={<div class="text-xs text-neu-500">No prediction yet.</div>}>
                        {(pred) => (
                          <div class="py-1.5 border-b border-neu-800 last:border-0">
                            <div class="flex justify-between gap-2 text-xs">
                              <span class="text-neu-300 truncate">{pred.camera_name || pred.service_id}</span>
                              <span class="text-cyan-200">{Math.round(pred.probability * 100)}%</span>
                            </div>
                            <div class="text-[10px] text-neu-500">{pred.reason}</div>
                          </div>
                        )}
                      </For>
                    </div>

                    <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-3">
                      <div class="text-xs font-semibold text-neu-200 mb-2">Proximity Graph</div>
                      <For each={journey()?.proximity || []} fallback={<div class="text-xs text-neu-500">No nearby identities detected.</div>}>
                        {(edge) => (
                          <div class="py-1.5 border-b border-neu-800 last:border-0 text-xs">
                            <div class="flex justify-between gap-2">
                              <span class="text-neu-300 truncate">{edge.person_name || unknownLabel(edge.global_person_id)}</span>
                              <span class="text-neu-500">{edge.overlap_count}x</span>
                            </div>
                            <div class="text-[10px] text-neu-500 truncate">{edge.camera_name || edge.service_id}</div>
                          </div>
                        )}
                      </For>
                    </div>

                    <Show when={(journey()?.ghost_trail || []).length > 0}>
                      <div class="rounded-lg border border-amber-500/30 bg-amber-500/10 p-3">
                        <div class="text-xs font-semibold text-amber-200 mb-2">Ghost Trail Inference</div>
                        <For each={journey()?.ghost_trail || []}>
                          {(step) => (
                            <div class="text-xs text-amber-100 py-1">
                              {step.from_service_id} {'->'} {step.to_service_id}
                              <div class="text-[10px] text-amber-200/70">{step.reason}</div>
                            </div>
                          )}
                        </For>
                      </div>
                    </Show>

                    <Show when={exportManifest()}>
                      {(manifest) => (
                        <div class="rounded-lg border border-neu-800 bg-neu-900/70 p-3">
                          <div class="text-xs font-semibold text-neu-200 flex items-center gap-1.5 mb-2"><FiDownload size={13} class="text-cyan-300" />Latest Export</div>
                          <div class="text-xs text-neu-400">{manifest().moments.length} moments · {manifest().snapshot_hashes.length} hashes</div>
                          <For each={manifest().snapshot_hashes.slice(0, 4)}>
                            {(hash) => <div class="mt-1 text-[10px] font-mono text-neu-500 truncate">{hash.sha256 || hash.error}</div>}
                          </For>
                        </div>
                      )}
                    </Show>
                  </Show>
                </div>
              );
            }}
          </Show>
        </div>
      </aside>
    </div>
  );
};

export default TabTimeline;
