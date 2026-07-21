import { Show, createMemo, createSignal, type Accessor, type Component } from 'solid-js';
import { gsap } from 'gsap';
import { ScrollTrigger } from 'gsap/ScrollTrigger';

gsap.registerPlugin(ScrollTrigger);

// Shell & Layout
import MarketingShell from '../components/marketing/MarketingShell';
import MarketingNav from '../components/marketing/MarketingNav';
import MarketingFooter from '../components/marketing/MarketingFooter';
import ContactFormModal from '../components/marketing/ContactFormModal';

// Home sections (v3)
import HeroSection from '../components/marketing/home/HeroSection';
import CapabilitiesSection from '../components/marketing/home/CapabilitiesSection';
import ProductShowcase from '../components/marketing/home/ProductShowcase';
import DetectionDemo from '../components/marketing/home/DetectionDemo';
import ArchitectureSection from '../components/marketing/home/ArchitectureSection';
import DashboardPreview from '../components/marketing/home/DashboardPreview';
import SecuritySection from '../components/marketing/home/SecuritySection';
import UseCasesSection from '../components/marketing/home/UseCasesSection';
import CTASection from '../components/marketing/home/CTASection';

// About sections
import AboutHero from '../components/marketing/about/AboutHero';
import MissionStatement from '../components/marketing/about/MissionStatement';
import TeamPrinciples from '../components/marketing/about/TeamPrinciples';

// Features sections
import FeaturesHero from '../components/marketing/features/FeaturesHero';
import FeatureStickyCards from '../components/marketing/features/FeatureStickyCards';
import ArchitectureDiagram from '../components/marketing/features/ArchitectureDiagram';

// Effects
import TextReveal from '../components/marketing/effects/TextReveal';
import GlassCard from '../components/marketing/effects/GlassCard';
import SectionShader from '../components/marketing/effects/SectionShader';
import MagneticButton from '../components/motion/MagneticButton';

type MarketingRoute = '/' | '/about' | '/features' | '/contact';

const WHATSAPP_NUMBER = '917855972168';

const normalizePath = (path: string): MarketingRoute => {
  const cleaned = (path || '/').split('?')[0].replace(/\/+$/, '') || '/';
  if (cleaned === '/about') return '/about';
  if (cleaned === '/features') return '/features';
  if (cleaned === '/contact') return '/contact';
  return '/';
};

const getWhatsAppURL = (
  kind: 'sales' | 'demo',
  payload: { name: string; company: string; email: string; notes: string; route: MarketingRoute }
) => {
  const label = kind === 'sales' ? 'Contact Sales' : 'Book Demo';
  const message =
    `Hi Hexmon Technology, I want to ${label} for VYRA.\n` +
    `Name: ${payload.name || 'Not provided'}\n` +
    `Company: ${payload.company || 'Not provided'}\n` +
    `Email: ${payload.email || 'Not provided'}\n` +
    `Interest: ${payload.notes || 'General inquiry'}\n` +
    `Page: ${payload.route}`;
  return `https://wa.me/${WHATSAPP_NUMBER}?text=${encodeURIComponent(message)}`;
};

export interface MarketingSiteProps {
  path: Accessor<string>;
  onNavigate: (to: string) => void;
  dashboardBaseURL: string;
}

const MarketingSite: Component<MarketingSiteProps> = (props) => {
  const route = createMemo<MarketingRoute>(() => normalizePath(props.path()));

  // Form state (persists across pages)
  const [name, setName] = createSignal('');
  const [company, setCompany] = createSignal('');
  const [email, setEmail] = createSignal('');
  const [notes, setNotes] = createSignal('');

  // Modal state
  const [modalOpen, setModalOpen] = createSignal(false);
  const [modalKind, setModalKind] = createSignal<'sales' | 'demo'>('sales');

  // Transition state
  const [transitioning, setTransitioning] = createSignal(false);
  const [navTransitionLocked, setNavTransitionLocked] = createSignal(false);
  let mainRef!: HTMLElement;

  const navigate = (to: MarketingRoute) => {
    if (to === route() || navTransitionLocked()) return;
    if (!mainRef) {
      props.onNavigate(to);
      window.scrollTo({ top: 0, behavior: 'instant' as ScrollBehavior });
      return;
    }

    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (reduceMotion) {
      props.onNavigate(to);
      window.scrollTo({ top: 0, behavior: 'instant' as ScrollBehavior });
      return;
    }

    setNavTransitionLocked(true);
    setTransitioning(true);

    gsap.killTweensOf(mainRef);
    gsap.timeline({ defaults: { overwrite: 'auto' } })
      .to(mainRef, {
        duration: 0.24,
        autoAlpha: 0,
        y: 12,
        scale: 0.992,
        transformPerspective: 1800,
        transformOrigin: 'center top',
        ease: 'power2.inOut',
      })
      .add(() => {
        props.onNavigate(to);
        window.scrollTo({ top: 0, behavior: 'instant' as ScrollBehavior });
        requestAnimationFrame(() => ScrollTrigger.refresh());
      })
      .fromTo(
        mainRef,
        { autoAlpha: 0, y: 18, scale: 1.008 },
        {
          duration: 0.52,
          autoAlpha: 1,
          y: 0,
          scale: 1,
          ease: 'power3.out',
          clearProps: 'transform,opacity,visibility',
          onComplete: () => {
            setTransitioning(false);
            setNavTransitionLocked(false);
            ScrollTrigger.refresh();
          },
        }
      );
  };

  const gotoDashboardLogin = () => {
    window.location.href = `${props.dashboardBaseURL}/login`;
  };

  const openModal = (kind: 'sales' | 'demo') => {
    setModalKind(kind);
    setModalOpen(true);
  };

  const handleFormSubmit = (kind: 'sales' | 'demo') => {
    const url = getWhatsAppURL(kind, {
      name: name().trim(),
      company: company().trim(),
      email: email().trim(),
      notes: notes().trim(),
      route: route(),
    });
    window.open(url, '_blank', 'noopener,noreferrer');
  };

  return (
    <MarketingShell>
      <MarketingNav
        route={route}
        onNavigate={navigate}
        onContactSales={() => openModal('sales')}
        onBookDemo={() => openModal('demo')}
        onDashboard={gotoDashboardLogin}
      />

      <main ref={mainRef} class={`v3-main ${transitioning() ? 'is-transitioning' : ''}`}>
        {/* ─── HOME ─── */}
        <Show when={route() === '/'}>
          <HeroSection
            onBookDemo={() => openModal('demo')}
            onDashboard={gotoDashboardLogin}
          />
          <SectionShader class="v3-divider-hero" colorA={[0.08, 0.34, 0.56]} colorB={[0.42, 0.74, 0.96]} />
          <CapabilitiesSection />
          <ProductShowcase />
          <DetectionDemo />
          <SectionShader class="v3-divider-mid" colorA={[0.08, 0.26, 0.42]} colorB={[0.34, 0.78, 0.72]} />
          <ArchitectureSection />
          <DashboardPreview />
          <SecuritySection />
          <UseCasesSection />
          <CTASection
            onContactSales={() => openModal('sales')}
            onBookDemo={() => openModal('demo')}
          />
        </Show>

        {/* ─── ABOUT ─── */}
        <Show when={route() === '/about'}>
          <AboutHero />
          <MissionStatement />
          <TeamPrinciples
            onContactSales={() => openModal('sales')}
            onBookDemo={() => openModal('demo')}
          />
        </Show>

        {/* ─── FEATURES ─── */}
        <Show when={route() === '/features'}>
          <FeaturesHero />
          <FeatureStickyCards
            onContactSales={() => openModal('sales')}
            onBookDemo={() => openModal('demo')}
          />
          <ArchitectureDiagram />
          <CTASection
            onContactSales={() => openModal('sales')}
            onBookDemo={() => openModal('demo')}
          />
        </Show>

        {/* ─── CONTACT ─── */}
        <Show when={route() === '/contact'}>
          <section class="v3-section v3-contact-page">
            <div class="v3-contact-copy">
              <span class="v3-eyebrow">Contact</span>
              <TextReveal
                text="Talk to the team behind VYRA."
                tag="h2"
                mode="word"
                class="v3-contact-title"
              />
              <p class="v3-contact-desc">
                Share your environment, camera footprint, compliance concerns, or rollout timeline. We’ll respond with a focused next step, not a generic sales sequence.
              </p>
            </div>

            <div class="v3-contact-grid">
              <div class="v3-contact-notes">
                <div class="v3-contact-note">
                  <span>Response window</span>
                  <strong>Usually within one business day</strong>
                  <p>Fast qualification for demos, deployment discussions, and security review requests.</p>
                </div>
                <div class="v3-contact-note">
                  <span>Best for</span>
                  <strong>Manufacturing, warehousing, construction, critical infrastructure</strong>
                  <p>Tell us how many cameras you run and what kind of incident visibility you need.</p>
                </div>
              </div>

              <GlassCard holo depth={2} class="v3-contact-form-shell">
                <div class="v3-contact-form-head">
                  <span>Request a walkthrough</span>
                  <strong>We’ll tailor the conversation to your operational environment.</strong>
                </div>

                <div class="v3-modal-form">
                  <div class="v3-modal-field-row">
                    <label class="v3-modal-field">
                      <span>Name</span>
                      <input
                        type="text"
                        value={name()}
                        onInput={(e) => setName(e.currentTarget.value)}
                        placeholder="Your full name"
                      />
                    </label>
                    <label class="v3-modal-field">
                      <span>Company</span>
                      <input
                        type="text"
                        value={company()}
                        onInput={(e) => setCompany(e.currentTarget.value)}
                        placeholder="Organization name"
                      />
                    </label>
                  </div>
                  <label class="v3-modal-field">
                    <span>Email</span>
                    <input
                      type="email"
                      value={email()}
                      onInput={(e) => setEmail(e.currentTarget.value)}
                      placeholder="name@company.com"
                    />
                  </label>
                  <label class="v3-modal-field">
                    <span>What are you looking for?</span>
                    <textarea
                      value={notes()}
                      onInput={(e) => setNotes(e.currentTarget.value)}
                      placeholder="Number of cameras, use case, timeline..."
                      rows={4}
                    />
                  </label>
                </div>

                <div class="v3-contact-actions">
                  <MagneticButton class="v3-btn v3-btn-primary v3-btn-lg" onClick={() => handleFormSubmit('sales')}>
                    Contact Sales
                  </MagneticButton>
                  <MagneticButton class="v3-btn v3-btn-secondary" onClick={() => handleFormSubmit('demo')}>
                    Book Demo
                  </MagneticButton>
                </div>
              </GlassCard>
            </div>
          </section>
        </Show>
      </main>

      <MarketingFooter
        onContactSales={() => openModal('sales')}
        onBookDemo={() => openModal('demo')}
        dashboardBaseURL={props.dashboardBaseURL}
        onNavigate={navigate}
      />

      <ContactFormModal
        kind={modalKind}
        open={modalOpen}
        setOpen={setModalOpen}
        name={name}
        setName={setName}
        company={company}
        setCompany={setCompany}
        email={email}
        setEmail={setEmail}
        notes={notes}
        setNotes={setNotes}
        onSubmit={handleFormSubmit}
      />
    </MarketingShell>
  );
};

export default MarketingSite;
