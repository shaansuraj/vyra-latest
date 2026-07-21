import { type Component, type ParentProps } from 'solid-js';
import MarketingSignalField from './effects/MarketingSignalField';
import ScrollProgress from './effects/ScrollProgress';

export interface MarketingShellProps extends ParentProps {
  class?: string;
}

const MarketingShell: Component<MarketingShellProps> = (props) => {
  return (
    <div class={`v3-shell ${props.class || ''}`}>
      <ScrollProgress />
      <div class="v3-shell-backdrop" aria-hidden="true" />
      <div class="v3-shell-atmosphere" aria-hidden="true" />
      <MarketingSignalField />
      <div class="v3-shell-grain" aria-hidden="true" />
      {props.children}
    </div>
  );
};

export default MarketingShell;
