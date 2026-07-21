import { type JSX, createSignal, onCleanup, onMount } from 'solid-js';
import { usePrefersReducedMotion } from './usePrefersReducedMotion';

export default function MagneticButton(props: {
  class?: string;
  children: JSX.Element;
  onClick?: JSX.EventHandlerUnion<HTMLButtonElement, MouseEvent>;
  disabled?: boolean;
  title?: string;
  type?: 'button' | 'submit' | 'reset';
}) {
  let ref!: HTMLButtonElement;
  const [shift, setShift] = createSignal({ x: 0, y: 0, scale: 1 });
  const reducedMotion = usePrefersReducedMotion();

  onMount(() => {
    if (reducedMotion()) return;
    if (!window.matchMedia('(hover: hover) and (pointer: fine)').matches) return;

    const handleMove = (ev: MouseEvent) => {
      if (props.disabled) return;
      const rect = ref.getBoundingClientRect();
      const cx = rect.left + rect.width / 2;
      const cy = rect.top + rect.height / 2;
      const dx = ev.clientX - cx;
      const dy = ev.clientY - cy;
      const distance = Math.sqrt(dx * dx + dy * dy);

      if (distance < 50) {
        setShift({
          x: dx * 0.08,
          y: dy * 0.08,
          scale: 1.05,
        });
      } else {
        setShift({ x: 0, y: 0, scale: 1 });
      }
    };

    const handleLeave = () => setShift({ x: 0, y: 0, scale: 1 });

    window.addEventListener('mousemove', handleMove);
    ref.addEventListener('mouseleave', handleLeave);

    onCleanup(() => {
      window.removeEventListener('mousemove', handleMove);
      ref.removeEventListener('mouseleave', handleLeave);
    });
  });

  return (
    <button
      ref={ref}
      type={props.type || 'button'}
      title={props.title}
      class={`magnetic-button ${props.class || ''}`}
      onClick={props.onClick}
      disabled={props.disabled}
      style={{ transform: `translate3d(${shift().x}px, ${shift().y}px, 0) scale(${shift().scale})` }}
    >
      {props.children}
    </button>
  );
}
