import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const FEATURES = [
  {
    id: '01',
    label: 'Command center',
    title: 'Keep cameras, workers, and relay state under one control system.',
    body: 'The command workspace lets operators act on the deployment itself, not just the output of detections.',
    points: ['Camera inventory and routing', 'Worker and node management', 'Deployment health visibility'],
    image: '/marketing/product/control-agents.webp',
  },
  {
    id: '02',
    label: 'Investigation',
    title: 'Move from alert to evidence without losing context.',
    body: 'Event review, source frames, AI summaries, and operator notes stay attached to the same timeline.',
    points: ['Chronological incident view', 'Evidence-linked AI summaries', 'Known vs unknown person context'],
    image: '/marketing/product/investigate-events.webp',
  },
  {
    id: '03',
    label: 'Analytics',
    title: 'See where risk and compliance trends are actually forming.',
    body: 'Analytics surfaces make patterns legible across cameras, violations, and time windows.',
    points: ['Severity and compliance trends', 'Camera-level performance insight', 'Daily briefing output'],
    image: '/marketing/product/analytics-overview.webp',
  },
  {
    id: '04',
    label: 'Ask AI',
    title: 'Query the operation in natural language and retrieve evidence-backed answers.',
    body: 'AI search sits on top of the evidence graph, helping operators locate moments and summarize what matters.',
    points: ['Natural-language scene search', 'Context-aware answer generation', 'Action-oriented prompts'],
    image: '/marketing/product/ask-ai.webp',
  },
];

export interface FeatureStickyCardsProps {
  onContactSales: () => void;
  onBookDemo: () => void;
}

const FeatureStickyCards: Component<FeatureStickyCardsProps> = (props) => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-feature-story-reveal',
        start: 'top 84%',
        y: 30,
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
    <section ref={sectionRef} class="v3-section v3-features-story-section">
      <div class="v3-section-head v3-feature-story-reveal">
        <span class="v3-eyebrow">Capabilities</span>
        <h2 class="v3-section-title">A guided product demo built into the page itself.</h2>
        <p class="v3-section-lead">
          Each chapter is now a self-contained demo slice, so the page keeps its balance on every screen size and never collapses into empty columns.
        </p>
      </div>

      <div class="v3-feature-demo-list">
        <For each={FEATURES}>
          {(feature, index) => (
            <article class={`v3-feature-demo-panel v3-feature-story-reveal ${index() % 2 === 1 ? 'is-reversed' : ''}`}>
              <figure class="v3-feature-demo-media">
                <div class="v3-feature-demo-frame">
                  <div class="v3-feature-demo-top">
                    <span>{feature.label}</span>
                    <span>{feature.id} / {FEATURES.length}</span>
                  </div>
                  <div class="v3-feature-demo-screen">
                    <img src={feature.image} alt={feature.title} loading="lazy" decoding="async" />
                  </div>
                </div>
              </figure>

              <div class="v3-feature-demo-copy">
                <span class="v3-feature-demo-id">{feature.id}</span>
                <span class="v3-feature-demo-label">{feature.label}</span>
                <h3>{feature.title}</h3>
                <p>{feature.body}</p>
                <ul>
                  <For each={feature.points}>
                    {(point) => (
                      <li>
                        <span />
                        {point}
                      </li>
                    )}
                  </For>
                </ul>
              </div>
            </article>
          )}
        </For>
      </div>

      <div class="v3-feature-demo-actions v3-feature-story-reveal">
        <button class="v3-btn v3-btn-primary" onClick={props.onBookDemo}>
          Book Demo
        </button>
        <button class="v3-btn v3-btn-secondary" onClick={props.onContactSales}>
          Contact Sales
        </button>
      </div>
    </section>
  );
};

export default FeatureStickyCards;
