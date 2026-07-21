import { createMemo, createSignal, lazy, onMount, Suspense, type Component } from 'solid-js';
import { FiActivity, FiAlertTriangle, FiCamera, FiGrid, FiZap } from 'solid-icons/fi';
import { fetchRecentAgentEventsRest, nodes, setTab, type AgentEvent } from '../../shared';
import CountUpNumber from '../motion/CountUpNumber';
import RevealOnScroll from '../motion/RevealOnScroll';
import MagneticButton from '../motion/MagneticButton';

const TabBirdEye = lazy(() => import('./TabBirdEye'));

const TabCommandCenter: Component = () => {
  const [events, setEvents] = createSignal<AgentEvent[]>([]);

  onMount(async () => {
    try {
      const data = await fetchRecentAgentEventsRest({ limit: 260 });
      setEvents(data);
    } catch {
      setEvents([]);
    }
  });

  const kpis = createMemo(() => {
    const evs = events();
    let violations = 0;
    let highSeverity = 0;
    const cameras = new Set<string>();

    for (const ev of evs) {
      const meta: any = ev.metadata || {};
      const data: any = ev.data || {};
      const vc = Number(meta.violation_count ?? data.violation_count ?? 0);
      const sev = String(meta.severity ?? data.severity ?? '').toLowerCase();
      if (vc > 0 || meta.has_violations === true || data.has_violations === true) violations += Math.max(1, vc);
      if (sev === 'high') highSeverity += 1;
      const sid = String(meta.service_id || ev.service_ids?.[0] || '').trim();
      if (sid) cameras.add(sid);
    }

    return {
      totalEvents: evs.length,
      violations,
      highSeverity,
      activeCameras: cameras.size,
    };
  });

  const jumpToFirstStream = () => {
    const firstNode = nodes().find((n) => (n.services || []).length > 0);
    const firstService = firstNode?.services?.[0];
    if (!firstNode || !firstService) return;
    setTab({
      type: 'view',
      nodeId: firstNode.id,
      serviceId: firstService.id,
      name: firstService.name || 'Live Stream',
    });
  };

  return (
    <div class="h-full min-h-0 overflow-hidden flex flex-col gap-4 p-4 md:p-5">
      <RevealOnScroll class="flex-none">
        <div class="depth-panel rounded-2xl px-4 py-4 md:px-5 md:py-5 border border-neu-700/60">
          <div class="flex flex-wrap items-start gap-4 justify-between">
            <div>
              <div class="heading-display text-[12px] tracking-[0.1em] text-neu-500">Command Center</div>
              <h2 class="text-xl md:text-2xl font-semibold text-neu-100 mt-1 leading-tight">Live Operations Grid</h2>
              <p class="text-sm text-neu-400 mt-2 max-w-3xl leading-relaxed">
                Conversational surveillance interface with active incidents, identities, and camera orchestration in one spatial canvas.
              </p>
            </div>
            <MagneticButton
              class="saas-cta lift-on-hover interaction-layer"
              onClick={jumpToFirstStream}
              title="Jump to first live stream"
            >
              <FiZap size={14} class="icon-pop" />
              Open Live View
            </MagneticButton>
          </div>

          <div class="grid grid-cols-2 lg:grid-cols-4 gap-3 mt-4">
            <div class="kpi-card-premium">
              <div class="text-[11px] text-neu-500 tracking-[0.08em] flex items-center gap-1"><FiActivity size={12} /> Events</div>
              <div class="text-2xl mt-2 font-semibold text-neu-100"><CountUpNumber value={kpis().totalEvents} /></div>
            </div>
            <div class="kpi-card-premium">
              <div class="text-[11px] text-neu-500 tracking-[0.08em] flex items-center gap-1"><FiAlertTriangle size={12} /> Violations</div>
              <div class="text-2xl mt-2 font-semibold text-red-300"><CountUpNumber value={kpis().violations} /></div>
            </div>
            <div class="kpi-card-premium">
              <div class="text-[11px] text-neu-500 tracking-[0.08em] flex items-center gap-1"><FiGrid size={12} /> High Severity</div>
              <div class="text-2xl mt-2 font-semibold text-amber-300"><CountUpNumber value={kpis().highSeverity} /></div>
            </div>
            <div class="kpi-card-premium">
              <div class="text-[11px] text-neu-500 tracking-[0.08em] flex items-center gap-1"><FiCamera size={12} /> Active Cameras</div>
              <div class="text-2xl mt-2 font-semibold text-cyan-300"><CountUpNumber value={kpis().activeCameras} /></div>
            </div>
          </div>
        </div>
      </RevealOnScroll>

      <div class="flex-1 min-h-0 depth-canvas rounded-2xl border border-neu-800/80 overflow-hidden">
        <Suspense fallback={<div class="h-full flex items-center justify-center text-sm text-neu-500">Loading camera grid...</div>}>
          <TabBirdEye />
        </Suspense>
      </div>
    </div>
  );
};

export default TabCommandCenter;
