import { type JSX, createSignal, onCleanup, onMount } from 'solid-js';
import { usePrefersReducedMotion } from './usePrefersReducedMotion';

export default function RevealOnScroll(props: {
  children: JSX.Element;
  class?: string;
  threshold?: number;
  once?: boolean;
}) {
  let ref!: HTMLDivElement;
  const [visible, setVisible] = createSignal(false);
  const reducedMotion = usePrefersReducedMotion();

  onMount(() => {
    if (reducedMotion()) {
      setVisible(true);
      return;
    }

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            setVisible(true);
            if (props.once !== false) observer.unobserve(entry.target);
          } else if (props.once === false) {
            setVisible(false);
          }
        }
      },
      { threshold: props.threshold ?? 0.12 }
    );

    observer.observe(ref);
    onCleanup(() => observer.disconnect());
  });

  return (
    <div ref={ref} class={`reveal-on-scroll ${visible() ? 'is-visible' : ''} ${props.class || ''}`}>
      {props.children}
    </div>
  );
}
