import { type Component, type JSX, type ParentProps } from 'solid-js';

export interface GlassCardProps extends ParentProps {
  class?: string;
  depth?: 1 | 2 | 3;
  holo?: boolean;
  style?: JSX.CSSProperties;
  onClick?: () => void;
}

const GlassCard: Component<GlassCardProps> = (props) => {
  const depth = props.depth ?? 2;

  return (
    <div
      class={`v3-glass-card v3-glass-${depth} ${props.holo ? 'is-holo' : ''} ${props.class || ''}`}
      style={props.style}
      onClick={props.onClick}
    >
      {props.children}
    </div>
  );
};

export default GlassCard;
