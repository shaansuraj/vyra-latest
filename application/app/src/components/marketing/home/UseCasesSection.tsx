import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const USE_CASES = [
  {
    title: 'Manufacturing',
    description: 'Monitor PPE compliance, unsafe movement, and workstation blind spots across active production floors.',
    meta: 'Production oversight',
    tone: 'cyan',
  },
  {
    title: 'Warehousing',
    description: 'Track people, perimeter zones, and workflow violations in dense camera environments.',
    meta: 'Logistics control',
    tone: 'blue',
  },
  {
    title: 'Construction',
    description: 'Detect missing safety gear and surface incident-ready evidence across fast-changing sites.',
    meta: 'Field safety',
    tone: 'amber',
  },
  {
    title: 'Critical Infrastructure',
    description: 'Maintain high-trust monitoring where response certainty and evidence retention are non-negotiable.',
    meta: 'High-security operations',
    tone: 'mint',
  },
];

const UseCasesSection: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-usecase-reveal',
        start: 'top 82%',
        y: 36,
        stagger: 0.12,
        duration: 0.82,
      });
    }, sectionRef);

    onCleanup(() => {
      cleanupReveal();
      ctx.revert();
    });
  });

  return (
    <section ref={sectionRef} class="v3-section v3-usecases-section">
      <div class="v3-section-head v3-usecase-reveal">
        <span class="v3-eyebrow">Industries</span>
        <h2 class="v3-section-title">Built for operations across industries.</h2>
        <p class="v3-section-lead">
          The interface stays disciplined while the operational context changes from site to site.
        </p>
      </div>

      <div class="v3-usecases-grid">
        <For each={USE_CASES}>
          {(useCase) => (
            <article class={`v3-usecase-card v3-usecase-reveal is-${useCase.tone}`}>
              <div class="v3-usecase-schematic" aria-hidden="true">
                <span />
                <span />
                <span />
              </div>
              <div class="v3-usecase-copy">
                <span>{useCase.meta}</span>
                <h3>{useCase.title}</h3>
                <p>{useCase.description}</p>
              </div>
            </article>
          )}
        </For>
      </div>
    </section>
  );
};

export default UseCasesSection;
