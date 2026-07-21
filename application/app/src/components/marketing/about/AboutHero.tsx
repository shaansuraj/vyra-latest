import { type Component, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';

const AboutHero: Component = () => {
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
    <section ref={sectionRef} class="v3-page-hero v3-about-hero">
      <div class="v3-page-hero-copy">
        <span class="v3-eyebrow v3-page-hero-reveal">About VYRA</span>
        <h1 class="v3-page-hero-reveal">
          Built for teams that run critical operations under real-world pressure.
        </h1>
        <p class="v3-page-hero-reveal">
          VYRA comes from a simple belief: camera systems should help operators act with clarity, not drown them in disconnected feeds and low-context alerts.
        </p>
      </div>

      <aside class="v3-about-hero-panel v3-page-hero-reveal">
        <span>Why VYRA exists</span>
        <p>
          Modern sites already have cameras. The gap is not visibility, but operational meaning. We build VYRA to turn footage into trusted signal for the people responsible for response.
        </p>
        <div class="v3-about-hero-metrics">
          <div>
            <strong>Product + AI</strong>
            <span>One integrated command surface</span>
          </div>
          <div>
            <strong>Deployment-first</strong>
            <span>Built for live environments, not lab demos</span>
          </div>
        </div>
      </aside>
    </section>
  );
};

export default AboutHero;
