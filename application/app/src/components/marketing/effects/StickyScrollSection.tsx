import { type Component, type ParentProps, onMount, onCleanup, createSignal } from 'solid-js';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';

gsap.registerPlugin(ScrollTrigger);

export interface StickyScrollSectionProps extends ParentProps {
  class?: string;
  /** Number of "pages" worth of scroll distance */
  pages?: number;
  /** Callback with progress 0-1 */
  onProgress?: (progress: number) => void;
}

const StickyScrollSection: Component<StickyScrollSectionProps> = (props) => {
  let wrapperRef!: HTMLDivElement;
  let pinnedRef!: HTMLDivElement;
  const pages = props.pages ?? 4;
  const [staticMode, setStaticMode] = createSignal(false);

  onMount(() => {
    const reduceMotionQuery = window.matchMedia('(prefers-reduced-motion: reduce)');
    let trigger: ScrollTrigger | null = null;

    const killTrigger = () => {
      if (trigger) {
        trigger.kill();
        trigger = null;
      }
    };

    const createTrigger = () => {
      killTrigger();
      trigger =
      ScrollTrigger.create({
        trigger: wrapperRef,
        start: 'top top',
        end: `+=${pages * 100}%`,
        pin: pinnedRef,
        pinSpacing: true,
        scrub: 1,
        invalidateOnRefresh: true,
        anticipatePin: 1,
        onUpdate: (self) => {
          const progress = Number.isFinite(self.progress) ? self.progress : 0;
          props.onProgress?.(progress);
        },
      });
      requestAnimationFrame(() => ScrollTrigger.refresh());
    };

    const applyMode = () => {
      const shouldStatic = window.innerWidth <= 1080 || reduceMotionQuery.matches;
      setStaticMode(shouldStatic);
      if (shouldStatic) {
        killTrigger();
        props.onProgress?.(0);
      } else {
        createTrigger();
      }
    };

    applyMode();
    window.addEventListener('resize', applyMode, { passive: true });
    if ('addEventListener' in reduceMotionQuery) {
      reduceMotionQuery.addEventListener('change', applyMode);
    } else {
      reduceMotionQuery.addListener(applyMode);
    }

    onCleanup(() => {
      killTrigger();
      window.removeEventListener('resize', applyMode);
      if ('removeEventListener' in reduceMotionQuery) {
        reduceMotionQuery.removeEventListener('change', applyMode);
      } else {
        reduceMotionQuery.removeListener(applyMode);
      }
    });
  });

  return (
    <div
      ref={wrapperRef}
      class={`v3-sticky-wrapper ${props.class || ''} ${staticMode() ? 'is-static' : ''}`}
      style={{ height: staticMode() ? 'auto' : `${(pages + 1) * 100}vh` }}
    >
      <div ref={pinnedRef} class={`v3-sticky-pinned ${staticMode() ? 'is-static' : ''}`}>
        {props.children}
      </div>
    </div>
  );
};

export default StickyScrollSection;
