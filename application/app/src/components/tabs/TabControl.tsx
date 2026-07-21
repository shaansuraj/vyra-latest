import { createMemo, createSignal, lazy, Suspense, type Component, type ParentComponent } from 'solid-js';
import { FiServer, FiCamera, FiCpu } from 'solid-icons/fi';
import { ArkTabs } from '../../ark/ArkTabs';
import { ManagedNodesView, CamerasView } from './TabHome';

const TabAgents = lazy(() => import('./TabAgents'));

const SubtabLoading: Component = () => (
  <div class="h-full min-h-[220px] flex items-center justify-center text-sm text-neu-500">
    Loading control view...
  </div>
);

const LazySubtab: ParentComponent = (props) => (
  <Suspense fallback={<SubtabLoading />}>{props.children}</Suspense>
);

export type ControlSubtab = 'nodes' | 'cameras' | 'agents';

const TabControl: Component = () => {
  const [active, setActive] = createSignal<ControlSubtab>('nodes');
  const subtitle = createMemo(() => {
    if (active() === 'nodes') return 'Create, start, and supervise managed nodes.';
    if (active() === 'cameras') return 'Maintain camera inventory and node assignments.';
    return 'Scale workers and manage runtime pipelines.';
  });

  return (
    <div class="h-full min-h-0 overflow-hidden flex flex-col">
      <div class="flex-none px-5 py-4 border-b border-neu-800/70 bg-neu-900/45">
        <h2 class="text-sm font-semibold text-neu-100">Control Workspace</h2>
        <p class="text-xs text-neu-500 mt-1">{subtitle()}</p>
      </div>
      <div class="flex-1 min-h-0 p-4">
        <ArkTabs
          value={active()}
          onValueChange={(v) => setActive(v as ControlSubtab)}
          listClass="mb-3"
          items={[
            { value: 'nodes', label: 'Nodes', icon: <FiServer size={15} />, content: <ManagedNodesView /> },
            { value: 'cameras', label: 'Cameras', icon: <FiCamera size={15} />, content: <CamerasView /> },
            { value: 'agents', label: 'Agents', icon: <FiCpu size={15} />, content: <LazySubtab><TabAgents /></LazySubtab> },
          ]}
        />
      </div>
    </div>
  );
};

export default TabControl;
