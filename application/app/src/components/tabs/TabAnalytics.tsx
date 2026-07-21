import {
  Component,
  For,
  Show,
  createEffect,
  createMemo,
  createSignal,
  onCleanup,
  onMount,
} from 'solid-js';
import {
  ArcElement,
  BarController,
  BarElement,
  CategoryScale,
  Chart,
  DoughnutController,
  Filler,
  Legend,
  LineController,
  LineElement,
  LinearScale,
  PointElement,
  Tooltip,
} from 'chart.js';
import {
  agents,
  connectAgentEventsWebSocket,
  fetchAgents,
  fetchNodes,
  fetchRecentAgentEventsRest,
  nodes,
  subscribeToAgentEvents,
  type AgentEvent,
} from '../../shared';
import {
  FiActivity,
  FiAlertTriangle,
  FiCamera,
  FiFilter,
  FiRefreshCw,
  FiShield,
  FiUsers,
} from 'solid-icons/fi';
import CountUpNumber from '../motion/CountUpNumber';
import RevealOnScroll from '../motion/RevealOnScroll';

Chart.register(
  LineController,
  BarController,
  DoughnutController,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  BarElement,
  ArcElement,
  Tooltip,
  Legend,
  Filler,
);

const ANALYTICS_PAGE_SIZE = 1000;

const C = {
  red: 'rgba(239, 68, 68, 0.9)',
  amber: 'rgba(245, 158, 11, 0.9)',
  green: 'rgba(34, 197, 94, 0.9)',
  blue: 'rgba(59, 130, 246, 0.9)',
  cyan: 'rgba(93, 245, 255, 0.94)',
  violet: 'rgba(37, 99, 235, 0.9)',
  gray: 'rgba(115, 115, 115, 0.78)',
  grid: 'rgba(255,255,255,0.06)',
  text: '#a3a3a3',
  bg: '#171717',
  border: '#404040',
};

const TOOLTIP_OPTS = {
  backgroundColor: C.bg,
  titleColor: '#e5e5e5',
  bodyColor: C.text,
  borderColor: C.border,
  borderWidth: 1,
  padding: 10,
  cornerRadius: 8,
};

const SCALE_OPTS = {
  x: { ticks: { color: C.text, font: { size: 11 } }, grid: { color: C.grid } },
  y: { ticks: { color: C.text, font: { size: 11 } }, grid: { color: C.grid }, beginAtZero: true },
};

const LEGEND_BOTTOM = {
  position: 'bottom' as const,
  labels: { color: C.text, font: { size: 12 }, boxWidth: 10, padding: 14 },
};

type AnalyticsMode = 'total' | 'daywise';
type RangePreset = 'today' | 'yesterday' | '7d' | '30d' | 'all' | 'custom';
type PersonFilter = 'all' | 'known' | 'unknown';
type SnapshotFilter = 'all' | 'with' | 'without';
type ViolationFilter = 'all' | 'violations' | 'non_violations';

type DateRange = { from: Date | null; to: Date | null };

type DayRow = {
  dateKey: string;
  events: number;
  high: number;
  medium: number;
  violations: number;
  cameras: number;
  knownPersons: number;
  unknownSightings: number;
};

const pad2 = (n: number) => String(n).padStart(2, '0');

function localDateKey(input: string | Date): string {
  const d = input instanceof Date ? input : new Date(input);
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`;
}

function localDateLabel(dateKey: string): string {
  const [y, m, d] = dateKey.split('-').map((v) => Number(v));
  if (!y || !m || !d) return dateKey;
  const dt = new Date(y, m - 1, d);
  return dt.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
}

function startOfDay(date: Date): Date {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate(), 0, 0, 0, 0);
}

function endOfDay(date: Date): Date {
  return new Date(date.getFullYear(), date.getMonth(), date.getDate(), 23, 59, 59, 999);
}

function toNum(v: unknown): number {
  if (typeof v === 'number' && Number.isFinite(v)) return v;
  if (typeof v === 'string') {
    const n = Number(v);
    return Number.isFinite(n) ? n : 0;
  }
  return 0;
}

function normalizeString(v: unknown): string {
  if (typeof v !== 'string') return '';
  return v.trim();
}

function stringField(map: Record<string, unknown>, keys: string[]): string {
  for (const k of keys) {
    const value = normalizeString(map[k]);
    if (value) return value;
  }
  return '';
}

function arrayField(map: Record<string, unknown>, key: string): string[] {
  const raw = map[key];
  if (!Array.isArray(raw)) return [];
  return raw.map((v) => normalizeString(v)).filter(Boolean);
}

function eventMeta(ev: AgentEvent): Record<string, unknown> {
  return (ev.metadata || {}) as Record<string, unknown>;
}

function eventData(ev: AgentEvent): Record<string, unknown> {
  return (ev.data || {}) as Record<string, unknown>;
}

function eventServiceId(ev: AgentEvent): string {
  const m = eventMeta(ev);
  const d = eventData(ev);
  return (
    stringField(m, ['service_id', 'serviceId', 'service']) ||
    stringField(d, ['service_id', 'serviceId', 'service']) ||
    (ev.service_ids && ev.service_ids[0]) ||
    ''
  );
}

function eventSeverity(ev: AgentEvent): 'high' | 'medium' | 'low' | 'none' {
  const m = eventMeta(ev);
  const d = eventData(ev);
  const raw = (
    stringField(m, ['severity']) ||
    stringField(d, ['severity'])
  ).toLowerCase();

  if (raw === 'high') return 'high';
  if (raw === 'medium') return 'medium';
  if (raw === 'low') return 'low';
  return 'none';
}

function eventKind(ev: AgentEvent): string {
  const m = eventMeta(ev);
  const d = eventData(ev);
  return (
    stringField(m, ['event_kind', 'agent_type', 'type']) ||
    stringField(d, ['event_kind', 'agent_type', 'type']) ||
    'general'
  ).toLowerCase();
}

function eventSnapshotPath(ev: AgentEvent): string {
  const m = eventMeta(ev);
  const d = eventData(ev);
  return stringField(m, ['snapshot_path']) || stringField(d, ['snapshot_path']);
}

function eventPersonName(ev: AgentEvent): string {
  const m = eventMeta(ev);
  const d = eventData(ev);
  return stringField(m, ['person_name', 'name']) || stringField(d, ['person_name', 'name']);
}

function eventGlobalPersonId(ev: AgentEvent): string {
  const m = eventMeta(ev);
  const d = eventData(ev);
  return stringField(m, ['global_person_id']) || stringField(d, ['global_person_id']);
}

function eventViolationCount(ev: AgentEvent): number {
  const m = eventMeta(ev);
  const d = eventData(ev);
  return toNum(m.violation_count ?? d.violation_count ?? 0);
}

function eventHasViolations(ev: AgentEvent): boolean {
  const m = eventMeta(ev);
  const d = eventData(ev);
  const labels = arrayField(m, 'violation_labels').length + arrayField(d, 'violation_labels').length;
  const hasFlag = m.has_violations === true || d.has_violations === true || m.has_violations === 1 || d.has_violations === 1;
  return eventViolationCount(ev) > 0 || labels > 0 || hasFlag;
}

function eventViolationLabels(ev: AgentEvent): string[] {
  const m = eventMeta(ev);
  const d = eventData(ev);
  const out = [...arrayField(m, 'violation_labels'), ...arrayField(d, 'violation_labels')];
  return out.map((v) => v.replace(/_/g, ' ').replace(/-/g, ' ').trim()).filter(Boolean);
}

function eventSummary(ev: AgentEvent): string {
  const m = eventMeta(ev);
  const d = eventData(ev);
  return (
    stringField(d, ['vlm_summary', 'summary', 'event_brief', 'message', 'answer', 'result']) ||
    stringField(m, ['vlm_summary', 'summary', 'event_brief', 'message', 'answer', 'result'])
  );
}

function isKnownPersonName(name: string): boolean {
  const v = name.trim().toLowerCase();
  if (!v) return false;
  return !['unknown', 'unidentified', 'na', 'n/a', 'none'].includes(v);
}

function isPersonEvent(ev: AgentEvent): boolean {
  const name = eventPersonName(ev);
  const gid = eventGlobalPersonId(ev);
  const kind = eventKind(ev);
  if (name || gid) return true;
  return kind.includes('person') || kind.includes('visit') || kind.includes('identity');
}

function dedupeEvents(events: AgentEvent[]): AgentEvent[] {
  const seen = new Set<string>();
  const out: AgentEvent[] = [];
  for (const ev of events) {
    if (!ev?.id || seen.has(ev.id)) continue;
    seen.add(ev.id);
    out.push(ev);
  }
  return out;
}

function enumerateDays(from: Date, to: Date): string[] {
  const start = startOfDay(from);
  const end = startOfDay(to);
  if (start > end) return [];

  const out: string[] = [];
  const cursor = new Date(start);
  while (cursor <= end) {
    out.push(localDateKey(cursor));
    cursor.setDate(cursor.getDate() + 1);
    if (out.length > 3700) break;
  }
  return out;
}

function topMapPairs(map: Map<string, number>, top: number): [string, number][] {
  return [...map.entries()].sort((a, b) => b[1] - a[1]).slice(0, top);
}

function setArr(arr: any[], vals: any[]) {
  arr.length = 0;
  arr.push(...vals);
}

function hourHistogram(events: AgentEvent[]) {
  const labels = Array.from({ length: 24 }, (_, h) => `${pad2(h)}:00`);
  const counts = new Array(24).fill(0);
  for (const ev of events) {
    const h = new Date(ev.created_at).getHours();
    if (h >= 0 && h < 24) counts[h] += 1;
  }
  return { labels, counts };
}

function daySeries(events: AgentEvent[], range: DateRange) {
  if (events.length === 0 && !range.from && !range.to) {
    return { keys: [] as string[], labels: [] as string[], counts: [] as number[] };
  }

  let from = range.from;
  let to = range.to;

  if (!from || !to) {
    const times = events.map((e) => new Date(e.created_at).getTime());
    const minTs = times.length ? Math.min(...times) : Date.now();
    const maxTs = times.length ? Math.max(...times) : Date.now();
    from = from || new Date(minTs);
    to = to || new Date(maxTs);
  }

  if (!from || !to) {
    return { keys: [] as string[], labels: [] as string[], counts: [] as number[] };
  }

  const keys = enumerateDays(from, to);
  const byDay = new Map<string, number>();
  for (const ev of events) {
    const key = localDateKey(ev.created_at);
    byDay.set(key, (byDay.get(key) || 0) + 1);
  }

  return {
    keys,
    labels: keys.map(localDateLabel),
    counts: keys.map((k) => byDay.get(k) || 0),
  };
}

function severityDistribution(events: AgentEvent[]) {
  let high = 0;
  let medium = 0;
  let low = 0;
  let none = 0;
  for (const ev of events) {
    const sev = eventSeverity(ev);
    if (sev === 'high') high += 1;
    else if (sev === 'medium') medium += 1;
    else if (sev === 'low') low += 1;
    else none += 1;
  }
  return [high, medium, low, none] as [number, number, number, number];
}

function violationCompliance(events: AgentEvent[]) {
  let violations = 0;
  let compliant = 0;
  for (const ev of events) {
    if (eventHasViolations(ev)) {
      violations += 1;
    } else {
      compliant += 1;
    }
  }
  return [violations, compliant] as [number, number];
}

function topViolationTypes(events: AgentEvent[], top = 10): [string, number][] {
  const counts = new Map<string, number>();
  for (const ev of events) {
    const labels = eventViolationLabels(ev);
    if (labels.length === 0 && eventHasViolations(ev)) {
      const fallback = eventKind(ev).replace(/_/g, ' ');
      counts.set(fallback, (counts.get(fallback) || 0) + 1);
      continue;
    }
    for (const label of labels) {
      counts.set(label, (counts.get(label) || 0) + 1);
    }
  }
  return topMapPairs(counts, top);
}

function cameraCounts(events: AgentEvent[], cameraMap: Map<string, string>, top = 12): [string, number][] {
  const counts = new Map<string, number>();
  for (const ev of events) {
    const sid = eventServiceId(ev);
    if (!sid) continue;
    const name = cameraMap.get(sid) || sid.slice(0, 14);
    counts.set(name, (counts.get(name) || 0) + 1);
  }
  return topMapPairs(counts, top);
}

function agentCounts(events: AgentEvent[], agentMap: Map<string, string>, top = 12): [string, number][] {
  const counts = new Map<string, number>();
  for (const ev of events) {
    const id = ev.agent_id;
    const name = agentMap.get(id) || ev.agent_name || id.slice(0, 14);
    counts.set(name, (counts.get(name) || 0) + 1);
  }
  return topMapPairs(counts, top);
}

function personKnownUnknown(events: AgentEvent[]) {
  let known = 0;
  let unknown = 0;
  for (const ev of events) {
    if (!isPersonEvent(ev)) continue;
    const name = eventPersonName(ev);
    if (isKnownPersonName(name)) known += 1;
    else unknown += 1;
  }
  return [known, unknown] as [number, number];
}

function severityByDay(events: AgentEvent[], range: DateRange) {
  const series = daySeries(events, range);
  if (series.labels.length === 0) {
    return { labels: [] as string[], high: [] as number[], medium: [] as number[], low: [] as number[] };
  }

  const keyByLabel = new Map<string, string>();
  series.keys.forEach((k) => keyByLabel.set(localDateLabel(k), k));

  const byDay = new Map<string, { high: number; medium: number; low: number }>();
  for (const ev of events) {
    const key = localDateKey(ev.created_at);
    if (!byDay.has(key)) byDay.set(key, { high: 0, medium: 0, low: 0 });
    const sev = eventSeverity(ev);
    const bucket = byDay.get(key)!;
    if (sev === 'high') bucket.high += 1;
    else if (sev === 'medium') bucket.medium += 1;
    else if (sev === 'low') bucket.low += 1;
  }

  return {
    labels: series.labels,
    high: series.labels.map((label) => byDay.get(keyByLabel.get(label) || '')?.high || 0),
    medium: series.labels.map((label) => byDay.get(keyByLabel.get(label) || '')?.medium || 0),
    low: series.labels.map((label) => byDay.get(keyByLabel.get(label) || '')?.low || 0),
  };
}

function dayWiseRows(events: AgentEvent[], range: DateRange): DayRow[] {
  const byDay = new Map<string, DayRow>();
  const perDayEvents = new Map<string, AgentEvent[]>();
  for (const ev of events) {
    const key = localDateKey(ev.created_at);
    if (!byDay.has(key)) {
      byDay.set(key, {
        dateKey: key,
        events: 0,
        high: 0,
        medium: 0,
        violations: 0,
        cameras: 0,
        knownPersons: 0,
        unknownSightings: 0,
      });
    }
    if (!perDayEvents.has(key)) perDayEvents.set(key, []);
    perDayEvents.get(key)!.push(ev);

    const row = byDay.get(key)!;
    row.events += 1;

    const sev = eventSeverity(ev);
    if (sev === 'high') row.high += 1;
    if (sev === 'medium') row.medium += 1;
    if (eventHasViolations(ev)) row.violations += 1;
  }

  for (const [dateKey, row] of byDay.entries()) {
    const dayEvents = perDayEvents.get(dateKey) || [];
    const cameraSet = new Set<string>();
    const knownSet = new Set<string>();
    let unknown = 0;

    for (const ev of dayEvents) {
      const sid = eventServiceId(ev);
      if (sid) cameraSet.add(sid);

      if (isPersonEvent(ev)) {
        const name = eventPersonName(ev);
        if (isKnownPersonName(name)) {
          knownSet.add(name.toLowerCase());
        } else {
          unknown += 1;
        }
      }
    }

    row.cameras = cameraSet.size;
    row.knownPersons = knownSet.size;
    row.unknownSightings = unknown;
  }

  if (range.from && range.to) {
    for (const key of enumerateDays(range.from, range.to)) {
      if (!byDay.has(key)) {
        byDay.set(key, {
          dateKey: key,
          events: 0,
          high: 0,
          medium: 0,
          violations: 0,
          cameras: 0,
          knownPersons: 0,
          unknownSightings: 0,
        });
      }
    }
  }

  return [...byDay.values()].sort((a, b) => (a.dateKey > b.dateKey ? -1 : 1));
}

const KpiCard: Component<{
  label: string;
  value: string | number;
  sub?: string;
  accent?: 'violet' | 'red' | 'green' | 'amber';
  icon: any;
}> = (props) => {
  const cls = {
    violet: 'text-violet-400 bg-violet-900/20',
    red: 'text-red-400 bg-red-900/20',
    green: 'text-green-400 bg-green-900/20',
    amber: 'text-amber-400 bg-amber-900/20',
  }[props.accent || 'violet'];

  return (
    <div class="kpi-card-premium lift-on-hover">
      <div class="flex items-start justify-between">
        <span class="stat-card-label">{props.label}</span>
        <span class={`p-2 rounded-lg ${cls}`}><props.icon size={16} /></span>
      </div>
      <span class="stat-card-value">
        {typeof props.value === 'number' ? <CountUpNumber value={props.value} durationMs={520} /> : props.value}
      </span>
      <Show when={props.sub}><span class="stat-card-sub">{props.sub}</span></Show>
    </div>
  );
};

const TabAnalytics: Component = () => {
  const [events, setEvents] = createSignal<AgentEvent[]>([]);
  const [loading, setLoading] = createSignal(true);
  const [lastRefresh, setLastRefresh] = createSignal(new Date());

  const [mode, setMode] = createSignal<AnalyticsMode>('total');
  const [rangePreset, setRangePreset] = createSignal<RangePreset>('7d');
  const [dateFrom, setDateFrom] = createSignal('');
  const [dateTo, setDateTo] = createSignal('');

  const [cameraFilter, setCameraFilter] = createSignal('all');
  const [agentFilter, setAgentFilter] = createSignal('all');
  const [severityFilter, setSeverityFilter] = createSignal<'all' | 'high' | 'medium' | 'low' | 'none'>('all');
  const [kindFilter, setKindFilter] = createSignal('all');
  const [personFilter, setPersonFilter] = createSignal<PersonFilter>('all');
  const [snapshotFilter, setSnapshotFilter] = createSignal<SnapshotFilter>('all');
  const [violationFilter, setViolationFilter] = createSignal<ViolationFilter>('all');
  const [textFilter, setTextFilter] = createSignal('');

  let trendChart: Chart | null = null;
  let severityChart: Chart | null = null;
  let complianceChart: Chart | null = null;
  let violationChart: Chart | null = null;
  let cameraChart: Chart | null = null;
  let agentChart: Chart | null = null;
  let personMixChart: Chart | null = null;
  let daySeverityChart: Chart | null = null;

  const cameraNameMap = createMemo<Map<string, string>>(() => {
    const map = new Map<string, string>();
    for (const node of nodes()) {
      for (const service of node.services || []) {
        map.set(service.id, service.name || service.id.slice(0, 14));
      }
    }
    for (const ev of events()) {
      const sid = eventServiceId(ev);
      if (sid && !map.has(sid)) map.set(sid, sid.slice(0, 14));
    }
    return map;
  });

  const agentNameMap = createMemo<Map<string, string>>(() => {
    const map = new Map<string, string>();
    for (const a of agents()) map.set(a.id, a.name || a.id.slice(0, 14));
    for (const ev of events()) {
      if (!map.has(ev.agent_id)) map.set(ev.agent_id, ev.agent_name || ev.agent_id.slice(0, 14));
    }
    return map;
  });

  const cameraOptions = createMemo(() => {
    return [...cameraNameMap().entries()]
      .map(([id, name]) => ({ id, name }))
      .sort((a, b) => a.name.localeCompare(b.name));
  });

  const agentOptions = createMemo(() => {
    return [...agentNameMap().entries()]
      .map(([id, name]) => ({ id, name }))
      .sort((a, b) => a.name.localeCompare(b.name));
  });

  const kindOptions = createMemo(() => {
    const set = new Set<string>();
    for (const ev of events()) set.add(eventKind(ev));
    return [...set].sort();
  });

  const resolvedRange = createMemo<DateRange>(() => {
    const now = new Date();
    const preset = rangePreset();

    if (preset === 'all') return { from: null, to: null };
    if (preset === 'today') return { from: startOfDay(now), to: now };

    if (preset === 'yesterday') {
      const y = new Date(now);
      y.setDate(y.getDate() - 1);
      return { from: startOfDay(y), to: endOfDay(y) };
    }

    if (preset === '7d') {
      const from = startOfDay(new Date(now.getFullYear(), now.getMonth(), now.getDate() - 6));
      return { from, to: now };
    }

    if (preset === '30d') {
      const from = startOfDay(new Date(now.getFullYear(), now.getMonth(), now.getDate() - 29));
      return { from, to: now };
    }

    const from = dateFrom() ? startOfDay(new Date(`${dateFrom()}T00:00:00`)) : null;
    const to = dateTo() ? endOfDay(new Date(`${dateTo()}T00:00:00`)) : null;
    return { from, to };
  });

  const filteredEvents = createMemo<AgentEvent[]>(() => {
    const sid = cameraFilter();
    const aid = agentFilter();
    const sev = severityFilter();
    const kind = kindFilter();
    const person = personFilter();
    const snap = snapshotFilter();
    const vio = violationFilter();
    const q = textFilter().trim().toLowerCase();
    const range = resolvedRange();
    const camMap = cameraNameMap();

    return events().filter((ev) => {
      const createdAt = new Date(ev.created_at);
      if (range.from && createdAt < range.from) return false;
      if (range.to && createdAt > range.to) return false;

      const eventSid = eventServiceId(ev);
      if (sid !== 'all' && eventSid !== sid) return false;

      if (aid !== 'all' && ev.agent_id !== aid) return false;

      const eventSev = eventSeverity(ev);
      if (sev !== 'all' && eventSev !== sev) return false;

      const eventKindKey = eventKind(ev);
      if (kind !== 'all' && eventKindKey !== kind) return false;

      if (person === 'known') {
        if (!isKnownPersonName(eventPersonName(ev))) return false;
      } else if (person === 'unknown') {
        if (!isPersonEvent(ev) || isKnownPersonName(eventPersonName(ev))) return false;
      }

      const hasSnap = !!eventSnapshotPath(ev);
      if (snap === 'with' && !hasSnap) return false;
      if (snap === 'without' && hasSnap) return false;

      const hasVio = eventHasViolations(ev);
      if (vio === 'violations' && !hasVio) return false;
      if (vio === 'non_violations' && hasVio) return false;

      if (q) {
        const violations = eventViolationLabels(ev).join(' ');
        const summary = eventSummary(ev);
        const camera = camMap.get(eventSid) || eventSid;
        const name = eventPersonName(ev);
        const haystack = `${ev.agent_name} ${ev.agent_id} ${summary} ${name} ${camera} ${eventKindKey} ${violations}`.toLowerCase();
        if (!haystack.includes(q)) return false;
      }

      return true;
    });
  });

  const trendData = createMemo(() => {
    const evs = filteredEvents();
    if (mode() === 'daywise') return daySeries(evs, resolvedRange());
    return hourHistogram(evs);
  });

  const severityData = createMemo(() => severityDistribution(filteredEvents()));
  const complianceData = createMemo(() => violationCompliance(filteredEvents()));
  const topViolationData = createMemo(() => topViolationTypes(filteredEvents(), 10));
  const cameraData = createMemo(() => cameraCounts(filteredEvents(), cameraNameMap(), 12));
  const agentData = createMemo(() => agentCounts(filteredEvents(), agentNameMap(), 12));
  const personMixData = createMemo(() => personKnownUnknown(filteredEvents()));
  const daySeverityData = createMemo(() => severityByDay(filteredEvents(), resolvedRange()));
  const dayRows = createMemo(() => dayWiseRows(filteredEvents(), resolvedRange()));

  const recentHighAlerts = createMemo(() =>
    filteredEvents()
      .filter((ev) => eventSeverity(ev) === 'high')
      .slice(0, 20)
  );

  const activeFilters = createMemo(() => {
    let n = 0;
    if (cameraFilter() !== 'all') n += 1;
    if (agentFilter() !== 'all') n += 1;
    if (severityFilter() !== 'all') n += 1;
    if (kindFilter() !== 'all') n += 1;
    if (personFilter() !== 'all') n += 1;
    if (snapshotFilter() !== 'all') n += 1;
    if (violationFilter() !== 'all') n += 1;
    if (textFilter().trim()) n += 1;
    if (rangePreset() !== '7d') n += 1;
    return n;
  });

  const kpiFiltered = createMemo(() => filteredEvents().length);
  const kpiViolationEvents = createMemo(() => filteredEvents().filter(eventHasViolations).length);
  const kpiViolationRate = createMemo(() => {
    const total = filteredEvents().length;
    if (!total) return '0.0%';
    return `${((kpiViolationEvents() / total) * 100).toFixed(1)}%`;
  });

  const kpiHigh = createMemo(() => filteredEvents().filter((ev) => eventSeverity(ev) === 'high').length);
  const kpiHighRate = createMemo(() => {
    const total = filteredEvents().length;
    if (!total) return '0.0%';
    return `${((kpiHigh() / total) * 100).toFixed(1)}%`;
  });
  const kpiUniqueCameras = createMemo(() => {
    const set = new Set<string>();
    for (const ev of filteredEvents()) {
      const sid = eventServiceId(ev);
      if (sid) set.add(sid);
    }
    return set.size;
  });

  const kpiUniqueAgents = createMemo(() => {
    const set = new Set<string>();
    for (const ev of filteredEvents()) set.add(ev.agent_id);
    return set.size;
  });

  const kpiKnownPersons = createMemo(() => {
    const set = new Set<string>();
    for (const ev of filteredEvents()) {
      const name = eventPersonName(ev);
      if (isKnownPersonName(name)) set.add(name.toLowerCase());
    }
    return set.size;
  });

  const kpiSnapshotRate = createMemo(() => {
    const evs = filteredEvents();
    if (!evs.length) return '0.0%';
    const withSnapshot = evs.filter((ev) => !!eventSnapshotPath(ev)).length;
    return `${((withSnapshot / evs.length) * 100).toFixed(1)}%`;
  });

  const kpiDailyAvg = createMemo(() => {
    const rows = dayRows();
    if (!rows.length) return '0.0';
    const total = rows.reduce((sum, row) => sum + row.events, 0);
    return (total / rows.length).toFixed(1);
  });

  const clearFilters = () => {
    setRangePreset('7d');
    setDateFrom('');
    setDateTo('');
    setCameraFilter('all');
    setAgentFilter('all');
    setSeverityFilter('all');
    setKindFilter('all');
    setPersonFilter('all');
    setSnapshotFilter('all');
    setViolationFilter('all');
    setTextFilter('');
  };

  const loadEvents = async () => {
    setLoading(true);
    try {
      let offset = 0;
      const out: AgentEvent[] = [];

      while (true) {
        const chunk = await fetchRecentAgentEventsRest({
          limit: ANALYTICS_PAGE_SIZE,
          offset,
        });

        if (chunk.length === 0) break;
        out.push(...chunk);

        if (chunk.length < ANALYTICS_PAGE_SIZE) break;
        offset += chunk.length;
      }

      setEvents(dedupeEvents(out));
      setLastRefresh(new Date());
    } finally {
      setLoading(false);
    }
  };

  const destroyCharts = () => {
    [
      trendChart,
      severityChart,
      complianceChart,
      violationChart,
      cameraChart,
      agentChart,
      personMixChart,
      daySeverityChart,
    ].forEach((c) => c?.destroy());

    trendChart = null;
    severityChart = null;
    complianceChart = null;
    violationChart = null;
    cameraChart = null;
    agentChart = null;
    personMixChart = null;
    daySeverityChart = null;
  };

  function getCtx(id: string): CanvasRenderingContext2D | null {
    return (document.getElementById(id) as HTMLCanvasElement | null)?.getContext('2d') || null;
  }

  const initCharts = () => {
    destroyCharts();

    const entranceAnim = { duration: 520, easing: 'easeOutCubic' };

    const trendCtx = getCtx('chart-main-trend');
    if (trendCtx) {
      const data = trendData();
      const grad = trendCtx.createLinearGradient(0, 0, 0, 220);
      grad.addColorStop(0, 'rgba(93, 245, 255, 0.32)');
      grad.addColorStop(1, 'rgba(93, 245, 255, 0.03)');
      trendChart = new Chart(trendCtx, {
        type: 'line',
        data: {
          labels: data.labels,
          datasets: [{
            label: mode() === 'daywise' ? 'Events per day' : 'Events by hour',
            data: data.counts,
            borderColor: '#5df5ff',
            backgroundColor: grad,
            borderWidth: 2.4,
            pointRadius: 2,
            pointHoverRadius: 5,
            fill: true,
            tension: 0.38,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: entranceAnim,
          plugins: { legend: { display: false }, tooltip: TOOLTIP_OPTS },
          scales: SCALE_OPTS,
        } as any,
      });
    }

    const severityCtx = getCtx('chart-severity');
    if (severityCtx) {
      severityChart = new Chart(severityCtx, {
        type: 'doughnut',
        data: {
          labels: ['High', 'Medium', 'Low', 'No data'],
          datasets: [{
            data: severityData(),
            backgroundColor: [C.red, C.amber, C.green, C.gray],
            borderColor: C.bg,
            borderWidth: 3,
            hoverOffset: 6,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: entranceAnim,
          cutout: '66%',
          plugins: { legend: LEGEND_BOTTOM, tooltip: TOOLTIP_OPTS },
        } as any,
      });
    }

    const complianceCtx = getCtx('chart-compliance');
    if (complianceCtx) {
      complianceChart = new Chart(complianceCtx, {
        type: 'doughnut',
        data: {
          labels: ['Violations', 'Non-violations'],
          datasets: [{
            data: complianceData(),
            backgroundColor: [C.red, C.green],
            borderColor: C.bg,
            borderWidth: 3,
            hoverOffset: 6,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: entranceAnim,
          cutout: '66%',
          plugins: { legend: LEGEND_BOTTOM, tooltip: TOOLTIP_OPTS },
        } as any,
      });
    }

    const violCtx = getCtx('chart-violations');
    if (violCtx) {
      const pairs = topViolationData();
      const grad = violCtx.createLinearGradient(0, 0, 420, 0);
      grad.addColorStop(0, 'rgba(255, 111, 143, 0.95)');
      grad.addColorStop(1, 'rgba(255, 209, 102, 0.88)');
      violationChart = new Chart(violCtx, {
        type: 'bar',
        data: {
          labels: pairs.map((p) => p[0]),
          datasets: [{
            label: 'Occurrences',
            data: pairs.map((p) => p[1]),
            backgroundColor: grad,
            borderColor: 'transparent',
            borderRadius: 14,
            borderSkipped: false,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: entranceAnim,
          indexAxis: 'y' as const,
          plugins: { legend: { display: false }, tooltip: TOOLTIP_OPTS },
          scales: SCALE_OPTS,
        } as any,
      });
    }

    const camCtx = getCtx('chart-cameras');
    if (camCtx) {
      const pairs = cameraData();
      const grad = camCtx.createLinearGradient(0, 0, 0, 260);
      grad.addColorStop(0, 'rgba(93, 245, 255, 0.92)');
      grad.addColorStop(1, 'rgba(62, 166, 255, 0.84)');
      cameraChart = new Chart(camCtx, {
        type: 'bar',
        data: {
          labels: pairs.map((p) => p[0]),
          datasets: [{
            label: 'Events',
            data: pairs.map((p) => p[1]),
            backgroundColor: grad,
            borderColor: 'transparent',
            borderRadius: 12,
            borderSkipped: false,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: entranceAnim,
          plugins: { legend: { display: false }, tooltip: TOOLTIP_OPTS },
          scales: SCALE_OPTS,
        } as any,
      });
    }

    const agentCtx = getCtx('chart-agents');
    if (agentCtx) {
      const pairs = agentData();
      const grad = agentCtx.createLinearGradient(0, 0, 0, 260);
      grad.addColorStop(0, 'rgba(107, 129, 255, 0.92)');
      grad.addColorStop(1, 'rgba(62, 166, 255, 0.84)');
      agentChart = new Chart(agentCtx, {
        type: 'bar',
        data: {
          labels: pairs.map((p) => p[0]),
          datasets: [{
            label: 'Events',
            data: pairs.map((p) => p[1]),
            backgroundColor: grad,
            borderColor: 'transparent',
            borderRadius: 12,
            borderSkipped: false,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: entranceAnim,
          plugins: { legend: { display: false }, tooltip: TOOLTIP_OPTS },
          scales: SCALE_OPTS,
        } as any,
      });
    }

    const personMixCtx = getCtx('chart-person-mix');
    if (personMixCtx) {
      personMixChart = new Chart(personMixCtx, {
        type: 'doughnut',
        data: {
          labels: ['Known', 'Unknown'],
          datasets: [{
            data: personMixData(),
            backgroundColor: [C.blue, C.amber],
            borderColor: C.bg,
            borderWidth: 3,
            hoverOffset: 6,
          }],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: entranceAnim,
          cutout: '66%',
          plugins: { legend: LEGEND_BOTTOM, tooltip: TOOLTIP_OPTS },
        } as any,
      });
    }

    const daySevCtx = getCtx('chart-day-severity');
    if (daySevCtx) {
      const byDay = daySeverityData();
      daySeverityChart = new Chart(daySevCtx, {
        type: 'bar',
        data: {
          labels: byDay.labels,
          datasets: [
            { label: 'High', data: byDay.high, backgroundColor: 'rgba(239, 68, 68, 0.86)', borderRadius: 6 },
            { label: 'Medium', data: byDay.medium, backgroundColor: 'rgba(245, 158, 11, 0.86)', borderRadius: 6 },
            { label: 'Low', data: byDay.low, backgroundColor: 'rgba(34, 197, 94, 0.86)', borderRadius: 6 },
          ],
        },
        options: {
          responsive: true,
          maintainAspectRatio: false,
          animation: entranceAnim,
          plugins: { legend: LEGEND_BOTTOM, tooltip: TOOLTIP_OPTS },
          scales: {
            ...SCALE_OPTS,
            x: { ...SCALE_OPTS.x, stacked: true },
            y: { ...SCALE_OPTS.y, stacked: true },
          },
        } as any,
      });
    }
  };

  const updateCharts = () => {
    const trend = trendData();
    if (trendChart) {
      setArr(trendChart.data.labels as any[], trend.labels);
      setArr(trendChart.data.datasets[0].data as any[], trend.counts);
      (trendChart.data.datasets[0] as any).label = mode() === 'daywise' ? 'Events per day' : 'Events by hour';
      trendChart.update('none');
    }

    if (severityChart) {
      setArr(severityChart.data.datasets[0].data as any[], severityData());
      severityChart.update('none');
    }

    if (complianceChart) {
      setArr(complianceChart.data.datasets[0].data as any[], complianceData());
      complianceChart.update('none');
    }

    if (violationChart) {
      const pairs = topViolationData();
      setArr(violationChart.data.labels as any[], pairs.map((p) => p[0]));
      setArr(violationChart.data.datasets[0].data as any[], pairs.map((p) => p[1]));
      violationChart.update('none');
    }

    if (cameraChart) {
      const pairs = cameraData();
      setArr(cameraChart.data.labels as any[], pairs.map((p) => p[0]));
      setArr(cameraChart.data.datasets[0].data as any[], pairs.map((p) => p[1]));
      cameraChart.update('none');
    }

    if (agentChart) {
      const pairs = agentData();
      setArr(agentChart.data.labels as any[], pairs.map((p) => p[0]));
      setArr(agentChart.data.datasets[0].data as any[], pairs.map((p) => p[1]));
      agentChart.update('none');
    }

    if (personMixChart) {
      setArr(personMixChart.data.datasets[0].data as any[], personMixData());
      personMixChart.update('none');
    }

    if (daySeverityChart) {
      const byDay = daySeverityData();
      setArr(daySeverityChart.data.labels as any[], byDay.labels);
      setArr(daySeverityChart.data.datasets[0].data as any[], byDay.high);
      setArr(daySeverityChart.data.datasets[1].data as any[], byDay.medium);
      setArr(daySeverityChart.data.datasets[2].data as any[], byDay.low);
      daySeverityChart.update('none');
    }
  };

  onMount(async () => {
    connectAgentEventsWebSocket();
    await Promise.all([fetchNodes(), fetchAgents()]);
    await loadEvents();
    requestAnimationFrame(() => initCharts());
  });

  onMount(() => {
    const unsub = subscribeToAgentEvents((ev) => {
      setEvents((prev) => dedupeEvents([ev, ...prev]));
      setLastRefresh(new Date());
    });
    onCleanup(unsub);
  });

  onCleanup(destroyCharts);

  let chartsReady = false;
  createEffect(() => {
    mode();
    trendData();
    severityData();
    complianceData();
    topViolationData();
    cameraData();
    agentData();
    personMixData();
    daySeverityData();

    if (!chartsReady) {
      chartsReady = true;
      return;
    }
    updateCharts();
  });

  const fmtTime = (iso: string) => {
    const d = new Date(iso);
    const now = new Date();
    if (d.toDateString() === now.toDateString()) {
      return `Today ${d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}`;
    }
    return `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })} ${d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`;
  };

  return (
    <div class="h-full overflow-y-auto bg-neu-925/20">
      <div class="p-5 space-y-5">

        <RevealOnScroll>
          <div class="saas-section p-4 md:p-5 space-y-4">
            <div class="flex flex-col xl:flex-row xl:items-center xl:justify-between gap-3">
              <div>
                <h2 class="text-sm md:text-base font-semibold text-white">Advanced Analytics</h2>
                <p class="text-xs text-neu-500 mt-0.5">
                  Total and day-wise monitoring with camera, agent, severity, person, and violation-level filters.
                </p>
              </div>
              <div class="flex flex-wrap items-center gap-2">
                <span class="text-xs text-neu-500 px-2 py-1 rounded-lg border border-neu-800 bg-neu-900/75">
                  Loaded {events().length} events
                </span>
                <span class="text-xs text-neu-500 px-2 py-1 rounded-lg border border-neu-800 bg-neu-900/75">
                  Refreshed {lastRefresh().toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' })}
                </span>
                <button
                  class="btn-primary"
                  onClick={async () => {
                    await loadEvents();
                    requestAnimationFrame(() => initCharts());
                  }}
                  disabled={loading()}
                >
                  <FiRefreshCw size={14} class={loading() ? 'animate-spin' : ''} />
                  {loading() ? 'Loading...' : 'Refresh'}
                </button>
              </div>
            </div>

            <div class="grid grid-cols-1 xl:grid-cols-[auto_auto_minmax(0,1fr)] gap-2 items-center">
              <div class="inline-flex rounded-xl border border-neu-750 bg-neu-900/70 p-1">
                <button
                  class={`px-3 h-8 rounded-lg text-xs font-semibold transition-colors ${mode() === 'total' ? 'bg-violet-600 text-white' : 'text-neu-300 hover:text-neu-100 hover:bg-neu-800/80'}`}
                  onClick={() => setMode('total')}
                >
                  Total Analytics
                </button>
                <button
                  class={`px-3 h-8 rounded-lg text-xs font-semibold transition-colors ${mode() === 'daywise' ? 'bg-violet-600 text-white' : 'text-neu-300 hover:text-neu-100 hover:bg-neu-800/80'}`}
                  onClick={() => setMode('daywise')}
                >
                  Day-wise Analytics
                </button>
              </div>

              <div class="flex items-center gap-2">
                <select class="select h-9" value={rangePreset()} onChange={(e) => setRangePreset(e.currentTarget.value as RangePreset)}>
                  <option value="today">Today</option>
                  <option value="yesterday">Yesterday</option>
                  <option value="7d">Last 7 days</option>
                  <option value="30d">Last 30 days</option>
                  <option value="all">All data</option>
                  <option value="custom">Custom range</option>
                </select>
                <Show when={rangePreset() === 'custom'}>
                  <input class="input h-9" type="date" value={dateFrom()} onInput={(e) => setDateFrom(e.currentTarget.value)} />
                  <input class="input h-9" type="date" value={dateTo()} onInput={(e) => setDateTo(e.currentTarget.value)} />
                </Show>
              </div>

              <div class="flex items-center justify-start xl:justify-end gap-2">
                <span class="text-xs text-neu-500 inline-flex items-center gap-1 px-2 py-1 rounded-lg border border-neu-800 bg-neu-900/70">
                  <FiFilter size={12} />
                  {activeFilters()} active filters
                </span>
                <button class="btn-secondary h-9" onClick={clearFilters}>
                  Clear Filters
                </button>
              </div>
            </div>

            <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-5 gap-2">
              <select class="select h-9" value={cameraFilter()} onChange={(e) => setCameraFilter(e.currentTarget.value)}>
                <option value="all">All cameras</option>
                <For each={cameraOptions()}>{(opt) => <option value={opt.id}>{opt.name}</option>}</For>
              </select>

              <select class="select h-9" value={agentFilter()} onChange={(e) => setAgentFilter(e.currentTarget.value)}>
                <option value="all">All agents</option>
                <For each={agentOptions()}>{(opt) => <option value={opt.id}>{opt.name}</option>}</For>
              </select>

              <select class="select h-9" value={severityFilter()} onChange={(e) => setSeverityFilter(e.currentTarget.value as any)}>
                <option value="all">All severities</option>
                <option value="high">High</option>
                <option value="medium">Medium</option>
                <option value="low">Low</option>
                <option value="none">No severity</option>
              </select>

              <select class="select h-9" value={kindFilter()} onChange={(e) => setKindFilter(e.currentTarget.value)}>
                <option value="all">All event types</option>
                <For each={kindOptions()}>{(k) => <option value={k}>{k.replace(/_/g, ' ')}</option>}</For>
              </select>

              <select class="select h-9" value={personFilter()} onChange={(e) => setPersonFilter(e.currentTarget.value as PersonFilter)}>
                <option value="all">All identities</option>
                <option value="known">Known persons only</option>
                <option value="unknown">Unknown persons only</option>
              </select>
            </div>

            <div class="grid grid-cols-1 sm:grid-cols-2 xl:grid-cols-[1fr_1fr_1fr_2fr] gap-2">
              <select class="select h-9" value={violationFilter()} onChange={(e) => setViolationFilter(e.currentTarget.value as ViolationFilter)}>
                <option value="all">All violation states</option>
                <option value="violations">Violations only</option>
                <option value="non_violations">Non-violations only</option>
              </select>

              <select class="select h-9" value={snapshotFilter()} onChange={(e) => setSnapshotFilter(e.currentTarget.value as SnapshotFilter)}>
                <option value="all">All snapshot states</option>
                <option value="with">With snapshot</option>
                <option value="without">Without snapshot</option>
              </select>

              <div class="text-xs text-neu-500 flex items-center px-3 border border-neu-800 rounded-xl bg-neu-900/55">
                Mode: {mode() === 'daywise' ? 'Daily trend + day table' : 'Total aggregation + hourly profile'}
              </div>

              <input
                class="input h-9"
                placeholder="Search summary, person, camera, agent, or violation label..."
                value={textFilter()}
                onInput={(e) => setTextFilter(e.currentTarget.value)}
              />
            </div>
          </div>
        </RevealOnScroll>

        <RevealOnScroll>
          <div class="grid grid-cols-2 lg:grid-cols-4 gap-4">
            <KpiCard label="Filtered Events" value={kpiFiltered()} sub={`${events().length} loaded events`} accent="violet" icon={FiShield} />
            <KpiCard label="Day Avg" value={kpiDailyAvg()} sub={`${dayRows().length} day buckets`} accent="green" icon={FiActivity} />
            <KpiCard label="High Severity" value={kpiHigh()} sub={`${kpiHighRate()} of filtered`} accent="red" icon={FiAlertTriangle} />
            <KpiCard label="Violation Rate" value={kpiViolationRate()} sub={`${kpiViolationEvents()} violating events`} accent="amber" icon={FiUsers} />
            <KpiCard label="Active Cameras" value={kpiUniqueCameras()} sub="cameras in filtered set" accent="green" icon={FiCamera} />
            <KpiCard label="Active Agents" value={kpiUniqueAgents()} sub="agents in filtered set" accent="violet" icon={FiShield} />
            <KpiCard label="Known Persons" value={kpiKnownPersons()} sub="distinct known identities" accent="green" icon={FiUsers} />
            <KpiCard label="Snapshot Coverage" value={kpiSnapshotRate()} sub="events with snapshot" accent="amber" icon={FiCamera} />
          </div>
        </RevealOnScroll>

        <RevealOnScroll>
          <div class="chart-card">
            <div class="chart-card-title">
              {mode() === 'daywise' ? 'Day-wise Event Trend' : 'Hour-wise Activity Profile'}
            </div>
            <div class="relative w-full" style={{ height: '230px' }}>
              <canvas id="chart-main-trend" />
              <Show when={trendData().labels.length === 0}>
                <div class="absolute inset-0 flex items-center justify-center text-neu-700 text-sm pointer-events-none">
                  No activity data for selected filters
                </div>
              </Show>
            </div>
          </div>
        </RevealOnScroll>

        <RevealOnScroll>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div class="chart-card">
              <div class="chart-card-title">Severity Distribution</div>
              <div class="relative w-full" style={{ height: '220px' }}><canvas id="chart-severity" /></div>
            </div>
            <div class="chart-card">
              <div class="chart-card-title">Violation vs Non-violation</div>
              <div class="relative w-full" style={{ height: '220px' }}><canvas id="chart-compliance" /></div>
            </div>
          </div>
        </RevealOnScroll>

        <RevealOnScroll>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div class="chart-card">
              <div class="chart-card-title">Top Violation Types</div>
              <div class="relative w-full" style={{ height: '250px' }}>
                <canvas id="chart-violations" />
                <Show when={topViolationData().length === 0}>
                  <div class="absolute inset-0 flex items-center justify-center text-neu-700 text-sm pointer-events-none">
                    No violation labels available
                  </div>
                </Show>
              </div>
            </div>
            <div class="chart-card">
              <div class="chart-card-title">Events per Camera</div>
              <div class="relative w-full" style={{ height: '250px' }}>
                <canvas id="chart-cameras" />
                <Show when={cameraData().length === 0}>
                  <div class="absolute inset-0 flex items-center justify-center text-neu-700 text-sm pointer-events-none">
                    No camera data available
                  </div>
                </Show>
              </div>
            </div>
          </div>
        </RevealOnScroll>

        <RevealOnScroll>
          <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div class="chart-card">
              <div class="chart-card-title">Events per Agent</div>
              <div class="relative w-full" style={{ height: '250px' }}>
                <canvas id="chart-agents" />
                <Show when={agentData().length === 0}>
                  <div class="absolute inset-0 flex items-center justify-center text-neu-700 text-sm pointer-events-none">
                    No agent activity data available
                  </div>
                </Show>
              </div>
            </div>
            <div class="chart-card">
              <div class="chart-card-title">Known vs Unknown Person Sightings</div>
              <div class="relative w-full" style={{ height: '250px' }}><canvas id="chart-person-mix" /></div>
            </div>
          </div>
        </RevealOnScroll>

        <RevealOnScroll>
          <div class="chart-card">
            <div class="chart-card-title">Day-wise Severity Mix (Stacked)</div>
            <div class="relative w-full" style={{ height: '260px' }}>
              <canvas id="chart-day-severity" />
              <Show when={daySeverityData().labels.length === 0}>
                <div class="absolute inset-0 flex items-center justify-center text-neu-700 text-sm pointer-events-none">
                  No day-wise severity data available
                </div>
              </Show>
            </div>
          </div>
        </RevealOnScroll>

        <RevealOnScroll>
          <div class="chart-card">
            <div class="chart-card-title">Day-wise Analytics Breakdown</div>
            <Show
              when={dayRows().length > 0}
              fallback={<p class="text-neu-700 text-sm py-8 text-center">No day-wise records for selected filters</p>}
            >
              <div class="overflow-x-auto mt-2">
                <table class="data-table">
                  <thead>
                    <tr>
                      <th>Date</th>
                      <th>Events</th>
                      <th>High</th>
                      <th>Medium</th>
                      <th>Violations</th>
                      <th>Active Cameras</th>
                      <th>Known Persons</th>
                      <th>Unknown Sightings</th>
                    </tr>
                  </thead>
                  <tbody>
                    <For each={dayRows()}>
                      {(row) => (
                        <tr>
                          <td class="text-neu-200 font-medium">{localDateLabel(row.dateKey)}</td>
                          <td>{row.events}</td>
                          <td><span class="badge border severity-high">{row.high}</span></td>
                          <td><span class="badge border severity-medium">{row.medium}</span></td>
                          <td><span class="badge badge-violation">{row.violations}</span></td>
                          <td>{row.cameras}</td>
                          <td>{row.knownPersons}</td>
                          <td>{row.unknownSightings}</td>
                        </tr>
                      )}
                    </For>
                  </tbody>
                </table>
              </div>
            </Show>
          </div>
        </RevealOnScroll>

        <RevealOnScroll>
          <div class="chart-card">
            <div class="chart-card-title">Recent High-Severity Alerts</div>
            <Show
              when={recentHighAlerts().length > 0}
              fallback={<p class="text-neu-700 text-sm py-8 text-center">No high-severity alerts in selected filters</p>}
            >
              <div class="overflow-x-auto mt-2">
                <table class="data-table">
                  <thead>
                    <tr>
                      <th>Time</th>
                      <th>Agent</th>
                      <th>Camera</th>
                      <th>Event Kind</th>
                      <th>Violations</th>
                      <th>Summary</th>
                    </tr>
                  </thead>
                  <tbody>
                    <For each={recentHighAlerts()}>
                      {(ev) => {
                        const sid = eventServiceId(ev);
                        const cam = cameraNameMap().get(sid) || (sid ? sid.slice(0, 14) : '—');
                        const vc = eventViolationCount(ev);
                        const kind = eventKind(ev).replace(/_/g, ' ');
                        const summary = eventSummary(ev);
                        return (
                          <tr>
                            <td class="whitespace-nowrap font-mono text-xs text-neu-500">{fmtTime(ev.created_at)}</td>
                            <td class="text-neu-200 text-sm">{ev.agent_name || ev.agent_id.slice(0, 12)}</td>
                            <td class="text-neu-500 text-xs">{cam}</td>
                            <td class="text-neu-500 text-xs">{kind}</td>
                            <td><span class="badge badge-violation">{vc}</span></td>
                            <td class="max-w-[380px] truncate text-neu-500 text-xs" title={summary}>{summary || '—'}</td>
                          </tr>
                        );
                      }}
                    </For>
                  </tbody>
                </table>
              </div>
            </Show>
          </div>
        </RevealOnScroll>
      </div>
    </div>
  );
};

export default TabAnalytics;
