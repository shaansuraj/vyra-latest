import { Component, For, Show, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import {
  FiCheck,
  FiGrid,
  FiLayers,
  FiMaximize2,
  FiPlay,
  FiUsers,
  FiX,
} from 'solid-icons/fi';
import {
  agents,
  agentsLoading,
  connectAgentEventsWebSocket,
  fetchAgents,
  fetchManagedNodes,
  fetchNodes,
  fetchRecentAgentEventsRest,
  managedNodes,
  nodes,
  nodesLoading,
  patchAgent,
  subscribeToAgentEvents,
  type AgentEvent,
} from '../../shared';
import VideoTile from '../VideoTile';

const LIVE_IDENTITY_WINDOW_MS = 30 * 60 * 1000;
const LIVE_INCIDENT_WINDOW_MS = 5 * 60 * 1000;
const RECENT_EVENT_RETENTION_MS = 2 * 60 * 60 * 1000;
const RECENT_EVENT_LIMIT = 600;
const AUTO_PLAY_PREF_KEY = 'command_center_auto_play';

type ServiceCard = {
  id: string;
  name: string;
  type: string;
  node_id: string;
  node_name: string;
  node_status: string;
};

const TabBirdEye: Component = () => {
  const [selectedGroupId, setSelectedGroupId] = createSignal<string>('all');
  const [autoPlayAll, setAutoPlayAll] = createSignal(false);
  const [showAgentPicker, setShowAgentPicker] = createSignal(false);
  const [agentPickerServiceId, setAgentPickerServiceId] = createSignal<string | null>(null);
  const [applyAllAgentId, setApplyAllAgentId] = createSignal<string>('');
  const [fullscreenService, setFullscreenService] = createSignal<ServiceCard | null>(null);
  const [bulkApplied, setBulkApplied] = createSignal(false);
  const [recentEvents, setRecentEvents] = createSignal<AgentEvent[]>([]);

  const eventTimestamp = (ev: AgentEvent): number => {
    const ms = Date.parse(ev.created_at || '');
    return Number.isFinite(ms) ? ms : 0;
  };

  const sortAndPruneEvents = (events: AgentEvent[]): AgentEvent[] => {
    const now = Date.now();
    const dedup = new Map<string, AgentEvent>();
    for (const ev of events) {
      if (!ev?.id) continue;
      const ts = eventTimestamp(ev);
      if (!ts) continue;
      if (now-ts > RECENT_EVENT_RETENTION_MS) continue;
      if (!dedup.has(ev.id)) dedup.set(ev.id, ev);
    }
    return Array.from(dedup.values())
      .sort((a, b) => eventTimestamp(b)-eventTimestamp(a))
      .slice(0, RECENT_EVENT_LIMIT);
  };

  const persistAutoPlay = (enabled: boolean) => {
    setAutoPlayAll(enabled);
    try {
      window.localStorage.setItem(AUTO_PLAY_PREF_KEY, enabled ? '1' : '0');
    } catch {}
  };

  onMount(async () => {
    try {
      const raw = window.localStorage.getItem(AUTO_PLAY_PREF_KEY);
      setAutoPlayAll(raw === '1');
    } catch {
      setAutoPlayAll(false);
    }

    await Promise.all([fetchNodes(), fetchManagedNodes(), fetchAgents()]);

    const loadRecentEvents = async () => {
      try {
        const evs = await fetchRecentAgentEventsRest({ limit: 300 });
        setRecentEvents((prev) => sortAndPruneEvents([...evs, ...prev]));
      } catch {
        setRecentEvents((prev) => sortAndPruneEvents(prev));
      }
    };

    connectAgentEventsWebSocket();
    const unsub = subscribeToAgentEvents((ev) => {
      setRecentEvents((prev) => sortAndPruneEvents([ev, ...prev]));
    });

    await loadRecentEvents();
    const pollTimer = setInterval(loadRecentEvents, 30000);
    const pruneTimer = setInterval(() => {
      setRecentEvents((prev) => sortAndPruneEvents(prev));
    }, 30000);
    onCleanup(() => {
      clearInterval(pollTimer);
      clearInterval(pruneTimer);
      unsub();
    });
  });

  const handleKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      if (fullscreenService()) setFullscreenService(null);
      else if (showAgentPicker()) closeAgentPicker();
    }
  };
  onMount(() => {
    document.addEventListener('keydown', handleKeyDown);
    onCleanup(() => document.removeEventListener('keydown', handleKeyDown));
  });

  const managedNodeNameMap = createMemo(() => {
    const out = new Map<string, string>();
    for (const mn of managedNodes()) out.set(mn.id, mn.name || mn.id);
    return out;
  });

  const allServices = createMemo<ServiceCard[]>(() =>
    nodes().flatMap((n) =>
      (n.services || []).map((s) => ({
        ...s,
        node_id: n.id,
        node_name: n.name || managedNodeNameMap().get(n.id) || n.id,
        node_status: n.status,
      }))
    )
  );

  const serviceNameMap = createMemo(() => {
    const out = new Map<string, string>();
    for (const s of allServices()) out.set(s.id, s.name || s.id);
    return out;
  });

  const eventServiceId = (ev: AgentEvent): string => {
    const meta = (ev.metadata || {}) as Record<string, unknown>;
    const data = (ev.data || {}) as Record<string, unknown>;
    const fromMeta = (meta.service_id || meta.serviceId || meta.service) as string | undefined;
    if (fromMeta) return fromMeta;
    const fromData = (data.service_id || data.serviceId || data.service) as string | undefined;
    if (fromData) return fromData;
    const ids = ev.service_ids || [];
    return ids.length ? ids[0] : '';
  };

  const eventSeverity = (ev: AgentEvent): string =>
    String((ev.data as any)?.severity ?? (ev.metadata as any)?.severity ?? '').toLowerCase();

  const eventKind = (ev: AgentEvent): string =>
    String((ev.data as any)?.event_kind ?? (ev.metadata as any)?.event_kind ?? '').toLowerCase();

  const eventHasViolation = (ev: AgentEvent): boolean => {
    const v = (ev.data as any)?.has_violations ?? (ev.metadata as any)?.has_violations;
    return v === true || v === 1 || v === '1' || v === 'true';
  };

  const eventBrief = (ev: AgentEvent): string =>
    String(
      (ev.data as any)?.brief ||
      (ev.data as any)?.event_brief ||
      (ev.metadata as any)?.brief ||
      ''
    ).trim();

  const nodeGroups = createMemo(() => {
    const byNode = new Map<string, { id: string; name: string; count: number }>();
    for (const s of allServices()) {
      const existing = byNode.get(s.node_id);
      if (existing) {
        existing.count += 1;
        continue;
      }
      byNode.set(s.node_id, { id: s.node_id, name: s.node_name || s.node_id, count: 1 });
    }
    return Array.from(byNode.values()).sort((a, b) => a.name.localeCompare(b.name));
  });

  const sortByName = (list: ServiceCard[]) =>
    list.slice().sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id));

  const visibleServices = createMemo(() => {
    const groupId = selectedGroupId();
    const list = allServices();
    if (groupId === 'all') return sortByName(list);
    if (groupId.startsWith('node:')) {
      const nid = groupId.slice(5);
      return sortByName(list.filter((s) => s.node_id === nid));
    }
    return sortByName(list);
  });

  const groupedVisibleServices = createMemo(() => {
    const byNode = new Map<string, { nodeID: string; nodeName: string; services: ServiceCard[] }>();
    for (const s of visibleServices()) {
      const existing = byNode.get(s.node_id);
      if (existing) {
        existing.services.push(s);
      } else {
        byNode.set(s.node_id, {
          nodeID: s.node_id,
          nodeName: s.node_name || s.node_id,
          services: [s],
        });
      }
    }
    return Array.from(byNode.values())
      .map((group) => ({ ...group, services: sortByName(group.services) }))
      .sort((a, b) => a.nodeName.localeCompare(b.nodeName));
  });

  const shouldGroupByNode = createMemo(
    () => selectedGroupId() === 'all' && groupedVisibleServices().length > 1
  );

  const visibleServiceIds = createMemo(() => new Set(visibleServices().map((s) => s.id)));

  const incidentEvents = createMemo(() =>
    recentEvents()
      .filter((ev) => {
        const ts = eventTimestamp(ev);
        if (!ts) return false;
        if ((Date.now()-ts) > LIVE_INCIDENT_WINDOW_MS) return false;

        const sid = eventServiceId(ev);
        const visible = visibleServiceIds();
        if (sid && visible.size > 0 && !visible.has(sid)) return false;

        const brief = eventBrief(ev);
        if (/error loading image|no such file or directory|traceback/i.test(brief)) return false;

        const sev = eventSeverity(ev);
        const kind = eventKind(ev);
        if (eventHasViolation(ev)) return true;
        if (sev === 'high' || sev === 'medium') return true;
        return kind.includes('violation') || kind.includes('incident');
      })
      .sort((a, b) => eventTimestamp(b)-eventTimestamp(a))
      .slice(0, 14)
  );

  const activeIdentities = createMemo(() => {
    const byIdentity = new Map<string, { label: string; cameras: Set<string>; lastSeen: string; count: number }>();
    const nowMs = Date.now();
    const visible = visibleServiceIds();
    for (const ev of recentEvents()) {
      const created = eventTimestamp(ev);
      if (!created) continue;
      if (nowMs-created > LIVE_IDENTITY_WINDOW_MS) continue;

      const data = (ev.data || {}) as Record<string, unknown>;
      const meta = (ev.metadata || {}) as Record<string, unknown>;
      const globalID = String(data.global_person_id || meta.global_person_id || '').trim();
      const personName = String(data.person_name || meta.person_name || '').trim();
      if (!globalID && !personName) continue;
      const serviceID = eventServiceId(ev);
      if (serviceID && visible.size > 0 && !visible.has(serviceID)) continue;
      const key = globalID || `name:${personName.toLowerCase()}`;
      const label = personName || globalID;
      if (!byIdentity.has(key)) {
        byIdentity.set(key, {
          label,
          cameras: new Set(serviceID ? [serviceID] : []),
          lastSeen: ev.created_at,
          count: 1,
        });
        continue;
      }
      const cur = byIdentity.get(key)!;
      cur.count += 1;
      if (serviceID) cur.cameras.add(serviceID);
      if ((ev.created_at || '') > cur.lastSeen) cur.lastSeen = ev.created_at;
    }
    return Array.from(byIdentity.values())
      .sort((a, b) => (a.lastSeen < b.lastSeen ? 1 : -1))
      .slice(0, 12);
  });

  const isLegacyFaceAgent = (workerId: string) => workerId === 'vyra/face_recognition' || workerId === 'vyra/face';

  const serviceAssignedAgents = (serviceId: string) =>
    agents().filter((a) => !isLegacyFaceAgent(a.worker_id) && (a.service_ids || []).includes(serviceId));

  const assignableAgents = createMemo(() =>
    agents()
      .filter((a) => !isLegacyFaceAgent(a.worker_id))
      .slice()
      .sort((a, b) => {
        const aCustom = a.kind === 'custom_yolo' || a.worker_id.startsWith('vyra/custom_detection/');
        const bCustom = b.kind === 'custom_yolo' || b.worker_id.startsWith('vyra/custom_detection/');
        if (aCustom !== bCustom) return aCustom ? 1 : -1;
        return a.name.localeCompare(b.name);
      })
  );

  const toggleAgentForService = async (agentId: string, serviceId: string, enabled: boolean) => {
    const agent = agents().find((a) => a.id === agentId);
    if (!agent) return;
    const current = new Set(agent.service_ids || []);
    if (enabled) current.add(serviceId);
    else current.delete(serviceId);
    await patchAgent(agentId, { service_ids: Array.from(current) });
    await fetchAgents();
  };

  const applyAgentToAllVisible = async () => {
    const agentId = applyAllAgentId();
    if (!agentId) return;
    const agent = agents().find((a) => a.id === agentId);
    if (!agent) return;
    const current = new Set(agent.service_ids || []);
    visibleServices().forEach((s) => current.add(s.id));
    await patchAgent(agentId, { service_ids: Array.from(current) });
    await fetchAgents();
    setBulkApplied(true);
    setTimeout(() => {
      setBulkApplied(false);
      setApplyAllAgentId('');
    }, 2000);
  };

  const openAgentPicker = (serviceId: string) => {
    setAgentPickerServiceId(serviceId);
    setShowAgentPicker(true);
  };
  const closeAgentPicker = () => {
    setShowAgentPicker(false);
    setAgentPickerServiceId(null);
  };

  const isLoading = () => nodesLoading() || agentsLoading();

  const Checkbox = (props: { checked: boolean; onChange: () => void }) => (
    <div
      class={`flex-shrink-0 w-4 h-4 rounded border-2 flex items-center justify-center cursor-pointer transition-colors ${props.checked ? 'bg-violet-600 border-violet-600' : 'border-neu-600'}`}
      onClick={props.onChange}
    >
      {props.checked && (
        <svg width="10" height="10" viewBox="0 0 10 10" fill="none">
          <path d="M2 5l2.5 2.5L8 3" stroke="white" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" />
        </svg>
      )}
    </div>
  );

  const renderCameraTile = (service: ServiceCard) => {
    const assigned = () => serviceAssignedAgents(service.id);
    return (
      <div class="self-start bg-neu-900/82 border border-neu-800/75 rounded-2xl overflow-hidden hover:border-violet-700/45 transition-colors shadow-sm">
        <div class="flex items-center justify-between px-3 py-2.5 border-b border-neu-800/60">
          <div class="min-w-0 flex-1">
            <div class="text-sm font-semibold text-neu-100 truncate">{service.name || service.id}</div>
            <div class="text-[11px] text-neu-500 truncate">{service.node_name} · {service.type}</div>
          </div>
          <div class="flex items-center gap-1 ml-2 flex-shrink-0">
            <button class="btn-small" onClick={() => openAgentPicker(service.id)} title="Assign agents">
              <FiUsers size={12} />
            </button>
            <button class="btn-small" onClick={() => setFullscreenService(service)} title="Fullscreen">
              <FiMaximize2 size={12} />
            </button>
          </div>
        </div>

        <div class="aspect-video bg-neu-950">
          <VideoTile
            nodeId={service.node_id}
            serviceId={service.id}
            showControls={false}
            autoStart={autoPlayAll()}
            showBBoxes={true}
            showDetectionCounts={true}
            displayMode="tile"
            onDblClick={() => setFullscreenService(service)}
          />
        </div>

        <div class="px-3 py-2 min-h-[36px] bg-neu-900/40">
          <Show when={assigned().length > 0} fallback={
            <span class="text-[11px] text-neu-500">No agents assigned</span>
          }>
            <div class="flex flex-wrap gap-1">
              <For each={assigned()}>
                {(a) => (
                  <span class="badge bg-violet-900/30 text-violet-300 border border-violet-800/40 text-[10px]">
                    {a.name}
                  </span>
                )}
              </For>
            </div>
          </Show>
        </div>
      </div>
    );
  };

  return (
    <div class="h-full flex flex-col overflow-hidden bg-neu-925/20">
      <div class="flex-none px-4 py-3 border-b border-neu-800/60 flex flex-wrap items-center gap-3 bg-neu-900/85 backdrop-blur-sm">
        <div class="flex items-center gap-2">
          <FiLayers size={14} class="text-neu-500 flex-shrink-0" />
          <select
            class="select text-sm h-8 py-0 leading-8 pr-7"
            value={selectedGroupId()}
            onChange={(e) => setSelectedGroupId(e.currentTarget.value)}
          >
            <option value="all">All cameras</option>
            <Show when={nodeGroups().length > 0}>
              <optgroup label="By node">
                <For each={nodeGroups()}>{(n) => <option value={`node:${n.id}`}>{n.name} ({n.count})</option>}</For>
              </optgroup>
            </Show>
          </select>
        </div>

        <div class="flex items-center gap-2">
          <FiUsers size={14} class="text-neu-500 flex-shrink-0" />
          <select
            class="select text-sm h-8 py-0 leading-8 pr-7"
            value={applyAllAgentId()}
            onChange={(e) => setApplyAllAgentId(e.currentTarget.value)}
          >
            <option value="">Bulk assign…</option>
            <For each={assignableAgents()}>
              {(a) => (
                <option value={a.id}>
                  {a.name}{a.kind === 'custom_yolo' || a.worker_id.startsWith('vyra/custom_detection/') ? ' • Custom' : ''}
                </option>
              )}
            </For>
          </select>
          <Show
            when={bulkApplied()}
            fallback={
              <button class="btn-small" onClick={applyAgentToAllVisible} disabled={!applyAllAgentId()}>
                Apply to all
              </button>
            }
          >
            <span class="btn-small text-green-400 pointer-events-none flex items-center gap-1">
              <FiCheck size={13} /> Applied!
            </span>
          </Show>
        </div>

        <label class="flex items-center gap-2 cursor-pointer select-none">
          <div
            class={`relative w-9 h-5 rounded-full transition-colors ${autoPlayAll() ? 'bg-violet-600' : 'bg-neu-700'}`}
            onClick={() => persistAutoPlay(!autoPlayAll())}
          >
            <span class={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${autoPlayAll() ? 'translate-x-4' : 'translate-x-0'}`} />
          </div>
          <FiPlay size={13} class="text-neu-500" />
          <span class="text-xs text-neu-400">Auto-play</span>
        </label>
      </div>

      <div class="flex-1 overflow-y-auto p-4 md:p-5">
        <Show when={!isLoading()} fallback={
          <div class="flex items-center justify-center h-48 gap-3 text-neu-500">
            <svg class="animate-spin w-5 h-5" viewBox="0 0 24 24" fill="none">
              <circle class="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" stroke-width="4" />
              <path class="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8v8H4z" />
            </svg>
            <span class="text-sm">Loading cameras…</span>
          </div>
        }>
          <Show when={visibleServices().length > 0} fallback={
            <div class="flex flex-col items-center justify-center h-48 gap-3 text-neu-500">
              <FiGrid size={36} class="opacity-20" />
              <div class="text-center">
                <div class="text-sm font-medium">No cameras found</div>
                <div class="text-xs text-neu-600 mt-1">Start a node and assign cameras to it</div>
              </div>
            </div>
          }>
            <div class="grid grid-cols-1 2xl:grid-cols-[minmax(0,1fr)_360px] items-start gap-4">
              <div class="content-start auto-rows-max">
                <Show
                  when={shouldGroupByNode()}
                  fallback={
                    <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 3xl:grid-cols-4 gap-3">
                      <For each={visibleServices()}>
                        {(service) => renderCameraTile(service)}
                      </For>
                    </div>
                  }
                >
                  <div class="space-y-4">
                    <For each={groupedVisibleServices()}>
                      {(group) => (
                        <section class="rounded-2xl border border-neu-800/70 bg-neu-900/35 p-3">
                          <div class="flex items-center justify-between mb-3">
                            <div class="text-sm font-semibold text-neu-200">{group.nodeName}</div>
                            <div class="text-[11px] text-neu-500">
                              {group.services.length} camera{group.services.length !== 1 ? 's' : ''}
                            </div>
                          </div>
                          <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-3 3xl:grid-cols-4 gap-3">
                            <For each={group.services}>
                              {(service) => renderCameraTile(service)}
                            </For>
                          </div>
                        </section>
                      )}
                    </For>
                  </div>
                </Show>
              </div>

              <aside class="2xl:sticky 2xl:top-2 self-start max-h-[calc(100vh-11rem)] overflow-hidden bg-neu-900/92 border border-neu-800/80 rounded-2xl p-3.5 space-y-3 shadow-lg shadow-black/25">
                <div class="flex items-center justify-between">
                  <div>
                    <div class="text-sm font-semibold text-neu-100">Live Context</div>
                    <div class="text-[11px] text-neu-400">Live identities and incidents for visible cameras</div>
                  </div>
                  <span class="badge text-[10px] bg-green-900/30 text-green-300 border border-green-800/50">Live</span>
                </div>

                <div class="border border-neu-800/80 rounded-xl p-2.5 bg-neu-950/40">
                  <div class="text-[11px] font-semibold text-neu-300 uppercase tracking-wide mb-2">Active Identities</div>
                  <Show
                    when={activeIdentities().length > 0}
                    fallback={<div class="text-[11px] text-neu-600">No recent identities in the last 30 minutes.</div>}
                  >
                    <div class="space-y-1.5 max-h-[26vh] overflow-y-auto pr-1">
                      <For each={activeIdentities()}>
                        {(p) => (
                          <div class="rounded-lg border border-neu-800 bg-neu-900/50 p-2">
                            <div class="text-xs text-neu-200 font-medium truncate">{p.label}</div>
                            <div class="text-[10px] text-neu-500 mt-0.5">
                              {p.cameras.size} camera{p.cameras.size !== 1 ? 's' : ''} • {p.count} event{p.count !== 1 ? 's' : ''}
                            </div>
                          </div>
                        )}
                      </For>
                    </div>
                  </Show>
                </div>

                <div class="border border-neu-800/80 rounded-xl p-2.5 bg-neu-950/40">
                  <div class="text-[11px] font-semibold text-neu-300 uppercase tracking-wide mb-2">
                    Incidents (last {Math.round(LIVE_INCIDENT_WINDOW_MS / 60000)} min)
                  </div>
                  <Show when={incidentEvents().length > 0} fallback={<div class="text-[11px] text-neu-600">No active incidents in this live window.</div>}>
                    <div class="space-y-1.5 max-h-[44vh] overflow-y-auto pr-1">
                      <For each={incidentEvents()}>
                        {(ev) => {
                          const sid = eventServiceId(ev);
                          const camera = serviceNameMap().get(sid) || sid || 'Unknown camera';
                          const brief = eventBrief(ev) || 'Incident detected';
                          const sev = eventSeverity(ev);
                          const sevClass =
                            sev === 'high'
                              ? 'border-red-700/60 bg-red-900/15'
                              : sev === 'medium'
                                ? 'border-amber-700/60 bg-amber-900/15'
                                : 'border-red-900/50 bg-red-900/10';
                          return (
                            <div class={`rounded-lg border p-2 ${sevClass}`}>
                              <div class="text-[11px] font-medium text-neu-150 truncate">{camera}</div>
                              <div class="text-[10px] text-neu-300 mt-0.5 line-clamp-2">{brief}</div>
                              <div class="text-[10px] text-neu-500 mt-1">{new Date(ev.created_at).toLocaleTimeString()}</div>
                            </div>
                          );
                        }}
                      </For>
                    </div>
                  </Show>
                </div>
              </aside>
            </div>
          </Show>
        </Show>
      </div>

      <Show when={fullscreenService()}>
        {(service) => (
          <div class="fixed inset-0 z-50 bg-black flex flex-col overflow-hidden">
            <div class="flex items-center justify-between px-5 py-3 bg-neu-950/95 border-b border-white/10">
              <div class="flex items-center gap-3">
                <div class="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
                <span class="text-white font-semibold text-sm">{service().name || service().id}</span>
                <span class="text-white/40 text-xs">{service().node_name}</span>
              </div>
              <button
                class="flex items-center gap-2 px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20 transition-colors"
                onClick={() => setFullscreenService(null)}
              >
                <FiX size={14} /> Close (ESC)
              </button>
            </div>
            <div class="flex-1 min-h-0 overflow-hidden relative">
              <VideoTile
                nodeId={service().node_id}
                serviceId={service().id}
                showControls={true}
                autoStart={true}
                showBBoxes={true}
                showDetectionCounts={true}
                displayMode="fullscreen"
              />
            </div>
          </div>
        )}
      </Show>

      <Show when={showAgentPicker()}>
        <div class="modal" onClick={closeAgentPicker}>
          <div class="modal-box max-w-lg" onClick={(e) => e.stopPropagation()}>
            <div class="flex items-center justify-between mb-4">
              <div>
                <h3 class="text-base font-semibold text-white">Assign Agents</h3>
                <p class="text-xs text-neu-500 mt-0.5">Toggle AI workers for this camera</p>
              </div>
              <button class="btn btn-ghost btn-sm" onClick={closeAgentPicker}><FiX size={16} /></button>
            </div>

            <div class="space-y-1 max-h-[55vh] overflow-y-auto">
              <Show when={agents().length > 0} fallback={<p class="text-neu-600 text-sm py-4 text-center">No agents configured yet</p>}>
                <For each={assignableAgents()}>
                  {(a) => {
                    const sid = agentPickerServiceId() || '';
                    const checked = () => (a.service_ids || []).includes(sid);
                    const isCustom = a.kind === 'custom_yolo' || a.worker_id.startsWith('vyra/custom_detection/');
                    return (
                      <label class="flex items-center gap-3 p-3 rounded-lg hover:bg-neu-800/50 cursor-pointer transition-colors">
                        <Checkbox checked={checked()} onChange={() => toggleAgentForService(a.id, sid, !checked())} />
                        <div class="min-w-0 flex-1">
                          <div class="text-sm font-medium text-neu-200 flex items-center gap-2">
                            <span>{a.name}</span>
                            <span class={`px-2 py-0.5 rounded-full text-[10px] border ${
                              isCustom
                                ? 'bg-blue-900/25 text-blue-300 border-blue-700/35'
                                : 'bg-violet-900/25 text-violet-300 border-violet-700/35'
                            }`}>
                              {isCustom ? 'Custom' : 'Standard'}
                            </span>
                          </div>
                          <div class="text-xs text-neu-600 truncate">{a.instruction}</div>
                        </div>
                      </label>
                    );
                  }}
                </For>
              </Show>
            </div>

            <div class="modal-action">
              <button class="btn btn-primary" onClick={closeAgentPicker}>Done</button>
            </div>
          </div>
        </div>
      </Show>
    </div>
  );
};

export default TabBirdEye;
