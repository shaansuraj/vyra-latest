import { type Component, For, createEffect, createSignal, onCleanup, onMount } from 'solid-js';

type MarketingRoute = '/' | '/about' | '/features' | '/contact';

const NAV_ITEMS: Array<{ path: MarketingRoute; label: string }> = [
  { path: '/', label: 'Home' },
  { path: '/features', label: 'Features' },
  { path: '/about', label: 'About' },
  { path: '/contact', label: 'Contact' },
];

export interface MarketingNavProps {
  route: () => MarketingRoute;
  onNavigate: (to: MarketingRoute) => void;
  onContactSales: () => void;
  onBookDemo: () => void;
  onDashboard: () => void;
}

const MarketingNav: Component<MarketingNavProps> = (props) => {
  const [condensed, setCondensed] = createSignal(false);
  const [menuOpen, setMenuOpen] = createSignal(false);

  onMount(() => {
    const onScroll = () => setCondensed(window.scrollY > 40);
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setMenuOpen(false);
    };
    const onResize = () => {
      if (window.innerWidth > 960) setMenuOpen(false);
    };

    window.addEventListener('scroll', onScroll, { passive: true });
    window.addEventListener('keydown', onKeyDown);
    window.addEventListener('resize', onResize, { passive: true });
    onScroll();

    onCleanup(() => {
      window.removeEventListener('scroll', onScroll);
      window.removeEventListener('keydown', onKeyDown);
      window.removeEventListener('resize', onResize);
      document.body.style.removeProperty('overflow');
    });
  });

  createEffect(() => {
    document.body.style.overflow = menuOpen() ? 'hidden' : '';
  });

  const navigate = (to: MarketingRoute) => {
    setMenuOpen(false);
    props.onNavigate(to);
  };

  return (
    <>
      <header class={`v3-header ${condensed() ? 'is-condensed' : ''}`}>
        <div class="v3-header-inner">
          <a
            href="/"
            class="v3-brand"
            onClick={(ev) => {
              ev.preventDefault();
              navigate('/');
            }}
          >
            <span class="v3-brand-mark">VYRA</span>
            <span class="v3-brand-tag">Operational Vision</span>
          </a>

          <nav class="v3-nav" aria-label="Primary">
            <For each={NAV_ITEMS}>
              {(item) => (
                <a
                  href={item.path}
                  aria-current={props.route() === item.path ? 'page' : undefined}
                  class={`v3-nav-link ${props.route() === item.path ? 'is-active' : ''}`}
                  onClick={(ev) => {
                    ev.preventDefault();
                    navigate(item.path);
                  }}
                >
                  {item.label}
                </a>
              )}
            </For>
          </nav>

          <div class="v3-header-actions">
            <button class="v3-btn v3-btn-text" onClick={props.onDashboard}>
              Login
            </button>
            <button class="v3-btn v3-btn-primary" onClick={props.onBookDemo}>
              Book Demo
            </button>
          </div>

          <button
            class={`v3-hamburger ${menuOpen() ? 'is-open' : ''}`}
            onClick={() => setMenuOpen(!menuOpen())}
            aria-label="Toggle menu"
            aria-expanded={menuOpen()}
            aria-controls="v3-mobile-menu"
          >
            <span />
            <span />
            <span />
          </button>
        </div>
      </header>

      <div id="v3-mobile-menu" class={`v3-mobile-menu ${menuOpen() ? 'is-open' : ''}`} aria-hidden={!menuOpen()}>
        <div class="v3-mobile-menu-panel">
          <div class="v3-mobile-menu-meta">
            <span class="v3-mobile-menu-label">Command Overlay</span>
            <span class="v3-mobile-menu-caption">Navigate the VYRA marketing surface.</span>
          </div>

          <div class="v3-mobile-menu-links">
            <For each={NAV_ITEMS}>
              {(item, index) => (
                <a
                  href={item.path}
                  class={`v3-mobile-link ${props.route() === item.path ? 'is-active' : ''}`}
                  onClick={(ev) => {
                    ev.preventDefault();
                    navigate(item.path);
                  }}
                >
                  <span class="v3-mobile-link-index">0{index() + 1}</span>
                  <span>{item.label}</span>
                </a>
              )}
            </For>
          </div>

          <div class="v3-mobile-menu-actions">
            <button
              class="v3-btn v3-btn-text"
              onClick={() => {
                setMenuOpen(false);
                props.onDashboard();
              }}
            >
              Login
            </button>
            <button
              class="v3-btn v3-btn-primary"
              onClick={() => {
                setMenuOpen(false);
                props.onBookDemo();
              }}
            >
              Book Demo
            </button>
          </div>

          <div class="v3-mobile-menu-foot">
            <span class="v3-mobile-menu-label">Security-first deployment</span>
            <span class="v3-mobile-menu-caption">On-premise, encrypted, operator-ready.</span>
          </div>
        </div>
      </div>
    </>
  );
};

export default MarketingNav;
