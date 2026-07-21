import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const FLOW = [
  {
    title: 'Cameras',
    body: 'RTSP and edge feeds connect into the system without changing how sites already operate.',
    icon: (
      <svg viewBox="0 0 32 32" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round">
        <rect x="4" y="8" width="18" height="14" rx="2" />
        <path d="M22 13l6-3v12l-6-3" />
      </svg>
    ),
  },
  {
    title: 'Relay',
    body: 'Secure ingestion, streaming orchestration, and deployment-aware transport handling.',
    icon: (
      <svg viewBox="0 0 32 32" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round">
        <rect x="8" y="4" width="16" height="24" rx="2" />
        <path d="M12 10h8M12 14h8M12 18h5" />
        <circle cx="16" cy="24" r="1.5" />
      </svg>
    ),
  },
  {
    title: 'AI Engine',
    body: 'Detection, classification, summarization, and signal scoring stay close to the evidence source.',
    icon: (
      <svg viewBox="0 0 32 32" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round">
        <rect x="6" y="6" width="20" height="20" rx="3" />
        <rect x="11" y="11" width="10" height="10" rx="1.2" />
        <path d="M16 6V3M16 29v-3M6 16H3M29 16h-3" />
      </svg>
    ),
  },
  {
    title: 'Dashboard',
    body: 'Operators review, investigate, and respond from one secure command interface.',
    icon: (
      <svg viewBox="0 0 32 32" fill="none" stroke="currentColor" stroke-width="1.65" stroke-linecap="round" stroke-linejoin="round">
        <rect x="4" y="5" width="24" height="18" rx="2" />
        <path d="M4 19h24M12 23h8v3H12zM10 28h12" />
      </svg>
    ),
  },
];

const BADGES = ['Go', 'TypeScript', 'Python', 'YOLO', 'WebRTC', 'SQLite'];

const ArchitectureSection: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-architecture-reveal',
        start: 'top 84%',
        y: 32,
        stagger: 0.1,
        duration: 0.8,
      });
    }, sectionRef);

    onCleanup(() => {
      cleanupReveal();
      ctx.revert();
    });
  });

  return (
    <section ref={sectionRef} class="v3-section v3-architecture-section">
      <div class="v3-section-head v3-architecture-reveal">
        <span class="v3-eyebrow">Infrastructure</span>
        <h2 class="v3-section-title">Designed for production-grade deployments.</h2>
        <p class="v3-section-lead">
          Camera streams, secure relay, model inference, and the operator dashboard stay composed as one production pipeline.
        </p>
      </div>

      <div class="v3-architecture-flow v3-architecture-reveal">
        <For each={FLOW}>
          {(step, index) => (
            <>
              <article class="v3-architecture-node">
                <div class="v3-architecture-node-icon">{step.icon}</div>
                <div class="v3-architecture-node-copy">
                  <strong>{step.title}</strong>
                  <p>{step.body}</p>
                </div>
              </article>

              {index() < FLOW.length - 1 && (
                <div class="v3-architecture-link" aria-hidden="true">
                  <span class="v3-architecture-link-line" />
                  <span class="v3-architecture-link-dot" />
                </div>
              )}
            </>
          )}
        </For>
      </div>

      <div class="v3-architecture-badges v3-architecture-reveal">
        <For each={BADGES}>
          {(badge) => <span>{badge}</span>}
        </For>
      </div>
    </section>
  );
};

export default ArchitectureSection;
