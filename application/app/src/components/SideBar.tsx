import { Component, createSignal, type JSX } from 'solid-js';
import {
  FiActivity,
  FiSearch,
  FiMap,
  FiBarChart2,
  FiCpu,
  FiUsers,
  FiSettings,
  FiLogOut,
  FiChevronLeft,
  FiChevronRight,
} from 'solid-icons/fi';
import { logout, setTab, tab, type SimpleTabType } from '../shared';

interface NavItem {
  type: SimpleTabType;
  icon: (size: number) => JSX.Element;
  label: string;
  caption: string;
}

const NAV_ITEMS: NavItem[] = [
  { type: 'command', icon: (s) => <FiActivity size={s} />, label: 'Command Center', caption: 'Live operations' },
  { type: 'investigate', icon: (s) => <FiSearch size={s} />, label: 'Investigate', caption: 'Search and AI' },
  { type: 'timeline', icon: (s) => <FiMap size={s} />, label: 'Timeline', caption: 'Journeys and evidence' },
  { type: 'performance', icon: (s) => <FiBarChart2 size={s} />, label: 'Performance', caption: 'Analytics dashboards' },
  { type: 'control', icon: (s) => <FiCpu size={s} />, label: 'Control', caption: 'Nodes and workers' },
  { type: 'identity', icon: (s) => <FiUsers size={s} />, label: 'Identity', caption: 'Person registry' },
  { type: 'system', icon: (s) => <FiSettings size={s} />, label: 'System', caption: 'Platform settings' },
];

const SideBar: Component = () => {
  const [isCollapsed, setIsCollapsed] = createSignal(false);

  const isActive = (type: SimpleTabType) => tab().type === type;

  return (
    <aside
      class={`
        relative flex flex-col h-screen shrink-0
        bg-neu-925/96 border-r border-neu-800/80 backdrop-blur-xl
        transition-all duration-300 ease-out
        ${isCollapsed() ? 'w-[82px]' : 'w-[282px]'}
      `}
    >
      <div class="pointer-events-none absolute -top-20 left-4 w-44 h-44 rounded-full bg-cyan-500/12 blur-3xl ambient-float" />
      <div class="pointer-events-none absolute -bottom-20 right-0 w-40 h-40 rounded-full bg-violet-500/15 blur-3xl ambient-float" style={{ 'animation-delay': '1.3s' }} />

      <div class={`relative z-[1] h-16 border-b border-neu-800/75 px-3 ${isCollapsed() ? 'justify-center' : 'justify-between'} flex items-center`}>
        <div class={`flex items-center ${isCollapsed() ? 'justify-center' : 'gap-3'} min-w-0`}>
          <div class="w-10 h-10 rounded-xl border border-cyan-400/35 bg-neu-900/95 flex items-center justify-center shadow-lg shadow-cyan-900/20">
            <svg width="18" height="18" viewBox="0 0 18 18" fill="none">
              <circle cx="9" cy="9" r="2.8" fill="#9edbff" />
              <circle cx="9" cy="9" r="5.8" stroke="#70bbff" stroke-width="1.2" stroke-dasharray="2 1.2" opacity="0.85" />
              <circle cx="9" cy="9" r="7.8" stroke="#5df5ff" stroke-width="1" opacity="0.5" />
            </svg>
          </div>
          {!isCollapsed() && (
            <div class="min-w-0 logo-compact">
              <div class="heading-display text-neu-100 font-semibold text-[15px] tracking-[0.08em] truncate">VYRA</div>
              <div class="text-[10px] tracking-[0.1em] text-neu-500 truncate">Kinetic Console</div>
            </div>
          )}
        </div>
      </div>

      {!isCollapsed() && (
        <div class="px-3 pt-4 pb-2 text-[10px] tracking-[0.1em] text-neu-550 font-semibold">Sections</div>
      )}

      <nav class="relative z-[1] flex-1 px-2 space-y-1 overflow-y-auto">
        {NAV_ITEMS.map((item) => (
          <button
            class={`
              group relative w-full rounded-xl border px-2.5 py-2.5 transition-all duration-200 text-left
              ${isCollapsed() ? 'flex justify-center items-center h-11' : 'flex items-start gap-2.5'}
              ${isActive(item.type)
                ? 'border-cyan-500/40 bg-cyan-500/12 text-cyan-100 interaction-layer'
                : 'border-transparent text-neu-400 hover:text-neu-100 hover:border-neu-700/60 hover:bg-neu-800/45'}
            `}
            onClick={() => setTab({ type: item.type })}
            title={isCollapsed() ? item.label : undefined}
          >
            {isActive(item.type) && (
              <span class="absolute left-0 top-2 bottom-2 w-[2px] rounded-r bg-gradient-to-b from-cyan-300 to-violet-400" />
            )}
            <span class={`mt-0.5 ${isActive(item.type) ? 'text-cyan-300' : 'text-neu-500 group-hover:text-neu-200'} icon-pop`}>
              {item.icon(17)}
            </span>
            {!isCollapsed() && (
              <span class="min-w-0">
                <span class="block text-[13px] font-semibold leading-tight truncate">{item.label}</span>
                <span class="block text-[11px] text-neu-500 truncate mt-0.5 leading-tight">{item.caption}</span>
              </span>
            )}
          </button>
        ))}
      </nav>

      <div class="relative z-[1] border-t border-neu-800/70 px-2 py-2.5 space-y-2">
        {!isCollapsed() && (
          <div class="mx-1 rounded-xl border border-neu-800/80 bg-neu-900/65 px-2.5 py-2">
            <div class="text-[10px] uppercase tracking-[0.16em] text-neu-600">System</div>
            <div class="mt-1 text-[11px] text-green-400 flex items-center gap-1.5">
              <span class="w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
              Operational
            </div>
          </div>
        )}

        <button
          class={`
            w-full rounded-xl border border-transparent text-neu-500 hover:text-red-300 hover:bg-red-900/15
            transition-all duration-150 px-2.5 py-[9px] text-sm font-medium
            ${isCollapsed() ? 'flex items-center justify-center' : 'flex items-center gap-2.5'}
          `}
          onClick={async () => {
            const ok = window.confirm('Sign out of VYRA?');
            if (!ok) return;
            await logout();
          }}
          title={isCollapsed() ? 'Sign Out' : undefined}
        >
          <FiLogOut size={17} class="flex-shrink-0" />
          {!isCollapsed() && <span>Sign Out</span>}
        </button>
      </div>

      <button
        class="absolute -right-3 top-16 z-30 w-6 h-6 rounded-full bg-neu-850 border border-neu-700 flex items-center justify-center text-neu-400 hover:text-white hover:bg-neu-750 transition-all duration-150 shadow-lg"
        onClick={() => setIsCollapsed(!isCollapsed())}
        title={isCollapsed() ? 'Expand navigation' : 'Collapse navigation'}
      >
        {isCollapsed() ? <FiChevronRight size={12} /> : <FiChevronLeft size={12} />}
      </button>
    </aside>
  );
};

export default SideBar;
