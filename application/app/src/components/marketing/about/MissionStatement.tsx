import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const TIMELINE = [
  {
    step: '01',
    title: 'Observe',
    body: 'Replace passive footage review with live machine perception that keeps signal attached to the frame.',
  },
  {
    step: '02',
    title: 'Classify',
    body: 'Turn detections into operational meaning with confidence, severity, and compliance context.',
  },
  {
    step: '03',
    title: 'Respond',
    body: 'Preserve the evidence chain so operators can investigate and act without context loss.',
  },
];

const MissionStatement: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-about-reveal',
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
    <section ref={sectionRef} class="v3-section v3-about-story">
      <div class="v3-section-head v3-about-reveal">
        <span class="v3-eyebrow">Philosophy</span>
        <h2 class="v3-section-title">Credible, visionary, and grounded in operations.</h2>
      </div>

      <div class="v3-about-story-grid">
        <article class="v3-about-manifesto v3-about-reveal">
          <span>Deployment reality</span>
          <h3>We design for the conditions operators actually work in.</h3>
          <p>
            That means latency matters. Explainability matters. Auditability matters. Interfaces need to stay calm under pressure and evidence must survive handoffs without fragmentation.
          </p>
        </article>

        <div class="v3-about-timeline">
          <For each={TIMELINE}>
            {(item) => (
              <article class="v3-about-timeline-item v3-about-reveal">
                <span>{item.step}</span>
                <div>
                  <h3>{item.title}</h3>
                  <p>{item.body}</p>
                </div>
              </article>
            )}
          </For>
        </div>
      </div>
    </section>
  );
};

export default MissionStatement;
