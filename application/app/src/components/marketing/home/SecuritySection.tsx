import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const TRUST_SIGNALS = [
  {
    title: 'On-premise deployment',
    description: 'Keep inference, storage, and operational review inside infrastructure you already govern.',
  },
  {
    title: 'Encrypted data',
    description: 'Transport and evidence handling stay encrypted between camera ingest, relay, and review surfaces.',
  },
  {
    title: 'RBAC',
    description: 'Operators, investigators, and admins each get scoped access with clear separation of duties.',
  },
  {
    title: 'Audit trail',
    description: 'Every review action, escalation, and outcome is captured for compliance and after-action analysis.',
  },
];

const SecuritySection: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-security-reveal',
        start: 'top 82%',
        y: 30,
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
    <section ref={sectionRef} class="v3-section v3-security-section">
      <div class="v3-section-head v3-security-reveal">
        <span class="v3-eyebrow">Trust</span>
        <h2 class="v3-section-title">Enterprise-grade from day one.</h2>
        <p class="v3-section-lead">
          Security is part of the operating model, not a sales checkbox added later.
        </p>
      </div>

      <div class="v3-security-layout">
        <div class="v3-security-list">
          <For each={TRUST_SIGNALS}>
            {(signal) => (
              <article class="v3-security-item v3-security-reveal">
                <span class="v3-security-bullet" />
                <div>
                  <h3>{signal.title}</h3>
                  <p>{signal.description}</p>
                </div>
              </article>
            )}
          </For>
        </div>

        <div class="v3-security-visual v3-security-reveal" aria-hidden="true">
          <div class="v3-security-radial" />
          <svg class="v3-security-crest" viewBox="0 0 220 260" fill="none">
            <path d="M110 12 190 48v78c0 56.5-29.8 97.5-80 121.5C59.8 223.5 30 182.5 30 126V48l80-36z" />
            <path d="M110 34 170 61v60.5c0 42.8-22.5 74.5-60 92.5-37.5-18-60-49.7-60-92.5V61l60-27z" />
            <path d="m86 126 18 18 34-34" />
            <circle cx="110" cy="126" r="48" />
          </svg>
          <div class="v3-security-badge v3-security-badge-top">Encrypted transport</div>
          <div class="v3-security-badge v3-security-badge-bottom">Audit-ready evidence trail</div>
        </div>
      </div>
    </section>
  );
};

export default SecuritySection;
