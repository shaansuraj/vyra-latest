import { createEffect, createSignal, onCleanup } from 'solid-js';
import { usePrefersReducedMotion } from './usePrefersReducedMotion';

function easeOutCubic(t: number) {
  return 1 - Math.pow(1 - t, 3);
}

export default function CountUpNumber(props: {
  value: number;
  durationMs?: number;
  class?: string;
  formatter?: (value: number) => string;
}) {
  const [display, setDisplay] = createSignal(0);
  const [flashClass, setFlashClass] = createSignal('');
  const reducedMotion = usePrefersReducedMotion();
  let frame = 0;

  const format = (value: number) => {
    if (props.formatter) return props.formatter(value);
    return value.toLocaleString();
  };

  createEffect(() => {
    const target = props.value;
    const from = display();

    if (target !== from) {
      setFlashClass(target > from ? 'count-flash-positive' : 'count-flash-negative');
      window.setTimeout(() => setFlashClass(''), 320);
    }

    if (reducedMotion()) {
      setDisplay(target);
      return;
    }

    cancelAnimationFrame(frame);
    const started = performance.now();
    const duration = props.durationMs ?? 620;

    const tick = (now: number) => {
      const progress = Math.min(1, (now - started) / duration);
      const eased = easeOutCubic(progress);
      const next = Math.round(from + (target - from) * eased);
      setDisplay(next);
      if (progress < 1) frame = requestAnimationFrame(tick);
    };

    frame = requestAnimationFrame(tick);
  });

  onCleanup(() => cancelAnimationFrame(frame));

  return <span class={`kpi-value ${flashClass()} ${props.class || ''}`}>{format(display())}</span>;
}
