import { type Component } from 'solid-js';

export interface CTASectionProps {
  onContactSales: () => void;
  onBookDemo: () => void;
}

const CTASection: Component<CTASectionProps> = (props) => {
  return (
    <section class="v3-section v3-cta-section">
      <div class="v3-cta-shell">
        <div class="v3-cta-card">
          <span class="v3-eyebrow">Conversion</span>
          <h2 class="v3-cta-title">Start seeing what your cameras are missing.</h2>
          <p class="v3-cta-lead">
            Share your camera footprint, compliance goals, or response workflow. We’ll tailor a VYRA walkthrough to the way your site actually runs.
          </p>

          <div class="v3-cta-actions">
            <button class="v3-btn v3-btn-primary v3-btn-lg" onClick={props.onBookDemo}>
              Book Demo
            </button>
            <button class="v3-btn v3-btn-secondary" onClick={props.onContactSales}>
              Contact Sales
            </button>
          </div>

          <p class="v3-cta-email">
            or reach the team directly at <a href="mailto:info@hexmontechnology.com">info@hexmontechnology.com</a>
          </p>
        </div>
      </div>
    </section>
  );
};

export default CTASection;
