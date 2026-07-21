import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const PILLARS = [
  {
    title: 'Deploy',
    body: 'Connect cameras and ingestion securely without breaking the reality of existing infrastructure.',
  },
  {
    title: 'Operate',
    body: 'Keep detection, control, and investigation in one coherent workflow for the operator.',
  },
  {
    title: 'Govern',
    body: 'Maintain evidence traceability, permissions, and audit confidence throughout the lifecycle.',
  },
];

const ArchitectureDiagram: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-feature-architecture-reveal',
        start: 'top 84%',
        y: 28,
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
    <section ref={sectionRef} class="v3-section v3-feature-architecture">
      <div class="v3-section-head v3-feature-architecture-reveal">
        <span class="v3-eyebrow">Production Model</span>
        <h2 class="v3-section-title">Designed to move cleanly from pilot to serious rollout.</h2>
      </div>

      <div class="v3-feature-architecture-grid">
        <For each={PILLARS}>
          {(pillar) => (
            <article class="v3-feature-architecture-card v3-feature-architecture-reveal">
              <h3>{pillar.title}</h3>
              <p>{pillar.body}</p>
            </article>
          )}
        </For>
      </div>
    </section>
  );
};

export default ArchitectureDiagram;
