import { type Component, For, createSignal, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';
import TextReveal from '../effects/TextReveal';

gsap.registerPlugin(ScrollTrigger);

const HERO_METRICS = [
  { value: '<85 ms', label: 'Median decision latency' },
  { value: '24/7', label: 'Operator-grade monitoring' },
  { value: '99.2%', label: 'Evidence recall confidence' },
];

const HERO_PANELS = [
  {
    eyebrow: 'Command Center',
    title: 'Live camera control and node orchestration.',
    img: '/marketing/product/control-agents.webp',
    caption: 'Relay health, camera inventory, and worker governance in one surface.',
  },
  {
    eyebrow: 'Investigate',
    title: 'Trace incidents through one timeline.',
    img: '/marketing/product/investigate-events.webp',
    caption: 'Cross-camera evidence, summaries, and operator notes remain aligned.',
  },
  {
    eyebrow: 'Analytics',
    title: 'Turn detections into operational signal.',
    img: '/marketing/product/analytics-overview.webp',
    caption: 'Compliance drift, risk clusters, and daily intelligence stay visible.',
  },
];

const HERO_SIGNALS = [
  { label: 'Live feeds', value: '26 cameras online' },
  { label: 'Identity layer', value: 'Known and unknown tracking' },
  { label: 'Response loop', value: 'Alert to evidence in one surface' },
];

export interface HeroSectionProps {
  onBookDemo: () => void;
  onDashboard: () => void;
}

const HeroSection: Component<HeroSectionProps> = (props) => {
  let sectionRef!: HTMLElement;
  const [activePanel, setActivePanel] = createSignal(0);
  let timer: ReturnType<typeof setInterval> | undefined;

  onMount(() => {
    timer = setInterval(() => {
      setActivePanel((current) => (current + 1) % HERO_PANELS.length);
    }, 5200);

    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) {
      onCleanup(() => clearInterval(timer));
      return;
    }

    const ctx = gsap.context(() => {
      gsap.from(sectionRef.querySelectorAll('.v3-hero-reveal'), {
        opacity: 0,
        y: 28,
        stagger: 0.1,
        duration: 0.82,
        ease: 'power3.out',
      });

      const stage = sectionRef.querySelector('.v3-hero-stage-wrap');
      if (stage) {
        gsap.to(stage, {
          yPercent: 7,
          ease: 'none',
          scrollTrigger: {
            trigger: sectionRef,
            start: 'top top',
            end: 'bottom top',
            scrub: 1,
          },
        });
      }
    }, sectionRef);

    onCleanup(() => {
      ctx.revert();
      clearInterval(timer);
    });
  });

  return (
    <section ref={sectionRef} class="v3-hero">
      <div class="v3-hero-grid">
        <div class="v3-hero-copy">
          <span class="v3-eyebrow v3-hero-reveal">Operational Vision in Depth</span>

          <TextReveal
            text="See everything. Miss nothing."
            tag="h1"
            mode="word"
            class="v3-hero-title v3-hero-reveal"
          />

          <p class="v3-hero-lead v3-hero-reveal">
            VYRA turns ordinary CCTV feeds into a precise operational instrument for detection, compliance,
            investigation, and rapid response.
          </p>

          <div class="v3-hero-actions v3-hero-reveal">
            <button class="v3-btn v3-btn-primary v3-btn-lg" onClick={props.onBookDemo}>
              Book Demo
            </button>
            <button class="v3-btn v3-btn-secondary" onClick={props.onDashboard}>
              Login
            </button>
          </div>

          <div class="v3-hero-metrics v3-hero-reveal">
            <For each={HERO_METRICS}>
              {(metric) => (
                <div class="v3-hero-metric">
                  <strong>{metric.value}</strong>
                  <span>{metric.label}</span>
                </div>
              )}
            </For>
          </div>
        </div>

        <div class="v3-hero-stage-wrap v3-hero-reveal">
          <div class="v3-hero-visual-shell">
            <div class="v3-hero-optic" aria-hidden="true">
              <span class="v3-hero-optic-ring v3-hero-optic-ring-outer" />
              <span class="v3-hero-optic-ring v3-hero-optic-ring-mid" />
              <span class="v3-hero-optic-ring v3-hero-optic-ring-inner" />
              <span class="v3-hero-optic-arc v3-hero-optic-arc-left" />
              <span class="v3-hero-optic-arc v3-hero-optic-arc-right" />
              <div class="v3-hero-optic-core">
                <div class="v3-hero-optic-pupil" />
              </div>
            </div>
            <div class="v3-hero-stage-stack">
              <figure class="v3-hero-command-card">
                <div class="v3-hero-command-head">
                  <span>{HERO_PANELS[activePanel()].eyebrow}</span>
                  <span>{activePanel() + 1} / {HERO_PANELS.length}</span>
                </div>

                <div class="v3-hero-command-screen">
                  <For each={HERO_PANELS}>
                    {(panel, index) => (
                      <div class={`v3-hero-command-layer ${index() === activePanel() ? 'is-active' : ''}`}>
                        <img
                          src={panel.img}
                          alt={panel.title}
                          loading={index() === 0 ? 'eager' : 'lazy'}
                          decoding="async"
                        />
                      </div>
                    )}
                  </For>
                </div>

                <figcaption class="v3-hero-command-copy">
                  <strong>{HERO_PANELS[activePanel()].title}</strong>
                  <p>{HERO_PANELS[activePanel()].caption}</p>
                </figcaption>
              </figure>

              <div class="v3-hero-signal-strip">
                <For each={HERO_SIGNALS}>
                  {(signal) => (
                    <div class="v3-hero-signal-pill">
                      <span>{signal.label}</span>
                      <strong>{signal.value}</strong>
                    </div>
                  )}
                </For>
              </div>
            </div>
          </div>
        </div>
      </div>

      <div class="v3-scroll-hint" aria-hidden="true">
        <span />
      </div>
    </section>
  );
};

export default HeroSection;
