import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const WORKFLOWS = [
  {
    id: '01',
    title: 'Command Center',
    desc: 'Coordinate cameras, workers, and relay health from one operations deck.',
    img: '/marketing/product/control-agents.webp',
    meta: 'Command state',
    callout: 'Live node assignment, camera routing, and uptime visibility without leaving the control surface.',
    chips: ['Workers online', 'Camera inventory', 'Relay status'],
  },
  {
    id: '02',
    title: 'Investigate',
    desc: 'Track incidents through evidence, identities, and timelines in one step-by-step surface.',
    img: '/marketing/product/investigate-events.webp',
    meta: 'Evidence timeline',
    callout: 'Every event keeps source frames, AI summary, and operator context attached to the same record.',
    chips: ['Event chronology', 'Source frames', 'Operator notes'],
  },
  {
    id: '03',
    title: 'Analytics',
    desc: 'Understand recurring patterns across cameras, violations, and severity shifts.',
    img: '/marketing/product/analytics-overview.webp',
    meta: 'Performance layer',
    callout: 'Use real operational data to see where risk concentrates and how compliance changes over time.',
    chips: ['Violation trends', 'Camera performance', 'Severity breakdown'],
  },
  {
    id: '04',
    title: 'Ask AI',
    desc: 'Query what happened in natural language and retrieve evidence-ready answers.',
    img: '/marketing/product/ask-ai.webp',
    meta: 'AI query',
    callout: 'Search across incidents, detections, and notes as if you were briefing an operator in the room.',
    chips: ['Natural language', 'Context retrieval', 'Action prompts'],
  },
];

const ProductShowcase: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-showcase-reveal',
        start: 'top 84%',
        y: 34,
        stagger: 0.08,
        duration: 0.78,
      });
    }, sectionRef);

    onCleanup(() => {
      cleanupReveal();
      ctx.revert();
    });
  });

  return (
    <section ref={sectionRef} class="v3-section v3-showcase-section">
      <div class="v3-section-head v3-showcase-reveal">
        <span class="v3-eyebrow">Control</span>
        <h2 class="v3-section-title">One surface for control, investigation, and response.</h2>
        <p class="v3-section-lead">
          The product story now advances as a continuous scroll sequence. Each stage stays visible in its own full composition instead of pinning a dead side of the screen.
        </p>
      </div>

      <div class="v3-showcase-flow">
        <For each={WORKFLOWS}>
          {(workflow, index) => (
            <article class={`v3-showcase-panel v3-showcase-reveal ${index() % 2 === 1 ? 'is-reversed' : ''}`}>
              <div class="v3-showcase-panel-copy">
                <span class="v3-showcase-panel-index">{workflow.id}</span>
                <span class="v3-showcase-panel-meta">{workflow.meta}</span>
                <h3>{workflow.title}</h3>
                <p>{workflow.callout}</p>
                <div class="v3-showcase-chip-row">
                  <For each={workflow.chips}>
                    {(chip) => <span>{chip}</span>}
                  </For>
                </div>
              </div>

              <figure class="v3-showcase-panel-media">
                <div class="v3-showcase-screen-shell">
                  <div class="v3-showcase-screen-top">
                    <span>{workflow.meta}</span>
                    <span>{workflow.id} / {WORKFLOWS.length}</span>
                  </div>
                  <div class="v3-showcase-screen-frame">
                    <img src={workflow.img} alt={workflow.title} loading="lazy" decoding="async" />
                  </div>
                </div>
                <figcaption class="v3-showcase-panel-caption">{workflow.desc}</figcaption>
              </figure>
            </article>
          )}
        </For>
      </div>
    </section>
  );
};

export default ProductShowcase;
