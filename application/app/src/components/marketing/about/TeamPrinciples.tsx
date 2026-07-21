import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const PRINCIPLES = [
  {
    id: '01',
    title: 'Operational clarity',
    body: 'Interfaces should reduce cognitive load in the moments where monitoring turns into response.',
  },
  {
    id: '02',
    title: 'Engineering rigor',
    body: 'Inference, transport, and workflow controls need to be dependable enough for live deployments.',
  },
  {
    id: '03',
    title: 'Traceable intelligence',
    body: 'Every summary, detection, and action should stay tied to the underlying evidence trail.',
  },
];

export interface TeamPrinciplesProps {
  onContactSales: () => void;
  onBookDemo: () => void;
}

const TeamPrinciples: Component<TeamPrinciplesProps> = (props) => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-about-principles-reveal',
        start: 'top 84%',
        y: 26,
        stagger: 0.1,
        duration: 0.82,
      });
    }, sectionRef);

    onCleanup(() => {
      cleanupReveal();
      ctx.revert();
    });
  });

  return (
    <section ref={sectionRef} class="v3-section v3-about-principles">
      <div class="v3-section-head v3-about-principles-reveal">
        <span class="v3-eyebrow">Principles</span>
        <h2 class="v3-section-title">Built for teams that cannot afford blind spots.</h2>
      </div>

      <div class="v3-about-principles-grid">
        <For each={PRINCIPLES}>
          {(item) => (
            <article class="v3-about-principle-card v3-about-principles-reveal">
              <span>{item.id}</span>
              <h3>{item.title}</h3>
              <p>{item.body}</p>
            </article>
          )}
        </For>
      </div>

      <div class="v3-about-reality-card v3-about-principles-reveal">
        <div>
          <span>Deployment reality</span>
          <h3>From first walkthrough to production rollout, the bar stays the same.</h3>
          <p>
            VYRA is shaped around serious monitoring environments where security, uptime, and evidence confidence are not optional features.
          </p>
        </div>

        <div class="v3-about-principles-actions">
          <button class="v3-btn v3-btn-primary" onClick={props.onBookDemo}>
            Book Demo
          </button>
          <button class="v3-btn v3-btn-secondary" onClick={props.onContactSales}>
            Contact Sales
          </button>
          <a
            href="https://hexmontechnology.com"
            target="_blank"
            rel="noopener noreferrer"
            class="v3-btn v3-btn-text"
          >
            Explore Hexmon Technology
          </a>
        </div>
      </div>
    </section>
  );
};

export default TeamPrinciples;
