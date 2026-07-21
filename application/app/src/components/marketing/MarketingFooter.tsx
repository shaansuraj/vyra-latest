import { type Component } from 'solid-js';

type MarketingRoute = '/' | '/about' | '/features' | '/contact';

export interface MarketingFooterProps {
  onContactSales: () => void;
  onBookDemo: () => void;
  dashboardBaseURL: string;
  onNavigate: (to: MarketingRoute) => void;
}

const MarketingFooter: Component<MarketingFooterProps> = (props) => {
  const navigate = (event: MouseEvent, to: MarketingRoute) => {
    event.preventDefault();
    props.onNavigate(to);
  };

  return (
    <footer class="v3-footer">
      <div class="v3-footer-top">
        <div>
          <span class="v3-eyebrow v3-footer-eyebrow">Final Step</span>
          <h2 class="v3-footer-cta-title">Ready to see VYRA in action?</h2>
        </div>
        <button class="v3-btn v3-btn-primary" onClick={props.onBookDemo}>
          Book Demo
        </button>
      </div>

      <div class="v3-footer-grid">
        <div class="v3-footer-brand-block">
          <div class="v3-footer-brand">VYRA</div>
          <p class="v3-footer-tagline">
            AI CCTV vision for teams that need operational clarity, evidence confidence, and secure deployment from day one.
          </p>
          <span class="v3-footer-meta">Built by Hexmon Technology</span>
        </div>

        <div>
          <div class="v3-footer-col-title">Product</div>
          <ul class="v3-footer-links">
            <li><a href="/features" onClick={(event) => navigate(event, '/features')}>Features</a></li>
            <li><a href="/features" onClick={(event) => navigate(event, '/features')}>Command Center</a></li>
            <li><a href={`${props.dashboardBaseURL}/login`}>Dashboard</a></li>
          </ul>
        </div>

        <div>
          <div class="v3-footer-col-title">Company</div>
          <ul class="v3-footer-links">
            <li><a href="/about" onClick={(event) => navigate(event, '/about')}>About</a></li>
            <li><a href="/contact" onClick={(event) => navigate(event, '/contact')}>Contact</a></li>
            <li><a href="https://hexmontechnology.com" target="_blank" rel="noopener noreferrer">Hexmon Technology</a></li>
          </ul>
        </div>

        <div>
          <div class="v3-footer-col-title">Legal</div>
          <ul class="v3-footer-links">
            <li><button class="v3-footer-link-button" onClick={props.onContactSales}>Security Review</button></li>
            <li><button class="v3-footer-link-button" onClick={props.onBookDemo}>DPA on Request</button></li>
            <li><span>Audit trail and RBAC included</span></li>
          </ul>
        </div>
      </div>

      <div class="v3-footer-bottom">
        <span class="v3-footer-copy">&copy; {new Date().getFullYear()} Hexmon Technology. All rights reserved.</span>
        <span class="v3-footer-built">Designed for production-grade camera operations.</span>
      </div>
    </footer>
  );
};

export default MarketingFooter;
