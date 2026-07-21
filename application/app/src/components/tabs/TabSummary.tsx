import { Component, For, Show, createMemo, createSignal, onMount } from 'solid-js';
import {
  fetchDailySummary,
  relay,
  getToken,
  type DailySummary,
  type DailySummaryIncident,
} from '../../shared';
import { FiAlertTriangle, FiCalendar, FiCamera, FiRefreshCw, FiShield, FiUser, FiX, FiZoomIn } from 'solid-icons/fi';

const localDateStr = (d = new Date()) =>
  `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;

const todayStr = () => localDateStr();

const buildSnapshotUrl = (path: string) => {
  const base = relay(`/storage/snapshots/${path}`);
  const token = getToken();
  return token ? `${base}?token=${encodeURIComponent(token)}` : base;
};

const humanizeLabel = (label: string) =>
  label.replace(/[-_]/g, ' ').replace(/\b\w/g, (c) => c.toUpperCase());

const fmtTime = (iso: string) =>
  new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });

const fmtDate = (iso: string) =>
  new Date(iso).toLocaleDateString([], { weekday: 'long', month: 'long', day: 'numeric' });

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
        <button
          class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20 transition-colors"
          onClick={(e) => { e.stopPropagation(); setScale((s) => Math.min(5, s + 0.5)); }}
        >
          + Zoom
        </button>
        <button
          class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20 transition-colors"
          onClick={(e) => { e.stopPropagation(); setScale((s) => Math.max(0.5, s - 0.5)); }}
        >
          - Zoom
        </button>
        <button
          class="px-3 py-1.5 rounded-lg bg-white/10 text-white text-sm hover:bg-white/20 transition-colors"
          onClick={(e) => { e.stopPropagation(); setScale(1); setPos({ x: 0, y: 0 }); }}
        >
          Reset
        </button>
        <button
          class="px-3 py-1.5 rounded-lg bg-red-500/80 text-white text-sm hover:bg-red-500 transition-colors"
          onClick={props.onClose}
        >
          <FiX size={14} class="inline mr-1" />Close
        </button>
      </div>
      <div class="absolute bottom-4 left-1/2 -translate-x-1/2 text-white/40 text-xs">
        Scroll to zoom · Drag image when zoomed
      </div>
      <img
        src={props.src}
        alt="Incident snapshot"
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

const IncidentCard: Component<{ inc: DailySummaryIncident; onOpenImage?: (src: string) => void }> = (props) => {
  const sev = () => props.inc.severity;
  const sevClass = () =>
    sev() === 'high'
      ? 'bg-red-500/20 text-red-300 border-red-500/35'
      : 'bg-amber-500/20 text-amber-300 border-amber-500/35';
  const sevRail = () => (sev() === 'high' ? 'bg-red-500' : 'bg-amber-500');
  const imageSrc = () => (props.inc.snapshot_path ? buildSnapshotUrl(props.inc.snapshot_path) : '');

  return (
    <article class="relative rounded-xl border border-neu-800/75 bg-neu-900/78 p-3.5 hover:border-violet-600/35 transition-colors">
      <span class={`absolute left-0 top-0 bottom-0 w-[3px] rounded-l-xl ${sevRail()}`} />
      <div class="flex gap-3 items-start">
        <Show when={props.inc.snapshot_path}>
          <button
            class="group relative w-24 h-16 rounded-lg shrink-0 bg-neu-800 border border-neu-750 overflow-hidden hover:border-violet-500/55 transition-colors"
            onClick={() => imageSrc() && props.onOpenImage?.(imageSrc())}
          >
            <img
              src={imageSrc()}
              class="w-full h-full object-cover"
              loading="lazy"
              onError={(e) => { (e.currentTarget as HTMLImageElement).parentElement!.style.display = 'none'; }}
            />
            <div class="absolute inset-0 bg-black/0 group-hover:bg-black/35 transition-colors flex items-center justify-center">
              <FiZoomIn size={14} class="text-white opacity-0 group-hover:opacity-100 transition-opacity" />
            </div>
          </button>
        </Show>

        <div class="min-w-0 flex-1">
          <div class="flex items-center gap-2 mb-1.5 flex-wrap">
            <span class={`px-2 py-0.5 rounded-full text-[10px] font-semibold uppercase border ${sevClass()}`}>
              {sev()}
            </span>
            <Show when={props.inc.person_name}>
              <span class="inline-flex items-center gap-1 px-2 py-0.5 rounded-full text-[10px] bg-violet-500/15 text-violet-300 border border-violet-500/30">
                <FiUser size={9} />
                {props.inc.person_name}
              </span>
            </Show>
            <span class="ml-auto text-[10px] text-neu-600">{fmtTime(props.inc.created_at)}</span>
          </div>

          <p class="text-xs text-neu-200 leading-relaxed">{props.inc.brief || '(No description)'}</p>

          <Show when={props.inc.service_id}>
            <p class="mt-1.5 text-[10px] text-neu-500 inline-flex items-center gap-1">
              <FiCamera size={9} />
              {props.inc.service_id}
            </p>
          </Show>
        </div>
      </div>
    </article>
  );
};

const TabSummary: Component = () => {
  const [selectedDate, setSelectedDate] = createSignal(todayStr());
  const [summary, setSummary] = createSignal<DailySummary | null>(null);
  const [loading, setLoading] = createSignal(false);
  const [error, setError] = createSignal('');
  const [viewingImage, setViewingImage] = createSignal<string | null>(null);

  const load = async (date: string) => {
    setLoading(true);
    setError('');
    try {
      const data = await fetchDailySummary(date);
      setSummary(data);
      if (!data) setError('No summary available for this date.');
    } catch {
      setError('Failed to load summary.');
    } finally {
      setLoading(false);
    }
  };

  onMount(() => load(todayStr()));

  const handleDateChange = (d: string) => {
    setSelectedDate(d);
    load(d);
  };

  const repeatOffenders = createMemo(() => {
    const s = summary();
    if (!s) return [];
    return [...s.person_sightings].sort((a, b) => b.count - a.count).slice(0, 5);
  });

  const cameraHeat = createMemo(() => {
    const s = summary();
    if (!s) return [] as Array<{ camera: string; count: number }>;
    const counts = new Map<string, number>();
    for (const inc of s.incidents) {
      const cam = String(inc.service_id || '').trim();
      if (!cam) continue;
      counts.set(cam, (counts.get(cam) || 0) + 1);
    }
    return [...counts.entries()]
      .map(([camera, count]) => ({ camera, count }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 6);
  });

  const highSeverityCount = createMemo(() => summary()?.incidents.filter((i) => i.severity === 'high').length || 0);
  const violationTotal = createMemo(() => {
    const s = summary();
    if (!s) return 0;
    return Object.values(s.violation_breakdown).reduce((a, b) => a + b, 0);
  });
  const maxViolationCount = createMemo(() => {
    const s = summary();
    if (!s) return 0;
    return Math.max(...Object.values(s.violation_breakdown), 0);
  });

  return (
    <div class="h-full flex flex-col bg-neu-925/20 overflow-hidden">
      <Show when={viewingImage()}>
        <ImageViewer src={viewingImage()!} onClose={() => setViewingImage(null)} />
      </Show>

      <div class="flex-none px-4 md:px-5 pt-4 pb-4 border-b border-neu-800/60 bg-neu-900/55">
        <div class="rounded-2xl border border-neu-800/80 bg-[linear-gradient(145deg,rgba(15,22,38,0.94),rgba(11,17,31,0.88))] p-4 md:p-5 shadow-sm">
          <div class="flex flex-col xl:flex-row xl:items-center xl:justify-between gap-4">
            <div>
              <h2 class="text-lg md:text-xl font-semibold text-neu-100 tracking-tight flex items-center gap-2">
                <FiShield size={18} class="text-violet-300" />
                Daily Brief
              </h2>
              <p class="text-xs md:text-sm text-neu-500 mt-1">
                {summary() ? fmtDate(selectedDate() + 'T00:00:00') : 'Select a date to view the summary'}
              </p>
            </div>

            <div class="flex flex-wrap items-center gap-2">
              {(['Today', 'Yesterday'] as const).map((label) => {
                const d = label === 'Today' ? todayStr() : localDateStr(new Date(Date.now() - 86400_000));
                return (
                  <button
                    class={`px-3 h-8 rounded-lg text-xs font-medium border transition-all ${
                      selectedDate() === d
                        ? 'bg-violet-600 text-white border-violet-600'
                        : 'bg-neu-900 text-neu-400 border-neu-800 hover:border-neu-600 hover:text-neu-200'
                    }`}
                    onClick={() => handleDateChange(d)}
                  >
                    {label}
                  </button>
                );
              })}

              <div class="flex items-center gap-1.5 px-2.5 h-8 rounded-lg bg-neu-900 border border-neu-800 focus-within:border-violet-500/50 transition-colors">
                <FiCalendar size={13} class="text-neu-600" />
                <input
                  type="date"
                  class="bg-transparent text-xs text-neu-200 outline-none"
                  value={selectedDate()}
                  max={todayStr()}
                  onInput={(e) => handleDateChange(e.currentTarget.value)}
                />
              </div>

              <button
                class="btn-secondary h-8 px-3"
                onClick={() => load(selectedDate())}
                disabled={loading()}
              >
                <FiRefreshCw size={13} class={loading() ? 'animate-spin' : ''} />
                {loading() ? 'Loading...' : 'Refresh'}
              </button>
            </div>
          </div>

          <Show when={summary()}>
            {(s) => (
              <div class="mt-4 grid grid-cols-2 sm:grid-cols-4 gap-2.5">
                <div class="kpi-card-premium">
                  <div class="text-[10px] uppercase tracking-[0.08em] text-neu-500">Events</div>
                  <div class="kpi-value text-xl font-semibold mt-1">{s().event_count}</div>
                </div>
                <div class="kpi-card-premium">
                  <div class="text-[10px] uppercase tracking-[0.08em] text-neu-500">High Severity</div>
                  <div class="kpi-value text-xl font-semibold mt-1 text-red-300">{highSeverityCount()}</div>
                </div>
                <div class="kpi-card-premium">
                  <div class="text-[10px] uppercase tracking-[0.08em] text-neu-500">Known Persons</div>
                  <div class="kpi-value text-xl font-semibold mt-1 text-violet-200">{s().person_sightings.length}</div>
                </div>
                <div class="kpi-card-premium">
                  <div class="text-[10px] uppercase tracking-[0.08em] text-neu-500">Violations</div>
                  <div class="kpi-value text-xl font-semibold mt-1 text-amber-300">{violationTotal()}</div>
                </div>
              </div>
            )}
          </Show>
        </div>
      </div>

      <div class="flex-1 overflow-y-auto px-4 md:px-5 pb-5">
        <Show when={loading()}>
          <div class="h-full min-h-[260px] flex items-center justify-center">
            <div class="rounded-2xl border border-neu-800/80 bg-neu-900/70 p-5 w-full max-w-xl">
              <div class="skeleton h-4 w-36" />
              <div class="skeleton h-3 w-4/5 mt-3" />
              <div class="skeleton h-20 w-full mt-4" />
            </div>
          </div>
        </Show>

        <Show when={!loading() && error()}>
          <div class="h-full min-h-[260px] flex items-center justify-center">
            <div class="saas-empty w-full max-w-2xl">
              <FiShield class="mx-auto mb-3 w-9 h-9" />
              <p class="text-neu-300 font-medium">{error()}</p>
              <p class="text-xs mt-1">Summaries are generated from recorded events at regular intervals.</p>
            </div>
          </div>
        </Show>

        <Show when={!loading() && summary()}>
          {(s) => (
            <div class="max-w-6xl mx-auto pt-4 space-y-4">
              <div class="grid grid-cols-1 xl:grid-cols-2 gap-4">
                <section class="rounded-2xl border border-neu-800/80 bg-neu-900/75 p-4 md:p-5">
                  <div class="flex items-center justify-between gap-2 mb-3">
                    <h3 class="text-sm font-semibold text-neu-100 flex items-center gap-2">
                      <FiUser size={14} class="text-violet-300" />
                      Known Persons
                    </h3>
                    <span class="text-[10px] text-neu-500">{s().person_sightings.length} tracked</span>
                  </div>
                  <Show
                    when={s().person_sightings.length > 0}
                    fallback={<p class="text-xs text-neu-500">No known persons detected for this date.</p>}
                  >
                    <div class="grid grid-cols-1 sm:grid-cols-2 gap-2.5">
                      <For each={s().person_sightings.slice(0, 8)}>
                        {(p) => (
                          <div class="rounded-xl border border-neu-800/80 bg-neu-950/55 px-3 py-2.5">
                            <div class="flex items-center gap-2">
                              <div class="w-8 h-8 rounded-full bg-violet-600/85 text-white text-xs font-semibold flex items-center justify-center shrink-0">
                                {p.person_name.charAt(0).toUpperCase()}
                              </div>
                              <div class="min-w-0">
                                <p class="text-sm text-neu-100 font-medium truncate">{p.person_name}</p>
                                <p class="text-[10px] text-neu-500">Seen {p.count}x</p>
                              </div>
                            </div>
                            <p class="text-[10px] text-neu-600 mt-1.5">First {fmtTime(p.first_seen)} · Last {fmtTime(p.last_seen)}</p>
                          </div>
                        )}
                      </For>
                    </div>
                  </Show>
                </section>

                <section class="rounded-2xl border border-neu-800/80 bg-neu-900/75 p-4 md:p-5">
                  <h3 class="text-sm font-semibold text-neu-100 mb-3">Hotspots</h3>
                  <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
                    <div>
                      <p class="text-[11px] uppercase tracking-[0.08em] text-neu-500 mb-2">Repeat Offenders</p>
                      <Show when={repeatOffenders().length > 0} fallback={<p class="text-xs text-neu-500">No repeat-offender data.</p>}>
                        <div class="space-y-2">
                          <For each={repeatOffenders()}>
                            {(p) => (
                              <div class="rounded-lg border border-neu-800/80 bg-neu-950/55 px-3 py-2 flex items-center justify-between gap-2">
                                <div class="min-w-0">
                                  <p class="text-sm text-neu-200 truncate">{p.person_name}</p>
                                  <p class="text-[10px] text-neu-600">{p.cameras && p.cameras.length ? `${p.cameras.length} camera(s)` : 'camera unknown'}</p>
                                </div>
                                <span class="text-[11px] font-semibold text-amber-300 bg-amber-500/15 border border-amber-500/30 rounded-full px-2 py-0.5">{p.count}</span>
                              </div>
                            )}
                          </For>
                        </div>
                      </Show>
                    </div>

                    <div>
                      <p class="text-[11px] uppercase tracking-[0.08em] text-neu-500 mb-2">Camera Heat</p>
                      <Show when={cameraHeat().length > 0} fallback={<p class="text-xs text-neu-500">No camera-heat data.</p>}>
                        <div class="space-y-2">
                          <For each={cameraHeat()}>
                            {(c) => (
                              <div class="rounded-lg border border-neu-800/80 bg-neu-950/55 px-3 py-2 flex items-center justify-between gap-2">
                                <p class="text-sm text-neu-200 truncate">{c.camera}</p>
                                <span class="text-[11px] font-semibold text-red-300 bg-red-500/15 border border-red-500/30 rounded-full px-2 py-0.5">{c.count}</span>
                              </div>
                            )}
                          </For>
                        </div>
                      </Show>
                    </div>
                  </div>
                </section>
              </div>

              <Show when={s().incidents.length > 0}>
                <section class="rounded-2xl border border-neu-800/80 bg-neu-900/75 p-4 md:p-5">
                  <h3 class="text-sm font-semibold text-neu-100 flex items-center gap-2 mb-3">
                    <FiAlertTriangle size={14} class="text-amber-300" />
                    Key Incidents
                    <span class="ml-auto text-[10px] text-neu-500">{s().incidents.length} total</span>
                  </h3>
                  <div class="space-y-2.5">
                    <For each={s().incidents}>
                      {(inc) => <IncidentCard inc={inc} onOpenImage={(src) => setViewingImage(src)} />}
                    </For>
                  </div>
                </section>
              </Show>

              <Show when={Object.keys(s().violation_breakdown).length > 0}>
                <section class="rounded-2xl border border-neu-800/80 bg-neu-900/75 p-4 md:p-5">
                  <h3 class="text-sm font-semibold text-neu-100 mb-3">Violation Breakdown</h3>
                  <div class="space-y-2">
                    <For each={Object.entries(s().violation_breakdown).sort((a, b) => b[1] - a[1])}>
                      {([label, count]) => (
                        <div class="rounded-lg border border-neu-800/80 bg-neu-950/55 px-3 py-2">
                          <div class="flex items-center justify-between gap-2 mb-1">
                            <span class="text-xs text-neu-200">{humanizeLabel(label)}</span>
                            <span class="text-[11px] text-neu-300 font-semibold">{count}</span>
                          </div>
                          <div class="h-1.5 rounded-full bg-neu-800 overflow-hidden">
                            <div
                              class="h-full rounded-full bg-gradient-to-r from-red-500 to-amber-500 transition-all duration-500"
                              style={{ width: `${Math.max(6, maxViolationCount() > 0 ? (count / maxViolationCount()) * 100 : 0)}%` }}
                            />
                          </div>
                        </div>
                      )}
                    </For>
                  </div>
                </section>
              </Show>

              <Show when={s().updated_at}>
                <p class="text-[11px] text-neu-600 text-center pb-2">
                  Summary last updated {new Date(s().updated_at!).toLocaleString([], { hour: '2-digit', minute: '2-digit', month: 'short', day: 'numeric' })}
                </p>
              </Show>
            </div>
          )}
        </Show>
      </div>
    </div>
  );
};

export default TabSummary;
