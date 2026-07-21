import { Component, For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import {
  FiCamera,
  FiClock,
  FiCpu,
  FiFilter,
  FiMessageSquare,
  FiRefreshCw,
  FiSearch,
  FiUser,
  FiZoomIn,
} from 'solid-icons/fi';
import {
  agents,
  cameras,
  fetchAgents,
  fetchCameras,
  nodes,
  fetchNodes,
  searchAgentEventsRest,
  queryAI,
  fetchQueryChat,
  deleteQueryChat,
  startLLMWorker,
  llmWorkerStatus,
  fetchLLMWorkerStatus,
  setTab,
  relay,
  getToken,
  type AgentEvent,
  type AIQueryEvent,
  type AIQuerySource,
  type AIQueryTimelineEvidence,
  type AIChatMessage,
} from '../../shared';

type TabSearchMode = 'search' | 'ai';

interface TabSearchProps {
  forcedMode?: TabSearchMode;
}

interface AskAITurn {
  id: string;
  question: string;
  answer: string;
  confidence: string;
  intent?: string;
  dateScope: string;
  relevantEvents: AIQueryEvent[];
  timelineEvidence: AIQueryTimelineEvidence[];
  sources: AIQuerySource[];
  traceId?: string;
  createdAt: string;
}

const mkTurnId = () => `askai-turn-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`;
const ASK_AI_CHAT_ID_KEY = 'vyra.askai.chatId';

const buildChatHistoryFromTurns = (turns: AskAITurn[]): AIChatMessage[] =>
  turns.flatMap((t) => ([
    { role: 'user', content: t.question },
    { role: 'assistant', content: t.answer },
  ] as AIChatMessage[]));

const turnsFromChatMessages = (messages: AIChatMessage[], dateScope: string): AskAITurn[] => {
  const turns: AskAITurn[] = [];
  let pendingQuestion = '';
  for (const message of messages || []) {
    if (message.role === 'user') {
      pendingQuestion = message.content || '';
      continue;
    }
    if (message.role !== 'assistant' || !pendingQuestion) continue;
    turns.push({
      id: mkTurnId(),
      question: pendingQuestion,
      answer: message.content || '',
      confidence: 'medium',
      dateScope,
      relevantEvents: [],
      timelineEvidence: [],
      sources: [],
      createdAt: new Date().toISOString(),
    });
    pendingQuestion = '';
  }
  return turns;
};

const ImageViewer: Component<{ src: string; onClose: () => void }> = (props) => {
  const [scale, setScale] = createSignal(1);
  const [dragging, setDragging] = createSignal(false);
  const [pos, setPos] = createSignal({ x: 0, y: 0 });
  const [dragStart, setDragStart] = createSignal({ x: 0, y: 0 });

  const handleWheel = (e: WheelEvent) => {
    e.preventDefault();
    const delta = e.deltaY > 0 ? -0.15 : 0.15;
    setScale((s) => Math.max(0.5, Math.min(5, s + delta)));
  };

  const handleMouseDown = (e: MouseEvent) => {
    if (scale() <= 1) return;
    e.preventDefault();
    setDragging(true);
    setDragStart({ x: e.clientX - pos().x, y: e.clientY - pos().y });
  };

  const handleMouseMove = (e: MouseEvent) => {
    if (!dragging()) return;
    setPos({ x: e.clientX - dragStart().x, y: e.clientY - dragStart().y });
  };

  const handleMouseUp = () => setDragging(false);

  return (
    <div
      class="fixed inset-0 z-[100] bg-black/90 backdrop-blur-sm flex items-center justify-center"
      onClick={props.onClose}
      onWheel={handleWheel}
    >
      <div class="absolute top-4 right-4 flex gap-2 z-10">
        <button
          class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20"
          onClick={(e) => { e.stopPropagation(); setScale((s) => Math.min(5, s + 0.5)); }}
        >
          + Zoom In
        </button>
        <button
          class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20"
          onClick={(e) => { e.stopPropagation(); setScale((s) => Math.max(0.5, s - 0.5)); }}
        >
          - Zoom Out
        </button>
        <button
          class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20"
          onClick={(e) => { e.stopPropagation(); setScale(1); setPos({ x: 0, y: 0 }); }}
        >
          Reset
        </button>
        <button
          class="px-3 py-1.5 rounded-lg bg-red-500/80 text-white text-sm hover:bg-red-500"
          onClick={props.onClose}
        >
          Close
        </button>
      </div>
      <div class="absolute bottom-4 left-1/2 -translate-x-1/2 text-white/50 text-xs">
        Scroll to zoom. Drag to pan when zoomed. Click outside to close.
      </div>
      <img
        src={props.src}
        alt="Snapshot"
        class="max-w-[90vw] max-h-[85vh] object-contain select-none rounded-xl"
        style={{
          transform: `scale(${scale()}) translate(${pos().x / scale()}px, ${pos().y / scale()}px)`,
          cursor: scale() > 1 ? (dragging() ? 'grabbing' : 'grab') : 'zoom-in',
          transition: dragging() ? 'none' : 'transform 0.15s ease-out',
        }}
        onClick={(e) => e.stopPropagation()}
        onMouseDown={handleMouseDown}
        onMouseMove={handleMouseMove}
        onMouseUp={handleMouseUp}
        onMouseLeave={handleMouseUp}
        draggable={false}
      />
    </div>
  );
};

const ConfidenceBadge: Component<{ confidence: string }> = (props) => {
  const cls = () => {
    switch (props.confidence) {
      case 'high':
        return 'bg-green-500/20 text-green-300 border-green-500/35';
      case 'medium':
        return 'bg-amber-500/20 text-amber-300 border-amber-500/35';
      default:
        return 'bg-neu-850 text-neu-400 border-neu-700';
    }
  };

  return (
    <span class={`px-2.5 py-1 rounded-full text-[10px] font-semibold uppercase border ${cls()}`}>
      {props.confidence}
    </span>
  );
};

const TabSearch: Component<TabSearchProps> = (props) => {
  const [mode, setMode] = createSignal<TabSearchMode>(props.forcedMode || 'search');
  const activeMode = createMemo<TabSearchMode>(() => props.forcedMode || mode());
  const modeLocked = createMemo(() => !!props.forcedMode);

  const [query, setQuery] = createSignal('');
  const [results, setResults] = createSignal<AgentEvent[]>([]);
  const [loading, setLoading] = createSignal(false);
  const [error, setError] = createSignal<string | null>(null);
  const [filterServiceId, setFilterServiceId] = createSignal<string>('all');
  const [filterAgentId, setFilterAgentId] = createSignal<string>('all');

  const [aiQuestion, setAiQuestion] = createSignal('');
  const [aiDate, setAiDate] = createSignal<'today' | 'yesterday' | string>('today');
  const [aiLoading, setAiLoading] = createSignal(false);
  const [aiError, setAiError] = createSignal<string | null>(null);
  const [needsLLMStart, setNeedsLLMStart] = createSignal(false);
  const [aiTurns, setAiTurns] = createSignal<AskAITurn[]>([]);
  const [aiChatId, setAiChatId] = createSignal<string>('');
  const [aiPendingQuestion, setAiPendingQuestion] = createSignal<string | null>(null);

  const [viewingImage, setViewingImage] = createSignal<string | null>(null);

  let debounceTimer: number | null = null;

  onMount(async () => {
    await Promise.all([fetchAgents(), fetchNodes(), fetchCameras(), fetchLLMWorkerStatus()]);
    const storedChatId = localStorage.getItem(ASK_AI_CHAT_ID_KEY) || '';
    if (storedChatId) {
      const chat = await fetchQueryChat(storedChatId);
      if (chat?.id) {
        setAiChatId(chat.id);
        setAiTurns(turnsFromChatMessages(chat.messages || [], aiDate()));
      } else {
        localStorage.removeItem(ASK_AI_CHAT_ID_KEY);
      }
    }
  });

  onCleanup(() => {
    if (debounceTimer) window.clearTimeout(debounceTimer);
    debounceTimer = null;
  });

  const allServices = createMemo(() => nodes().flatMap((n) => n.services || []));
  const clearConversation = () => {
    setAiTurns([]);
    setAiChatId('');
    setAiPendingQuestion(null);
    setAiQuestion('');
    setAiError(null);
    setNeedsLLMStart(false);
    localStorage.removeItem(ASK_AI_CHAT_ID_KEY);
  };

  const deleteConversation = async () => {
    const currentChatId = aiChatId();
    if (currentChatId) {
      await deleteQueryChat(currentChatId);
    }
    clearConversation();
  };

  const nodeIdForService = (serviceId: string) => {
    for (const n of nodes()) {
      if ((n.services || []).some((s) => s.id === serviceId)) return n.id;
    }
    return '';
  };

  const serviceName = (serviceId: string) => {
    if (!serviceId) return '';
    const cam = cameras().find((c) => c.id === serviceId);
    if (cam) return cam.name;
    for (const n of nodes()) {
      const service = (n.services || []).find((s) => s.id === serviceId);
      if (service) return service.name || service.id;
    }
    return serviceId.substring(0, 8);
  };

  const primaryServiceIdForEvent = (e: AgentEvent): string => {
    const md: any = e.metadata || {};
    return md.service_id || md.serviceId || md.service || (e.service_ids && e.service_ids.length ? e.service_ids[0] : '');
  };

  const prettySummary = (e: AgentEvent) => {
    const data: any = e.data || {};
    return data.summary || data.answer || data.result || data.message || '';
  };

  const getSnapshotUrl = (e: AgentEvent): string | null => {
    const meta: any = e.metadata || {};
    const data: any = e.data || {};
    const path = meta.snapshot_path || data.snapshot_path;
    return resolveSnapshotUrl(path);
  };

  const getAiSnapshotUrl = (e: AIQueryEvent): string | null => {
    return resolveSnapshotUrl(e.snapshot_path);
  };

  const getTimelineSnapshotUrl = (m: AIQueryTimelineEvidence): string | null => {
    return resolveSnapshotUrl(m.snapshot_path);
  };

  const timelinePersonLabel = (m: AIQueryTimelineEvidence) => {
    const name = (m.person_name || '').trim();
    if (name) return name;
    const gid = (m.global_person_id || '').trim();
    if (!gid) return 'Unknown person';
    const unknownMatch = gid.match(/unknown[:/#\s-]*(\d+)/i);
    if (unknownMatch?.[1]) return `Unknown #${unknownMatch[1]}`;
    return gid;
  };

  const timelineCameraLabel = (m: AIQueryTimelineEvidence) => {
    return (m.camera_name || (m.service_id ? serviceName(m.service_id) : '') || m.service_id || '').trim();
  };

  const timelineConfidenceLabel = (m: AIQueryTimelineEvidence) => {
    const score = Number(m.identity_confidence || 0);
    if (!score) return '';
    return `${Math.round(score * 100)}%`;
  };

  const resolveSnapshotUrl = (rawPath?: string | null): string | null => {
    const path = String(rawPath || '').trim();
    if (!path) return null;

    let baseUrl = '';
    if (/^https?:\/\//i.test(path)) {
      baseUrl = path;
    } else {
      const storageMarker = '/storage/snapshots/';
      const markerPos = path.indexOf(storageMarker);
      if (markerPos >= 0) baseUrl = relay(path.slice(markerPos));
      else if (path.startsWith('/')) baseUrl = relay(path);
      else baseUrl = relay(`/storage/snapshots/${path.replace(/^\/+/, '')}`);
    }

    const token = getToken();
    if (!token) return baseUrl;
    const sep = baseUrl.includes('?') ? '&' : '?';
    return `${baseUrl}${sep}token=${encodeURIComponent(token)}`;
  };

  const sanitizeAiAnswer = (input: string) => {
    let out = (input || '').replace(/\r/g, ' ').trim();
    if (!out) return '';

    out = out.replace(/<\|[^>]*\|>/g, ' ');
    if (out.includes('```')) out = out.split('```', 1)[0] || '';
    out = out.replace(/\s*---\s*user:.*$/is, ' ');
    out = out.replace(/\s*---\s*assistant:.*$/is, ' ');

    const noisePatterns = [
      /focus on the overall pattern\.?/gi,
      /do not begin with phrases like[^.]*\.?/gi,
      /keep the answer concise[^.]*\.?/gi,
      /do not include any code fences[^.]*\.?/gi,
      /never emit code fences[^.]*\.?/gi,
      /do not output code fences or xml[^.]*\.?/gi,
      /do not repeat or restate prompt instructions[^.]*\.?/gi,
      /do not repeat the prompt[^.]*\.?/gi,
      /return only the final operator-facing answer[^.]*\.?/gi,
      /date context\s*:\s*\d{4}-\d{2}-\d{2}\.?/gi,
      /do not list every event line-by-line\.?/gi,
      /avoid listing every event line-by-line\.?/gi,
      /respond in english only[^.]*\.?/gi,
      /additional notes\s*:/gi,
      /\banswer\s*:/gi,
    ];
    for (const pattern of noisePatterns) out = out.replace(pattern, ' ');

    out = out
      .replace(/\s+\*\s*/g, ' ')
      .replace(/\s+/g, ' ')
      .trim();
    return out;
  };

  const agentTypeLabel = (type: string) => {
    switch (type) {
      case 'helmet':
      case 'helmet_detection':
        return 'Helmet Detection';
      case 'wellness':
        return 'Wellness Check';
      case 'security':
        return 'Security Check';
      default:
        return type;
    }
  };

  const severityBadge = (severity: string) => {
    switch (severity) {
      case 'high':
        return 'bg-red-500/20 text-red-300 border-red-500/35';
      case 'medium':
        return 'bg-amber-500/20 text-amber-300 border-amber-500/35';
      default:
        return 'bg-green-500/20 text-green-300 border-green-500/35';
    }
  };

  const runSearch = async (q: string) => {
    const trimmed = (q || '').trim();
    setQuery(trimmed);
    setError(null);
    setLoading(true);
    try {
      const res = await searchAgentEventsRest(trimmed, { limit: 100, offset: 0 });
      setResults(res);
    } catch (e: any) {
      setError(e?.message || 'Search failed');
      setResults([]);
    } finally {
      setLoading(false);
    }
  };

  const filteredResults = createMemo(() => {
    const sid = filterServiceId();
    const aid = filterAgentId();
    return results().filter((e) => {
      const serviceId = primaryServiceIdForEvent(e);
      const matchService = sid === 'all' || serviceId === sid || (e.service_ids || []).includes(sid);
      const matchAgent = aid === 'all' || e.agent_id === aid;
      return matchService && matchAgent;
    });
  });

  const runAIQuery = async () => {
    const q = aiQuestion().trim();
    if (!q) return;
    const dateScope = aiDate();
    setAiError(null);
    setNeedsLLMStart(false);
    setAiLoading(true);
    setAiPendingQuestion(q);
    setAiQuestion('');
    try {
      const history = buildChatHistoryFromTurns(aiTurns()).slice(-12);
      const res = await queryAI(q, dateScope, history, aiChatId());
      const cleanedAnswer = sanitizeAiAnswer(res.answer || '');
      const safeAnswer = cleanedAnswer || 'I found matching evidence, but could not generate a clean narrative response.';
      if (res.chat_id) {
        setAiChatId(res.chat_id);
        localStorage.setItem(ASK_AI_CHAT_ID_KEY, res.chat_id);
      }
      setAiTurns((prev) => [
        ...prev,
        {
          id: mkTurnId(),
          question: q,
          answer: safeAnswer,
          confidence: res.confidence || 'medium',
          intent: res.intent,
          dateScope,
          relevantEvents: Array.isArray(res.relevant_events) ? res.relevant_events : [],
          timelineEvidence: Array.isArray(res.timeline_evidence) ? res.timeline_evidence : [],
          sources: Array.isArray(res.sources) ? res.sources : [],
          traceId: res.trace_id,
          createdAt: new Date().toISOString(),
        },
      ]);
    } catch (e: any) {
      const msg = String(e?.message || 'AI query failed');
      if (msg.includes('llm_worker_not_running')) {
        setNeedsLLMStart(true);
        setAiError('Ask AI worker is offline. Start the local LLM worker, then retry.');
      } else {
        setAiError(msg);
      }
    } finally {
      setAiPendingQuestion(null);
      setAiLoading(false);
    }
  };

  const handleStartLLM = async () => {
    setAiError(null);
    const ok = await startLLMWorker('google/gemma-3-1b-it');
    if (!ok) {
      setAiError('Failed to start Ask AI worker.');
      return;
    }
    setNeedsLLMStart(false);
    await fetchLLMWorkerStatus();
  };

  const dateLabel = (d: string) => {
    if (d === 'today') return 'Today';
    if (d === 'yesterday') return 'Yesterday';
    return d;
  };

  const resultsCount = createMemo(() => filteredResults().length);
  const activeSearchFilters = createMemo(() => {
    let count = 0;
    if (filterServiceId() !== 'all') count++;
    if (filterAgentId() !== 'all') count++;
    return count;
  });
  const workerStateLabel = createMemo(() => {
    if (!llmWorkerStatus().running) return 'Offline';
    return llmWorkerStatus().ready ? 'Running' : 'Starting';
  });
  const workerStateTone = createMemo(() => {
    if (!llmWorkerStatus().running) return 'text-amber-300 bg-amber-500/10 border-amber-500/25';
    if (!llmWorkerStatus().ready) return 'text-cyan-300 bg-cyan-500/10 border-cyan-500/25';
    return 'text-green-300 bg-green-500/10 border-green-500/25';
  });
  const hasConversation = createMemo(() => aiTurns().length > 0 || !!aiPendingQuestion());

  return (
    <div class="p-4 md:p-5 h-full flex flex-col bg-neu-925/20">
      <Show when={viewingImage()}>
        <ImageViewer src={viewingImage()!} onClose={() => setViewingImage(null)} />
      </Show>

      <Show when={!modeLocked()}>
        <div class="flex-shrink-0 mb-4">
          <div class="flex items-center gap-1.5 bg-neu-900/78 rounded-xl p-1.5 w-fit border border-neu-800/80 shadow-sm">
            <button
              class={`px-4 py-1.5 rounded-lg text-sm font-medium transition-all ${activeMode() === 'search' ? 'bg-neu-800 text-white shadow' : 'text-neu-400 hover:text-neu-200'}`}
              onClick={() => setMode('search')}
            >
              Search
            </button>
            <button
              class={`px-4 py-1.5 rounded-lg text-sm font-medium transition-all flex items-center gap-1.5 ${activeMode() === 'ai' ? 'bg-violet-600 text-white shadow' : 'text-neu-400 hover:text-neu-200'}`}
              onClick={() => setMode('ai')}
            >
              <FiMessageSquare size={14} />
              Ask AI
            </button>
          </div>
        </div>
      </Show>

      <Show when={activeMode() === 'search'}>
        <div class="flex-1 flex flex-col min-h-0 gap-4">
          <div class="flex-shrink-0 rounded-2xl border border-neu-800/80 bg-[linear-gradient(145deg,rgba(12,18,34,0.94),rgba(10,16,30,0.88))] p-4 md:p-5 shadow-sm">
            <div class="flex flex-col xl:flex-row xl:items-center xl:justify-between gap-3">
              <div>
                <h2 class="text-lg md:text-xl font-semibold text-neu-100 tracking-tight flex items-center gap-2">
                  <FiSearch size={17} class="text-cyan-300" />
                  Event Search
                </h2>
                <p class="text-xs md:text-sm text-neu-500 mt-1">Fast lookup across moments with camera and agent filters.</p>
              </div>
              <div class="grid grid-cols-2 gap-2 sm:min-w-[240px]">
                <div class="rounded-xl border border-neu-800 bg-neu-900/70 px-3 py-2">
                  <div class="text-[10px] uppercase tracking-[0.08em] text-neu-500">Results</div>
                  <div class="text-sm font-semibold text-neu-100 mt-0.5">{resultsCount()}</div>
                </div>
                <div class="rounded-xl border border-neu-800 bg-neu-900/70 px-3 py-2">
                  <div class="text-[10px] uppercase tracking-[0.08em] text-neu-500">Filters</div>
                  <div class="text-sm font-semibold text-neu-100 mt-0.5">{activeSearchFilters()}</div>
                </div>
              </div>
            </div>

            <div class="mt-4 grid grid-cols-1 xl:grid-cols-[minmax(0,1fr)_auto] gap-2.5">
              <div class="h-10 rounded-xl border border-neu-750 bg-neu-950/60 px-3 flex items-center gap-2 focus-within:border-cyan-500/45 transition-colors">
                <FiSearch size={14} class="text-neu-500" />
                <input
                  class="bg-transparent flex-1 text-sm text-neu-100 placeholder:text-neu-600 outline-none"
                  placeholder="Search by person, violation, object, activity, camera, or summary text..."
                  value={query()}
                  onInput={(e) => {
                    const v = e.currentTarget.value;
                    setQuery(v);
                    setError(null);
                    if (debounceTimer) window.clearTimeout(debounceTimer);
                    debounceTimer = window.setTimeout(() => {
                      const trimmed = v.trim();
                      if (trimmed.length >= 2) runSearch(trimmed);
                      else setResults([]);
                    }, 350);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') runSearch(query());
                  }}
                />
              </div>
              <div class="flex items-center gap-2">
                <button class="btn-primary h-10 px-4" onClick={() => runSearch(query())} disabled={loading()}>
                  {loading() ? 'Searching...' : 'Search'}
                </button>
                <button
                  class="btn-secondary h-10 px-3"
                  onClick={() => {
                    setQuery('');
                    setResults([]);
                    setError(null);
                  }}
                >
                  Clear
                </button>
              </div>
            </div>

            <div class="mt-3 grid grid-cols-1 md:grid-cols-2 gap-2.5">
              <div>
                <label class="text-[11px] text-neu-500 mb-1 block">Camera</label>
                <select class="select w-full" value={filterServiceId()} onChange={(e) => setFilterServiceId(e.currentTarget.value)}>
                  <option value="all">All cameras</option>
                  <For each={allServices()}>{(s) => <option value={s.id}>{s.name || s.id}</option>}</For>
                </select>
              </div>
              <div>
                <label class="text-[11px] text-neu-500 mb-1 block">Agent</label>
                <select class="select w-full" value={filterAgentId()} onChange={(e) => setFilterAgentId(e.currentTarget.value)}>
                  <option value="all">All agents</option>
                  <For each={agents()}>{(a) => <option value={a.id}>{a.name}</option>}</For>
                </select>
              </div>
            </div>
          </div>

          <Show when={error()}>
            <div class="flex-shrink-0 p-3 rounded-xl bg-red-500/10 text-red-300 border border-red-500/25 text-sm">
              {error()}
            </div>
          </Show>

          <div class="flex-1 overflow-y-auto min-h-0 pr-0.5">
            <Show
              when={resultsCount() > 0}
              fallback={
                <div class="h-full min-h-[280px] flex items-center justify-center">
                  <div class="saas-empty w-full max-w-2xl">
                    <FiFilter class="mx-auto mb-3 w-8 h-8" />
                    <p class="text-neu-300 font-medium">{query().trim().length < 2 ? 'Start with a query' : loading() ? 'Searching events...' : 'No matching events found'}</p>
                    <p class="text-xs mt-1">
                      {query().trim().length < 2
                        ? 'Type at least 2 characters to query the event index.'
                        : 'Try broader terms or remove one of the filters.'}
                    </p>
                  </div>
                </div>
              }
            >
              <div class="space-y-3">
                <For each={filteredResults()}>
                  {(e) => {
                    const sid = primaryServiceIdForEvent(e);
                    const nid = sid ? nodeIdForService(sid) : '';
                    const snapshotUrl = getSnapshotUrl(e);
                    const summary = prettySummary(e);
                    const meta: any = e.metadata || {};
                    const data: any = e.data || {};
                    const agentType = meta.agent_type || 'unknown';
                    const severity = data.severity || meta.severity;
                    const vlmSummary = data.vlm_summary || meta.vlm_summary;

                    return (
                      <article class="group rounded-2xl border border-neu-800/80 bg-neu-900/72 p-4 md:p-5 shadow-sm hover:border-cyan-500/35 transition-colors">
                        <div class={`grid gap-4 ${snapshotUrl ? 'lg:grid-cols-[180px_minmax(0,1fr)]' : 'grid-cols-1'}`}>
                          <Show when={snapshotUrl}>
                            <button
                              class="relative h-[128px] rounded-xl overflow-hidden border border-neu-750 hover:border-cyan-500/40 transition-colors"
                              onClick={() => setViewingImage(snapshotUrl!)}
                            >
                              <img
                                src={snapshotUrl!}
                                alt="Snapshot"
                                class="w-full h-full object-cover"
                                loading="lazy"
                                onError={(ev) => {
                                  (ev.currentTarget as HTMLImageElement).parentElement!.style.display = 'none';
                                }}
                              />
                              <div class="absolute inset-0 bg-black/0 group-hover:bg-black/35 transition-colors flex items-center justify-center">
                                <FiZoomIn size={16} class="text-white opacity-0 group-hover:opacity-100 transition-opacity" />
                              </div>
                            </button>
                          </Show>

                          <div class="min-w-0">
                            <div class="flex flex-wrap items-center gap-2">
                              <span class="text-sm md:text-[15px] font-semibold text-neu-100">{e.agent_name}</span>
                              <span class="px-2 py-0.5 rounded-full text-[10px] font-medium bg-neu-850 text-neu-300 border border-neu-700">
                                {agentTypeLabel(agentType)}
                              </span>
                              <Show when={severity}>
                                <span class={`px-2 py-0.5 rounded-full text-[10px] font-semibold uppercase border ${severityBadge(severity)}`}>
                                  {severity}
                                </span>
                              </Show>
                              <span class="ml-auto flex items-center gap-1 text-[10px] text-neu-500">
                                <FiClock size={10} />
                                {new Date(e.created_at).toLocaleString()}
                              </span>
                            </div>

                            <p class="mt-2 text-sm text-neu-200 leading-relaxed">{summary || 'No summary available.'}</p>

                            <Show when={vlmSummary && vlmSummary !== summary}>
                              <div class="mt-2 rounded-lg border border-neu-750/70 bg-neu-950/55 px-3 py-2 text-xs text-neu-400 leading-relaxed">
                                {vlmSummary}
                              </div>
                            </Show>

                            <div class="mt-3 flex flex-wrap items-center gap-1.5">
                              <For each={e.service_ids || []}>
                                {(s) => (
                                  <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] bg-neu-850 text-neu-300 border border-neu-700">
                                    <FiCamera size={9} />
                                    {serviceName(s)}
                                  </span>
                                )}
                              </For>
                              <Show when={nid && sid}>
                                <button
                                  class="ml-auto px-3 py-1 rounded-lg text-[11px] bg-cyan-500/10 text-cyan-300 border border-cyan-500/25 hover:bg-cyan-500/20 transition-colors"
                                  onClick={() => {
                                    if (!nid || !sid) return;
                                    setTab({ type: 'view', nodeId: nid, serviceId: sid } as any);
                                  }}
                                >
                                  Open Camera
                                </button>
                              </Show>
                            </div>
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
      </Show>

      <Show when={activeMode() === 'ai'}>
        <div class="flex-1 flex flex-col min-h-0 gap-4">
          <div class="flex-shrink-0 rounded-2xl border border-neu-800/80 bg-[linear-gradient(145deg,rgba(14,24,45,0.9),rgba(11,18,34,0.88))] p-4 md:p-5 shadow-sm">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 class="text-lg md:text-xl font-semibold text-neu-100 tracking-tight flex items-center gap-2">
                  <FiMessageSquare size={17} class="text-violet-300" />
                  Ask AI
                </h2>
                <p class="text-xs md:text-sm text-neu-500 mt-1">Ask narrative questions and inspect evidence grounded in recorded events.</p>
              </div>
              <div class="flex items-center gap-2">
                <span class={`inline-flex items-center gap-1.5 rounded-full px-2.5 py-1 text-[10px] font-semibold border ${workerStateTone()}`}>
                  <FiCpu size={11} />
                  Worker {workerStateLabel()}
                </span>
                <Show when={llmWorkerStatus().model_id}>
                  <span class="text-[10px] text-neu-500 rounded-full px-2 py-1 border border-neu-800 bg-neu-900/60">
                    {llmWorkerStatus().model_id}
                  </span>
                </Show>
                <button
                  class="btn-small"
                  onClick={() => fetchLLMWorkerStatus()}
                  title="Refresh Ask AI worker status"
                >
                  <FiRefreshCw size={12} />
                  Refresh
                </button>
              </div>
            </div>
          </div>

          <div class="flex-shrink-0 rounded-2xl border border-neu-800/80 bg-neu-900/72 p-4 md:p-5 shadow-sm">
            <label class="text-[11px] text-neu-500">Question</label>
            <textarea
              class="textarea w-full mt-1 min-h-[96px] resize-y"
              placeholder='Example: "Who entered restricted area B yesterday after 7 PM?"'
              value={aiQuestion()}
              onInput={(e) => setAiQuestion(e.currentTarget.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) runAIQuery();
              }}
            />
            <div class="mt-2 flex flex-wrap items-center gap-2">
              <span class="text-[11px] text-neu-500">Date scope:</span>
              <For each={['today', 'yesterday'] as const}>
                {(d) => (
                  <button
                    class={`px-3 h-8 rounded-lg text-xs font-medium transition-all border ${
                      aiDate() === d
                        ? 'bg-violet-600/20 text-violet-200 border-violet-500/45'
                        : 'bg-neu-850 text-neu-400 border-neu-750 hover:border-neu-650 hover:text-neu-200'
                    }`}
                    onClick={() => setAiDate(d)}
                  >
                    {dateLabel(d)}
                  </button>
                )}
              </For>
              <input
                type="date"
                class="input text-xs px-2 py-1 h-8"
                value={aiDate() !== 'today' && aiDate() !== 'yesterday' ? aiDate() : ''}
                onChange={(e) => {
                  if (e.currentTarget.value) setAiDate(e.currentTarget.value);
                }}
              />
              <div class="ml-auto flex items-center gap-2">
                <button
                  class="btn-secondary h-8 px-3"
                  onClick={clearConversation}
                  disabled={aiLoading() || (!aiTurns().length && !aiQuestion().trim())}
                >
                  New Chat
                </button>
                <button
                  class="h-8 px-3 rounded-lg text-xs font-semibold border border-red-500/35 text-red-200 bg-red-500/12 hover:bg-red-500/20 transition-colors disabled:opacity-50 disabled:cursor-not-allowed"
                  onClick={deleteConversation}
                  disabled={aiLoading() || (!aiChatId() && aiTurns().length === 0)}
                >
                  Delete Chat
                </button>
                <button class="btn-primary h-8 px-4" disabled={aiLoading() || !aiQuestion().trim()} onClick={runAIQuery}>
                  {aiLoading() ? 'Thinking...' : 'Ask AI'}
                </button>
              </div>
            </div>
            <div class="text-[10px] text-neu-600 mt-2">Tip: press Ctrl+Enter to submit quickly.</div>

            <Show when={aiError()}>
              <div class="mt-3 p-3 rounded-xl bg-red-500/10 text-red-300 border border-red-500/25 text-sm">
                <div>{aiError()}</div>
                <Show when={needsLLMStart()}>
                  <button
                    class={`mt-2 px-3 py-1.5 rounded-lg text-xs font-semibold border ${
                      llmWorkerStatus().running
                        ? 'bg-neu-900 border-neu-800 text-neu-500 cursor-not-allowed'
                        : 'bg-neu-800 hover:bg-neu-700 border-neu-700 text-neu-200'
                    }`}
                    onClick={handleStartLLM}
                    disabled={llmWorkerStatus().running}
                  >
                    Start Ask AI Worker
                  </button>
                </Show>
              </div>
            </Show>
          </div>

          <div class="flex-1 overflow-y-auto min-h-0 pr-0.5">
            <div class="rounded-2xl border border-neu-800/80 bg-neu-900/72 p-4 md:p-5 min-h-full">
              <div class="text-xs font-semibold text-neu-400 mb-3 uppercase tracking-[0.08em]">Conversation</div>
              <div class="space-y-4">
                <Show
                  when={hasConversation()}
                  fallback={<p class="text-xs text-neu-500">Ask a question to start this conversation.</p>}
                >
                  <For each={aiTurns()}>
                    {(turn) => (
                      <article class="space-y-2 rounded-2xl border border-neu-800/70 bg-neu-950/40 p-3 md:p-4">
                        <div class="rounded-xl px-3 py-2 text-sm border bg-neu-850 border-neu-750 text-neu-100">
                          <div class="text-[10px] uppercase tracking-wide text-neu-500 mb-1">you</div>
                          <div class="whitespace-pre-wrap leading-relaxed">{turn.question}</div>
                        </div>

                        <div class="rounded-xl px-3 py-2 text-sm border bg-violet-500/10 border-violet-500/25 text-neu-200">
                          <div class="flex flex-wrap items-center gap-2 mb-1">
                            <div class="text-[10px] uppercase tracking-wide text-neu-500">assistant</div>
                            <ConfidenceBadge confidence={turn.confidence} />
                            <Show when={turn.intent}>
                              <span class="text-[10px] uppercase tracking-wide text-cyan-300 bg-cyan-500/10 border border-cyan-500/25 rounded-full px-2 py-0.5">
                                {turn.intent}
                              </span>
                            </Show>
                            <span class="ml-auto text-[10px] text-neu-500">{dateLabel(turn.dateScope)}</span>
                          </div>
                          <p class="whitespace-pre-wrap leading-relaxed">{turn.answer}</p>
                        </div>

                        <Show when={turn.timelineEvidence.length > 0}>
                          <div class="rounded-xl border border-cyan-500/20 bg-cyan-500/[0.055] p-3">
                            <div class="flex items-center justify-between mb-2">
                              <h3 class="text-xs font-semibold text-cyan-200 uppercase tracking-[0.07em]">Timeline Evidence</h3>
                              <span class="text-[10px] text-cyan-300/80">
                                {turn.timelineEvidence.length} moment{turn.timelineEvidence.length !== 1 ? 's' : ''}
                              </span>
                            </div>
                            <div class="space-y-2">
                              <For each={turn.timelineEvidence}>
                                {(moment) => {
                                  const snapUrl = getTimelineSnapshotUrl(moment);
                                  const cam = timelineCameraLabel(moment);
                                  const confidence = timelineConfidenceLabel(moment);
                                  return (
                                    <div class="rounded-xl border border-neu-800/80 bg-neu-950/55 p-3">
                                      <div class="flex flex-col md:flex-row gap-3">
                                        <div class="w-full md:w-[132px] md:flex-shrink-0">
                                          <Show
                                            when={snapUrl}
                                            fallback={
                                              <div class="w-full h-20 rounded-lg border border-neu-800/80 bg-neu-900/70 flex items-center justify-center text-[10px] text-neu-600">
                                                Snapshot unavailable
                                              </div>
                                            }
                                          >
                                            <button
                                              class="relative w-full h-20 rounded-lg overflow-hidden border border-neu-750 hover:border-cyan-400/45 transition-colors"
                                              onClick={() => setViewingImage(snapUrl!)}
                                            >
                                              <img
                                                src={snapUrl!}
                                                alt="Timeline snapshot"
                                                class="w-full h-full object-cover"
                                                loading="lazy"
                                              />
                                              <div class="absolute inset-0 bg-black/0 hover:bg-black/35 transition-colors flex items-center justify-center">
                                                <FiZoomIn size={12} class="text-white opacity-0 hover:opacity-100 transition-opacity" />
                                              </div>
                                            </button>
                                          </Show>
                                          <Show when={snapUrl}>
                                            <button
                                              class="mt-1 w-full h-7 rounded-md border border-cyan-500/35 text-[11px] text-cyan-200 bg-cyan-500/12 hover:bg-cyan-500/20 transition-colors"
                                              onClick={() => setViewingImage(snapUrl!)}
                                            >
                                              Full View + Zoom
                                            </button>
                                          </Show>
                                        </div>

                                        <div class="flex-1 min-w-0">
                                          <div class="flex items-center gap-1.5 mb-1 flex-wrap">
                                            <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] bg-blue-500/15 text-blue-300 border border-blue-500/30">
                                              <FiUser size={9} />
                                              {timelinePersonLabel(moment)}
                                            </span>
                                            <Show when={confidence}>
                                              <span class="px-1.5 py-0.5 rounded text-[10px] bg-green-500/15 text-green-300 border border-green-500/30">
                                                {confidence}
                                              </span>
                                            </Show>
                                            <Show when={cam}>
                                              <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] bg-neu-850 text-neu-300 border border-neu-700">
                                                <FiCamera size={9} />
                                                {cam}
                                              </span>
                                            </Show>
                                            <Show when={moment.severity}>
                                              <span class={`px-1.5 py-0.5 rounded text-[10px] font-semibold uppercase border ${severityBadge(moment.severity || '')}`}>
                                                {moment.severity}
                                              </span>
                                            </Show>
                                            <span class="ml-auto text-[10px] text-neu-600">
                                              {new Date(moment.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                                            </span>
                                          </div>
                                          <p class="text-xs text-neu-300 leading-relaxed">
                                            {moment.activity_brief || moment.event_kind || 'Person observed.'}
                                          </p>
                                          <div class="mt-1 text-[10px] text-neu-600 truncate">
                                            {moment.global_person_id}
                                          </div>
                                        </div>
                                      </div>
                                    </div>
                                  );
                                }}
                              </For>
                            </div>
                          </div>
                        </Show>

                        <div class="rounded-xl border border-neu-800/75 bg-neu-925/60 p-3">
                          <div class="flex items-center justify-between mb-2">
                            <h3 class="text-xs font-semibold text-neu-300 uppercase tracking-[0.07em]">Supporting Evidence</h3>
                            <span class="text-[10px] text-neu-500">
                              {turn.relevantEvents.length} event{turn.relevantEvents.length !== 1 ? 's' : ''}
                            </span>
                          </div>
                          <Show
                            when={turn.relevantEvents.length > 0}
                            fallback={<p class="text-xs text-neu-500">No supporting events were returned for this answer.</p>}
                          >
                            <div class="space-y-2">
                              <For each={turn.relevantEvents}>
                                {(ev) => {
                                  const snapUrl = getAiSnapshotUrl(ev);
                                  const sname = ev.service_id ? serviceName(ev.service_id) : null;
                                  return (
                                    <div class="rounded-xl border border-neu-800/80 bg-neu-950/55 p-3">
                                      <div class="flex flex-col md:flex-row gap-3">
                                        <div class="w-full md:w-[132px] md:flex-shrink-0">
                                          <Show
                                            when={snapUrl}
                                            fallback={
                                              <div class="w-full h-20 rounded-lg border border-neu-800/80 bg-neu-900/70 flex items-center justify-center text-[10px] text-neu-600">
                                                Snapshot unavailable
                                              </div>
                                            }
                                          >
                                            <button
                                              class="relative w-full h-20 rounded-lg overflow-hidden border border-neu-750 hover:border-violet-500/45 transition-colors"
                                              onClick={() => setViewingImage(snapUrl!)}
                                            >
                                              <img
                                                src={snapUrl!}
                                                alt="Snapshot"
                                                class="w-full h-full object-cover"
                                                loading="lazy"
                                                onError={(e) => { (e.currentTarget as HTMLImageElement).parentElement!.style.display = 'none'; }}
                                              />
                                              <div class="absolute inset-0 bg-black/0 hover:bg-black/35 transition-colors flex items-center justify-center">
                                                <FiZoomIn size={12} class="text-white opacity-0 hover:opacity-100 transition-opacity" />
                                              </div>
                                            </button>
                                          </Show>
                                          <Show when={snapUrl}>
                                            <button
                                              class="mt-1 w-full h-7 rounded-md border border-violet-500/35 text-[11px] text-violet-200 bg-violet-500/12 hover:bg-violet-500/20 transition-colors"
                                              onClick={() => setViewingImage(snapUrl!)}
                                            >
                                              Full View + Zoom
                                            </button>
                                          </Show>
                                        </div>

                                        <div class="flex-1 min-w-0">
                                          <div class="flex items-center gap-1.5 mb-1 flex-wrap">
                                            <Show when={ev.severity}>
                                              <span class={`px-1.5 py-0.5 rounded text-[10px] font-semibold uppercase border ${severityBadge(ev.severity)}`}>
                                                {ev.severity}
                                              </span>
                                            </Show>
                                            <Show when={ev.person_name}>
                                              <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] bg-blue-500/15 text-blue-300 border border-blue-500/30">
                                                <FiUser size={9} />
                                                {ev.person_name}
                                              </span>
                                            </Show>
                                            <Show when={sname}>
                                              <span class="inline-flex items-center gap-1 px-1.5 py-0.5 rounded text-[10px] bg-neu-850 text-neu-300 border border-neu-700">
                                                <FiCamera size={9} />
                                                {sname}
                                              </span>
                                            </Show>
                                            <span class="ml-auto text-[10px] text-neu-600">
                                              {new Date(ev.created_at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}
                                            </span>
                                          </div>
                                          <p class="text-xs text-neu-300 leading-relaxed">{ev.brief || 'No description available.'}</p>
                                        </div>
                                      </div>
                                    </div>
                                  );
                                }}
                              </For>
                            </div>
                          </Show>
                        </div>

                        <Show when={turn.sources.length > 0}>
                          <div class="rounded-xl border border-neu-800/75 bg-neu-925/60 p-3">
                            <div class="flex items-center justify-between mb-2">
                              <h3 class="text-xs font-semibold text-neu-300 uppercase tracking-[0.07em]">Knowledge Sources</h3>
                              <span class="text-[10px] text-neu-500">
                                {turn.sources.length} source{turn.sources.length !== 1 ? 's' : ''}
                              </span>
                            </div>
                            <div class="space-y-2">
                              <For each={turn.sources}>
                                {(src) => (
                                  <div class="rounded-xl border border-neu-800/80 bg-neu-950/55 p-3">
                                    <div class="flex items-center gap-2 flex-wrap">
                                      <span class="text-xs font-semibold text-neu-200">{src.title || src.source_uri}</span>
                                      <Show when={src.heading}>
                                        <span class="text-[10px] text-neu-500">{src.heading}</span>
                                      </Show>
                                      <span class="ml-auto text-[10px] text-violet-300">
                                        {Math.round((src.score || 0) * 100)}%
                                      </span>
                                    </div>
                                    <div class="mt-1 text-[10px] text-neu-600 break-all">{src.source_uri}</div>
                                    <p class="mt-2 text-xs text-neu-300 leading-relaxed">{src.snippet}</p>
                                  </div>
                                )}
                              </For>
                            </div>
                          </div>
                        </Show>
                      </article>
                    )}
                  </For>

                  <Show when={aiPendingQuestion()}>
                    {(pendingQ) => (
                      <article class="space-y-2 rounded-2xl border border-neu-800/70 bg-neu-950/30 p-3 md:p-4">
                        <div class="rounded-xl px-3 py-2 text-sm border bg-neu-850 border-neu-750 text-neu-100">
                          <div class="text-[10px] uppercase tracking-wide text-neu-500 mb-1">you</div>
                          <div class="whitespace-pre-wrap leading-relaxed">{pendingQ()}</div>
                        </div>
                        <div class="rounded-xl px-3 py-2 text-sm border bg-violet-500/10 border-violet-500/25 text-neu-200">
                          <div class="text-[10px] uppercase tracking-wide text-neu-500 mb-1">assistant</div>
                          <div class="flex items-center gap-2">
                            <span class="inline-block w-1.5 h-1.5 rounded-full bg-violet-300 animate-pulse" />
                            <span>Thinking…</span>
                          </div>
                        </div>
                      </article>
                    )}
                  </Show>
                </Show>
              </div>
            </div>
          </div>
        </div>
      </Show>
    </div>
  );
};

export default TabSearch;
