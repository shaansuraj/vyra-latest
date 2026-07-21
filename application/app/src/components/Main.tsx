import { lazy, Match, Show, Suspense, Switch, type Component, type ParentComponent } from 'solid-js';
import SideBar from './SideBar';
import TabLayout from './TabLayout';
import ArkToast from '../ark/ArkToast';
import NodeAuthDialog from './NodeAuthDialog';
import { tab, type Tab } from '../shared';
import { useAgentPanel } from './AgentPanel';

const VideoTile = lazy(() => import('./VideoTile'));
const TabCommandCenter = lazy(() => import('./tabs/TabCommandCenter'));
const TabInvestigate = lazy(() => import('./tabs/TabInvestigate'));
const TabTimeline = lazy(() => import('./tabs/TabTimeline'));
const TabAnalytics = lazy(() => import('./tabs/TabAnalytics'));
const TabControl = lazy(() => import('./tabs/TabControl'));
const TabIdentity = lazy(() => import('./tabs/TabIdentity'));
const TabSystem = lazy(() => import('./tabs/TabSystem'));

const PaneLoading: Component = () => (
  <div class="h-full min-h-[220px] flex items-center justify-center text-sm text-neu-500">
    Loading workspace...
  </div>
);

const LazyPane: ParentComponent = (props) => (
  <Suspense fallback={<PaneLoading />}>
    {props.children}
  </Suspense>
);

const Main: Component = () => {
  return (
    <div class="kinetic-shell relative flex h-screen overflow-hidden">
      <ArkToast />
      <NodeAuthDialog />
      <SideBar />

      <div class="relative flex-1 h-screen overflow-hidden min-w-0 z-[1]">
        <Switch
          fallback={
            <TabLayout title="Command Center" subtitle="Live operations and spatial context">
              <LazyPane><TabCommandCenter /></LazyPane>
            </TabLayout>
          }
        >
          <Match when={tab().type === 'view'}>
            {(() => {
              const t = tab() as Extract<Tab, { type: 'view' }>;
              const agentPanel = useAgentPanel(t.serviceId);

              return (
                <div class="flex h-screen">
                  <div class="flex-1 h-screen overflow-hidden min-w-0">
                    <TabLayout
                      title={t.name || 'Live Stream'}
                      subtitle="Single-camera deep view"
                      showTopHeader={true}
                      headerAction={<Show when={!agentPanel.showAgentPanel()}><agentPanel.Toggle /></Show>}
                    >
                      <LazyPane><VideoTile nodeId={t.nodeId} serviceId={t.serviceId} name={t.name} /></LazyPane>
                    </TabLayout>
                  </div>
                  <agentPanel.Comp />
                </div>
              );
            })()}
          </Match>

          <Match when={tab().type === 'command'}>
            <TabLayout title="Command Center" subtitle="Live operations and spatial context">
              <LazyPane><TabCommandCenter /></LazyPane>
            </TabLayout>
          </Match>

          <Match when={tab().type === 'investigate'}>
            <TabLayout title="Investigate" subtitle="Timeline, AI queries, and daily briefs">
              <LazyPane><TabInvestigate /></LazyPane>
            </TabLayout>
          </Match>

          <Match when={tab().type === 'timeline'}>
            <TabLayout title="Timeline" subtitle="Journeys, cases, and evidence reconstruction">
              <LazyPane><TabTimeline /></LazyPane>
            </TabLayout>
          </Match>

          <Match when={tab().type === 'performance'}>
            <TabLayout title="Performance" subtitle="Operational analytics and trend intelligence">
              <LazyPane><TabAnalytics /></LazyPane>
            </TabLayout>
          </Match>

          <Match when={tab().type === 'control'}>
            <TabLayout title="Control" subtitle="Nodes, cameras, services, and runtime workers">
              <LazyPane><TabControl /></LazyPane>
            </TabLayout>
          </Match>

          <Match when={tab().type === 'identity'}>
            <TabLayout title="Identity" subtitle="Person registry and cross-camera traces">
              <LazyPane><TabIdentity /></LazyPane>
            </TabLayout>
          </Match>

          <Match when={tab().type === 'system'}>
            <TabLayout title="System" subtitle="Settings and runtime controls">
              <LazyPane><TabSystem /></LazyPane>
            </TabLayout>
          </Match>
        </Switch>
      </div>
    </div>
  );
};

export default Main;
