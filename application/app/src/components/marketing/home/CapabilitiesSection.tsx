import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';
import { revealOnEnter } from '../effects/revealOnEnter';

gsap.registerPlugin(ScrollTrigger);

const CAPABILITIES = [
  {
    title: 'Real-time Detection',
    description: 'Ingest live CCTV feeds and classify activity with low-latency object, person, and behavior awareness.',
    detail: 'Signal priority',
    stat: 'Sub-second alerting',
    points: ['Live object tracking', 'Cross-camera event stitching', 'Confidence-ranked incident feed'],
    icon: (
      <svg viewBox="0 0 32 32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
        <circle data-stroke cx="16" cy="16" r="3.5" />
        <path data-stroke d="M16 5.5C8.6 5.5 3 16 3 16s5.6 10.5 13 10.5S29 16 29 16 23.4 5.5 16 5.5z" />
        <path data-stroke d="M16 10.5v-2M16 23.5v-2M10.5 16h-2M23.5 16h-2" />
      </svg>
    ),
  },
  {
    title: 'Safety Compliance',
    description: 'Measure PPE adherence, detect violations, and surface non-compliant behavior before it becomes a report.',
    detail: 'Compliance layer',
    stat: '14-class PPE model set',
    points: ['Hardhat and vest checks', 'Worker-by-worker overlays', 'Escalation-ready violation evidence'],
    icon: (
      <svg viewBox="0 0 32 32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
        <path data-stroke d="M16 4.5 6 9v7.2c0 5.7 4.3 10.7 10 12.3 5.7-1.6 10-6.6 10-12.3V9L16 4.5z" />
        <path data-stroke d="m12.2 16.3 2.8 2.8 5.1-5.1" />
        <path data-stroke d="M10 13.2c1.2-.8 3.2-1.4 6-1.4s4.8.6 6 1.4" />
      </svg>
    ),
  },
  {
    title: 'Incident Intelligence',
    description: 'Turn detections into readable operational context with AI summaries, evidence packets, and response-ready timelines.',
    detail: 'Decision support',
    stat: 'Narrative evidence in one click',
    points: ['Natural-language scene summaries', 'Operator notes with source frames', 'Timeline export for review'],
    icon: (
      <svg viewBox="0 0 32 32" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
        <path data-stroke d="M10 9.5h12a3 3 0 0 1 3 3v7a3 3 0 0 1-3 3h-5.5L11 26v-3.5H10a3 3 0 0 1-3-3v-7a3 3 0 0 1 3-3z" />
        <path data-stroke d="M12 14h8M12 18h5" />
        <path data-stroke d="M22.5 10.5v-2" />
      </svg>
    ),
  },
];

const CapabilitiesSection: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-capability-reveal',
        start: 'top 82%',
        y: 36,
        stagger: 0.12,
        duration: 0.8,
      });

      sectionRef.querySelectorAll<SVGGeometryElement>('.v3-capability-icon [data-stroke]').forEach((stroke) => {
        const length = typeof stroke.getTotalLength === 'function' ? stroke.getTotalLength() : 60;
        gsap.set(stroke, {
          strokeDasharray: length,
          strokeDashoffset: length,
        });

        gsap.to(stroke, {
          strokeDashoffset: 0,
          duration: 1.05,
          ease: 'power2.out',
          scrollTrigger: {
            trigger: stroke.closest('.v3-capability-card'),
            start: 'top 88%',
            once: true,
          },
        });
      });
    }, sectionRef);

    onCleanup(() => {
      cleanupReveal();
      ctx.revert();
    });
  });

  return (
    <section ref={sectionRef} class="v3-section v3-capabilities-section">
      <div class="v3-section-head v3-capability-reveal">
        <span class="v3-eyebrow">Intelligence</span>
        <h2 class="v3-section-title">Intelligent monitoring at every layer.</h2>
        <p class="v3-section-lead">
          From live perception to investigation handoff, VYRA keeps the signal chain visible, precise, and operator-ready.
        </p>
      </div>

      <div class="v3-capabilities-grid">
        <For each={CAPABILITIES}>
          {(capability, index) => (
            <article class={`v3-capability-card v3-capability-reveal ${index() === 0 ? 'is-primary' : ''}`}>
              <div class="v3-capability-topline">
                <span>{capability.detail}</span>
                <span>{capability.stat}</span>
              </div>

              <div class="v3-capability-icon">{capability.icon}</div>
              <h3>{capability.title}</h3>
              <p>{capability.description}</p>

              <ul class="v3-capability-list">
                <For each={capability.points}>
                  {(point) => (
                    <li>
                      <span />
                      {point}
                    </li>
                  )}
                </For>
              </ul>
            </article>
          )}
        </For>
      </div>
    </section>
  );
};

export default CapabilitiesSection;
