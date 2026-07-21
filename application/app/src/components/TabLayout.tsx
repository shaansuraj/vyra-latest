import { Show, type Component, type JSX } from 'solid-js';
import LivingHeader from './motion/LivingHeader';

interface TabLayoutProps {
  title: string;
  subtitle?: string;
  children?: JSX.Element;
  headerAction?: JSX.Element;
  showTopHeader?: boolean;
}

const TabLayout: Component<TabLayoutProps> = (props) => {
  const showTopHeader = () => props.showTopHeader === true;

  return (
    <div class="saas-page flex flex-col h-screen py-3 pr-3 pl-2 overflow-hidden">
      <Show when={showTopHeader()}>
        <LivingHeader>
          <div class="relative flex-none min-h-[68px] flex items-center px-5 depth-panel border border-neu-800/75 rounded-2xl mb-3">
            <div class="absolute inset-x-0 top-0 h-[2px] rounded-t-2xl bg-gradient-to-r from-cyan-400/0 via-cyan-400/70 to-violet-400/0" />
            <div class="flex flex-col justify-center min-w-0 logo-compact">
              <span class="heading-display text-[16px] font-semibold text-neu-100 leading-tight tracking-[0.03em]">{props.title}</span>
              {props.subtitle && <span class="text-[12px] text-neu-400 leading-tight mt-1">{props.subtitle}</span>}
            </div>
            <div class="flex-1" />
            <div class="flex items-center gap-2">{props.headerAction}</div>
          </div>
        </LivingHeader>
      </Show>

      <div class="flex-1 overflow-hidden min-h-0">
        <div class="depth-canvas rounded-2xl h-full overflow-hidden border border-neu-800/75 kinetic-entrance">
          {props.children}
        </div>
      </div>
    </div>
  );
};

export default TabLayout;
