import { createMemo, createSignal, lazy, Suspense, type Component, type ParentComponent } from 'solid-js';
import { FiActivity, FiSearch, FiMessageSquare, FiFileText } from 'solid-icons/fi';
import { ArkTabs } from '../../ark/ArkTabs';

const TabMoments = lazy(() => import('./TabMoments'));
const TabSearch = lazy(() => import('./TabSearch'));
const TabSummary = lazy(() => import('./TabSummary'));

const SubtabLoading: Component = () => (
  <div class="h-full min-h-[220px] flex items-center justify-center text-sm text-neu-500">
    Loading investigation view...
  </div>
);

const LazySubtab: ParentComponent = (props) => (
  <Suspense fallback={<SubtabLoading />}>{props.children}</Suspense>
);

export type InvestigateSubtab = 'events' | 'search' | 'ask_ai' | 'daily';

const TabInvestigate: Component = () => {
  const [active, setActive] = createSignal<InvestigateSubtab>('events');
  const subtitle = createMemo(() => {
    if (active() === 'events') return 'Review chronological events with clean filters and fast drill-down.';
    if (active() === 'search') return 'Keyword search across recorded moments, agents, and cameras.';
    if (active() === 'ask_ai') return 'Query incidents in natural language and inspect supporting evidence.';
    return 'Track daily operational narratives, incidents, and repeat patterns.';
  });

  return (
    <div class="h-full min-h-0 overflow-hidden flex flex-col">
      <div class="flex-none px-5 py-4 border-b border-neu-800/70 bg-neu-900/45">
        <h2 class="text-sm font-semibold text-neu-100">Investigate Workspace</h2>
        <p class="text-xs text-neu-500 mt-1">{subtitle()}</p>
      </div>
      <div class="flex-1 min-h-0 p-4">
        <ArkTabs
          value={active()}
          onValueChange={(v) => setActive(v as InvestigateSubtab)}
          listClass="mb-3"
          items={[
            { value: 'events', label: 'Events', icon: <FiActivity size={15} />, content: <LazySubtab><TabMoments /></LazySubtab> },
            { value: 'search', label: 'Search', icon: <FiSearch size={15} />, content: <LazySubtab><TabSearch forcedMode="search" /></LazySubtab> },
            { value: 'ask_ai', label: 'Ask AI', icon: <FiMessageSquare size={15} />, content: <LazySubtab><TabSearch forcedMode="ai" /></LazySubtab> },
            { value: 'daily', label: 'Daily Brief', icon: <FiFileText size={15} />, content: <LazySubtab><TabSummary /></LazySubtab> },
          ]}
        />
      </div>
    </div>
  );
};

export default TabInvestigate;
