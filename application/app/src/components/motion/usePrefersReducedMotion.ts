import { createSignal, onCleanup, onMount } from 'solid-js';

export function usePrefersReducedMotion() {
  const [reducedMotion, setReducedMotion] = createSignal(false);

  onMount(() => {
    const media = window.matchMedia('(prefers-reduced-motion: reduce)');
    const sync = () => setReducedMotion(media.matches);
    sync();
    media.addEventListener('change', sync);
    onCleanup(() => media.removeEventListener('change', sync));
  });

  return reducedMotion;
}
