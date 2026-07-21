import { type Component, For, createMemo, createSignal, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const OPERATOR_VIEWS = [
  {
    title: 'Analytics Breakdown',
    description: 'Severity clusters and compliance drift stay readable without leaving the operator surface.',
    img: '/marketing/product/analytics-breakdown.webp',
    tag: 'Trend surface',
    points: ['Severity clustering', 'Compliance drift', 'Daily risk weighting'],
  },
  {
    title: 'Investigate Timeline',
    description: 'Source frames, notes, and AI summaries remain aligned in one review flow.',
    img: '/marketing/product/investigate-timeline.webp',
    tag: 'Evidence timeline',
    points: ['Chronological evidence', 'Linked source frames', 'Operator note trail'],
  },
  {
    title: 'Control Cameras',
    description: 'Inspect routing, health, and deployment state from the same operational deck.',
    img: '/marketing/product/control-cameras.webp',
    tag: 'Command surface',
    points: ['Camera routing', 'Health monitoring', 'Relay assignment'],
  },
  {
    title: 'Daily Brief',
    description: 'The daily brief condenses incidents, compliance drift, and open actions into one readable handoff.',
    img: '/marketing/product/daily-brief.webp',
    tag: 'Shift briefing',
    points: ['Morning handoff', 'Escalation summary', 'Action-ready briefing'],
  },
];

const DashboardPreview: Component = () => {
  let sectionRef!: HTMLElement;
  let stageRef!: HTMLDivElement;
  const [activeIndex, setActiveIndex] = createSignal(0);
  const [tilt, setTilt] = createSignal({ x: 0, y: 0 });
  const activeView = createMemo(() => OPERATOR_VIEWS[activeIndex()]);
  let timer: ReturnType<typeof setInterval> | undefined;
  let resumeTimeout: ReturnType<typeof setTimeout> | undefined;

  const handlePointerMove = (event: PointerEvent) => {
    const rect = stageRef.getBoundingClientRect();
    const px = (event.clientX - rect.left) / Math.max(rect.width, 1);
    const py = (event.clientY - rect.top) / Math.max(rect.height, 1);
    setTilt({
      x: (0.5 - py) * 6,
      y: (px - 0.5) * 8,
    });
  };

  const resetTilt = () => setTilt({ x: 0, y: 0 });

  const startCycle = () => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    clearInterval(timer);
    timer = setInterval(() => {
      setActiveIndex((current) => (current + 1) % OPERATOR_VIEWS.length);
    }, 5600);
  };

  const selectView = (index: number) => {
    setActiveIndex(index);
    clearInterval(timer);
    clearTimeout(resumeTimeout);
    resumeTimeout = setTimeout(() => startCycle(), 8000);
  };

  onMount(() => {
    startCycle();

    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      onCleanup(() => {
        clearInterval(timer);
        clearTimeout(resumeTimeout);
      });
      return;
    }

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-preview-reveal',
        start: 'top 82%',
        y: 40,
        stagger: 0.12,
        duration: 0.82,
      });
    }, sectionRef);

    onCleanup(() => {
      cleanupReveal();
      ctx.revert();
      clearInterval(timer);
      clearTimeout(resumeTimeout);
    });
  });

  return (
    <section ref={sectionRef} class="v3-section v3-preview-section">
      <div class="v3-section-head v3-preview-head v3-preview-reveal">
        <span class="v3-eyebrow">Operations</span>
        <h2 class="v3-section-title">Every detail at operator fingertips.</h2>
        <p class="v3-section-lead">
          One large command surface keeps trends, evidence, controls, and shift-ready summaries within immediate reach.
        </p>
      </div>

      <div class="v3-preview-console v3-preview-reveal">
        <div class="v3-preview-selector" role="tablist" aria-label="Operator views">
          <For each={OPERATOR_VIEWS}>
            {(view, index) => (
              <button
                type="button"
                role="tab"
                aria-selected={index() === activeIndex()}
                class={`v3-preview-selector-item ${index() === activeIndex() ? 'is-active' : ''}`}
                onClick={() => selectView(index())}
              >
                <span>{view.tag}</span>
                <strong>{view.title}</strong>
              </button>
            )}
          </For>
        </div>

        <div class="v3-preview-theatre">
          <div
            ref={stageRef!}
            class="v3-device-stage v3-device-stage-hero"
            onPointerMove={handlePointerMove}
            onPointerLeave={resetTilt}
          >
            <div
              class="v3-command-device v3-command-device-large"
              style={{ transform: `perspective(2200px) rotateX(${tilt().x}deg) rotateY(${tilt().y}deg)` }}
            >
              <div class="v3-command-device-lid">
                <div class="v3-command-device-camera" />
                <div class="v3-command-device-screen">
                  <For each={OPERATOR_VIEWS}>
                    {(view, index) => (
                      <figure class={`v3-command-screen-layer ${index() === activeIndex() ? 'is-active' : ''}`}>
                        <img src={view.img} alt={view.title} loading={index() === 0 ? 'eager' : 'lazy'} decoding="async" />
                      </figure>
                    )}
                  </For>
                </div>
                <div class="v3-command-device-reflection" />
              </div>
              <div class="v3-command-device-base">
                <span />
              </div>
              <div class="v3-command-device-shadow" />
            </div>
          </div>
        </div>

        <div class="v3-preview-detail-row">
          <div class="v3-preview-detail-copy">
            <span>{activeView().tag}</span>
            <h3>{activeView().title}</h3>
            <p>{activeView().description}</p>
          </div>

          <div class="v3-preview-detail-points">
            <For each={activeView().points}>
              {(point) => <span>{point}</span>}
            </For>
          </div>
        </div>
      </div>
    </section>
  );
};

export default DashboardPreview;
