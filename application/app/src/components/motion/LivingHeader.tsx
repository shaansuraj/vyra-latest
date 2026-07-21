import { type JSX, createSignal, onCleanup, onMount } from 'solid-js';
import { usePrefersReducedMotion } from './usePrefersReducedMotion';

export default function LivingHeader(props: {
  class?: string;
  children: JSX.Element;
}) {
  const [condensed, setCondensed] = createSignal(false);
  const reducedMotion = usePrefersReducedMotion();

  onMount(() => {
    if (reducedMotion()) return;
    const onScroll = () => setCondensed(window.scrollY > 18);
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
    onCleanup(() => window.removeEventListener('scroll', onScroll));
  });

  return (
    <div class={`living-header ${condensed() ? 'is-condensed' : ''} ${props.class || ''}`}>
      {props.children}
    </div>
  );
}
