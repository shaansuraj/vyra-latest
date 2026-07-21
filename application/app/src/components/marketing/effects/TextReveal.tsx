import { type Component, type JSX, onMount, onCleanup, For, createMemo } from 'solid-js';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';
import { usePrefersReducedMotion } from '../../motion/usePrefersReducedMotion';

gsap.registerPlugin(ScrollTrigger);

export interface TextRevealProps {
  text: string;
  class?: string;
  tag?: 'h1' | 'h2' | 'h3' | 'p' | 'span';
  mode?: 'letter' | 'word';
  stagger?: number;
  duration?: number;
  /** Use scroll-driven reveal instead of mount-based */
  scroll?: boolean;
  /** Delay before animation starts (ms) */
  delay?: number;
  style?: JSX.CSSProperties;
}

const TextReveal: Component<TextRevealProps> = (props) => {
  let containerRef!: HTMLElement;
  const tag = createMemo(() => props.tag || 'h1');
  const mode = createMemo(() => props.mode || 'letter');
  const stagger = createMemo(() => props.stagger ?? (mode() === 'letter' ? 0.03 : 0.08));
  const duration = props.duration ?? 0.9;
  const delay = (props.delay ?? 0) / 1000;
  const reducedMotion = usePrefersReducedMotion();

  const units = createMemo(() => {
    if (mode() === 'word') {
      return props.text.split(' ').map((w) => ({ text: w, isSpace: false }));
    }
    // letter mode - preserve spaces as non-animated units
    return props.text.split('').map((ch) => ({
      text: ch === ' ' ? '\u00A0' : ch,
      isSpace: ch === ' ',
    }));
  });

  onMount(() => {
    const chars = containerRef.querySelectorAll('.v3-tr-unit');
    if (!chars.length || reducedMotion()) return;

    const ctx = gsap.context(() => {
      const tl = gsap.timeline({
        delay,
        ...(props.scroll
          ? {
              scrollTrigger: {
                trigger: containerRef,
                start: 'top 85%',
                end: 'top 40%',
                scrub: 1,
              },
            }
          : {}),
      });

      tl.from(chars, {
        opacity: 0,
        y: mode() === 'letter' ? 30 : 50,
        stagger: stagger(),
        duration,
        ease: 'power3.out',
      });
    }, containerRef);

    onCleanup(() => ctx.revert());
  });

  const content = () => (
    <For each={units()}>
      {(unit) => (
        <span
          class={`v3-tr-unit ${unit.isSpace ? 'is-space' : ''}`}
          style={{
            display: 'inline-block',
            'will-change': 'transform, opacity, filter',
            ...(mode() === 'word' ? { 'margin-right': '0.3em' } : {}),
          }}
        >
          {unit.text}
        </span>
      )}
    </For>
  );

  const className = `v3-tr-container ${props.class || ''}`;

  if (tag() === 'h2') {
    return (
      <h2 ref={containerRef} class={className} style={props.style}>
        {content()}
      </h2>
    );
  }

  if (tag() === 'h3') {
    return (
      <h3 ref={containerRef} class={className} style={props.style}>
        {content()}
      </h3>
    );
  }

  if (tag() === 'p') {
    return (
      <p ref={containerRef} class={className} style={props.style}>
        {content()}
      </p>
    );
  }

  if (tag() === 'span') {
    return (
      <span ref={containerRef} class={className} style={props.style}>
        {content()}
      </span>
    );
  }

  return (
    <h1 ref={containerRef} class={className} style={props.style}>
      {content()}
    </h1>
  );
};

export default TextReveal;
