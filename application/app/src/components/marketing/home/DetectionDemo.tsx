import { type Component, For, onCleanup, onMount } from 'solid-js';
import { gsap } from 'gsap';
import { revealOnEnter } from '../effects/revealOnEnter';

const BOUNDING_BOXES = [
  {
    label: 'Satwik',
    state: 'No hardhat · No goggles',
    confidence: '96%',
    top: '44.5%',
    left: '0.2%',
    width: '10.6%',
    height: '31.8%',
    tone: 'critical',
  },
  {
    label: 'Unknown 67',
    state: 'No safety vest',
    confidence: '92%',
    top: '41.8%',
    left: '28.4%',
    width: '18.2%',
    height: '43.8%',
    tone: 'warning',
  },
];

const STATS = [
  { value: '02', label: 'Persons Detected' },
  { value: '03', label: 'PPE Violations' },
  { value: '01', label: 'Known Identity' },
];

const INCIDENTS = [
  { tone: 'red', label: 'Satwik', value: 'No hardhat / No goggles' },
  { tone: 'amber', label: 'Unknown 67', value: 'No vest' },
  { tone: 'cyan', label: 'Evidence synced', value: 'Cubicles · 12:03:25' },
];

const DetectionDemo: Component = () => {
  let sectionRef!: HTMLElement;

  onMount(() => {
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;

    let cleanupReveal = () => {};
    let cleanupBoxes = () => {};
    const ctx = gsap.context(() => {
      cleanupReveal = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-detection-reveal',
        start: 'top 84%',
        y: 34,
        stagger: 0.1,
        duration: 0.82,
      });

      cleanupBoxes = revealOnEnter({
        scope: sectionRef,
        selector: '.v3-detection-box',
        trigger: sectionRef.querySelector('.v3-detection-viewer'),
        start: 'top 82%',
        y: 0,
        stagger: 0.12,
        duration: 0.62,
        ease: 'power2.out',
      });
    }, sectionRef);

    onCleanup(() => {
      cleanupReveal();
      cleanupBoxes();
      ctx.revert();
    });
  });

  return (
    <section ref={sectionRef} class="v3-section v3-detection-section">
      <div class="v3-section-head v3-detection-reveal">
        <span class="v3-eyebrow">Detection</span>
        <h2 class="v3-section-title">Real-time threat and compliance detection.</h2>
        <p class="v3-section-lead">
          A single frame becomes a review-ready evidence surface with identity resolution, PPE misses, and response context attached.
        </p>
      </div>

      <div class="v3-detection-shell">
        <div class="v3-detection-console v3-detection-reveal">
          <div class="v3-detection-console-head">
            <span>Evidence Viewer</span>
            <span>Focus lock: Cubicles</span>
          </div>

          <div class="v3-detection-viewer">
            <img
              src="/marketing/shots/detection-cubicles.webp"
              alt="Office CCTV feed with two detected people and PPE compliance overlays"
              loading="lazy"
              decoding="async"
            />
            <div class="v3-detection-scanline" />
            <div class="v3-detection-focus-lock" />

            <For each={BOUNDING_BOXES}>
              {(box) => (
                <div
                  class={`v3-detection-box is-${box.tone}`}
                  style={{
                    top: box.top,
                    left: box.left,
                    width: box.width,
                    height: box.height,
                  }}
                >
                  <div class="v3-detection-box-meta">
                    <span>{box.label}</span>
                    <span>{box.confidence}</span>
                  </div>
                  <div class="v3-detection-box-label">{box.state}</div>
                </div>
              )}
            </For>
          </div>

          <div class="v3-detection-rail">
            <For each={INCIDENTS}>
              {(incident) => (
                <div class={`v3-detection-chip is-${incident.tone}`}>
                  <span>{incident.label}</span>
                  <strong>{incident.value}</strong>
                </div>
              )}
            </For>
          </div>
        </div>

        <div class="v3-detection-sidebar v3-detection-reveal">
          <div class="v3-detection-stats">
            <For each={STATS}>
              {(stat) => (
                <div class="v3-detection-stat">
                  <strong>{stat.value}</strong>
                  <span>{stat.label}</span>
                </div>
              )}
            </For>
          </div>

          <div class="v3-detection-brief">
            <span class="v3-detection-brief-label">Incident Intelligence</span>
            <h3>Two people detected. Identity and PPE gaps stay attached to the same frame.</h3>
            <p>
              Satwik is resolved as a known individual with no hardhat and no goggles. Unknown 67 is surfaced as unregistered and missing a safety vest in the same evidence chain.
            </p>
            <ul class="v3-detection-findings">
              <li>2 persons detected in Cubicles</li>
              <li>Satwik flagged for hardhat and goggles non-compliance</li>
              <li>Unknown 67 escalated for missing safety vest</li>
            </ul>
          </div>
        </div>
      </div>
    </section>
  );
};

export default DetectionDemo;
