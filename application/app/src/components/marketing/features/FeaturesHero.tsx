import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';

const FEATURE_STACK = [
  { title: 'Detect', body: 'Live PPE, person, and behavior signals from the same feed.' },
  { title: 'Investigate', body: 'Source frames, identities, and notes remain attached to the event.' },
  { title: 'Coordinate', body: 'Cameras, relays, and operators stay inside one command surface.' },
  { title: 'Explain', body: 'Ask AI for incident context and retrieve evidence-backed answers.' },
];

const FeaturesHero: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    const ctx = gsap.context(() => {
      gsap.from(sectionRef.querySelectorAll('.v3-page-hero-reveal'), {
        opacity: 0,
        y: 28,
        stagger: 0.09,
        duration: 0.82,
        ease: 'power3.out',
      });
    }, sectionRef);

    onCleanup(() => ctx.revert());
  });

  return (
    <section ref={sectionRef} class="v3-page-hero v3-features-hero">
      <div class="v3-page-hero-copy">
        <span class="v3-eyebrow v3-page-hero-reveal">Feature Architecture</span>
        <h1 class="v3-page-hero-reveal">A guided product story through the VYRA command surface.</h1>
        <p class="v3-page-hero-reveal">
          Each capability is built as part of the same operational system, from detection pipelines to investigation and response.
        </p>
      </div>

      <aside class="v3-features-hero-panel v3-page-hero-reveal">
        <span>Feature stack</span>
        <div class="v3-features-hero-stack">
          <For each={FEATURE_STACK}>
            {(item) => (
              <div class="v3-features-hero-item">
                <strong>{item.title}</strong>
                <p>{item.body}</p>
              </div>
            )}
          </For>
        </div>
      </aside>
    </section>
  );
};

export default FeaturesHero;
