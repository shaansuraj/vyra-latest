import { onMount, onCleanup, createSignal, For, type Component, type JSX } from 'solid-js';

/* ── Logo SVG ────────────────────────────────────────────── */
export const LogoMark = (props: { size?: number }) => (
  <svg width={props.size ?? 36} height={props.size ?? 36} viewBox="0 0 36 36" fill="none">
    <rect width="36" height="36" rx="10" fill="#0d1831" stroke="#00D4FF" stroke-width="1.2" />
    <circle cx="18" cy="18" r="4.8" fill="#c6ecff" />
    <circle cx="18" cy="18" r="8.6" stroke="#00D4FF" stroke-width="1.3" stroke-dasharray="2.2 1.5" opacity="0.8" />
    <circle cx="18" cy="18" r="12.4" stroke="#7C5DFA" stroke-width="1" opacity="0.55" />
  </svg>
);

/* ── Grid tile data — fills the entire panel ─────────────── */
const GRID_TILES = [
  // Row 1
  { label: 'PPE Violation',       icon: 'shield',  severity: 'high' },
  { label: 'Unauthorized Entry',  icon: 'lock',    severity: 'high' },
  { label: 'Missing Hardhat',     icon: 'hardhat', severity: 'high' },
  { label: 'Perimeter Breach',    icon: 'fence',   severity: 'high' },
  // Row 2
  { label: 'Misconduct',          icon: 'alert',   severity: 'medium' },
  { label: 'Safety Hazard',       icon: 'flame',   severity: 'medium' },
  { label: 'No Safety Vest',      icon: 'vest',    severity: 'medium' },
  { label: 'Restricted Zone',     icon: 'zone',    severity: 'medium' },
  // Row 3
  { label: 'Fall Detected',       icon: 'fall',    severity: 'high' },
  { label: 'Fire Hazard',         icon: 'flame',   severity: 'high' },
  { label: 'No Goggles',          icon: 'goggles', severity: 'medium' },
  { label: 'Tailgating',          icon: 'alert',   severity: 'medium' },
  // Row 4
  { label: 'Slip Hazard',         icon: 'fall',    severity: 'low' },
  { label: 'Unattended Bag',      icon: 'alert',   severity: 'medium' },
  { label: 'No Gloves',           icon: 'vest',    severity: 'low' },
  { label: 'Crowd Density',       icon: 'zone',    severity: 'low' },
  // Row 5
  { label: 'Vehicle Intrusion',   icon: 'fence',   severity: 'high' },
  { label: 'Loitering',           icon: 'alert',   severity: 'low' },
  { label: 'Smoke Detected',      icon: 'flame',   severity: 'high' },
  { label: 'No Mask',             icon: 'shield',  severity: 'medium' },
];

/* ── Inline SVG icons ────────────────────────────────────── */
const TileIcon = (props: { type: string }) => {
  switch (props.type) {
    case 'shield':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/><line x1="12" y1="8" x2="12" y2="12"/><line x1="12" y1="16" x2="12.01" y2="16"/></svg>;
    case 'lock':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="11" width="18" height="11" rx="2"/><path d="M7 11V7a5 5 0 0 1 10 0v4"/></svg>;
    case 'alert':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 21h16a2 2 0 0 0 1.73-3Z"/><line x1="12" y1="9" x2="12" y2="13"/><line x1="12" y1="17" x2="12.01" y2="17"/></svg>;
    case 'flame':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8.5 14.5A2.5 2.5 0 0 0 11 12c0-1.38-.5-2-1-3-1.072-2.143-.224-4.054 2-6 .5 2.5 2 4.9 4 6.5 2 1.6 3 3.5 3 5.5a7 7 0 1 1-14 0c0-1.153.433-2.294 1-3a2.5 2.5 0 0 0 2.5 2.5z"/></svg>;
    case 'hardhat':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M2 18a1 1 0 0 0 1 1h18a1 1 0 0 0 1-1v-2a1 1 0 0 0-1-1H3a1 1 0 0 0-1 1v2z"/><path d="M10 15V7a2 2 0 0 1 4 0v8"/><path d="M5 15v-3a7 7 0 0 1 14 0v3"/></svg>;
    case 'vest':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M14 6l-1-4H11L10 6"/><path d="M5 6h14v14a2 2 0 0 1-2 2H7a2 2 0 0 1-2-2V6z"/><path d="M5 12h14"/><path d="M5 16h14"/></svg>;
    case 'fence':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M4 4v16"/><path d="M20 4v16"/><path d="M12 4v16"/><path d="M4 8h16"/><path d="M4 16h16"/></svg>;
    case 'fall':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="17" cy="4" r="2"/><path d="m14 12-3-3-4 4-3-3"/><path d="m18 8-3 3-2-2"/><path d="M6 20h12"/></svg>;
    case 'zone':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="4.93" y1="4.93" x2="19.07" y2="19.07"/></svg>;
    case 'goggles':
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="7" cy="12" r="3"/><circle cx="17" cy="12" r="3"/><path d="M10 12h4"/><path d="M4 12H2"/><path d="M22 12h-2"/></svg>;
    default:
      return <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/></svg>;
  }
};

const AuthFrame: Component<{
  panelTitle: string;
  panelSubtitle: string;
  children: JSX.Element;
  footer?: JSX.Element;
}> = (props) => {
  let leftRef: HTMLDivElement | undefined;
  let spotlightRef: HTMLDivElement | undefined;
  let apertureRef: HTMLDivElement | undefined;
  const [mouseIn, setMouseIn] = createSignal(false);

  /* ── Mouse spotlight tracking ──────────────────────────── */
  onMount(() => {
    if (!leftRef || !window.matchMedia('(pointer: fine)').matches) return;
    const revealRadius = 220;
    const hiddenAperture =
      'radial-gradient(circle 0px at 50% 50%, transparent 0%, rgba(7,8,13,0.98) 0%, #07080D 100%)';

    const hideTiles = () => {
      if (!apertureRef) return;
      apertureRef.style.background = hiddenAperture;
    };

    const showTiles = (mx: number, my: number) => {
      if (!apertureRef) return;
      apertureRef.style.background =
        `radial-gradient(circle ${revealRadius}px at ${mx}px ${my}px, ` +
        'transparent 0%, transparent 56%, rgba(7,8,13,0.28) 72%, rgba(7,8,13,0.9) 86%, #07080D 100%)';
    };

    hideTiles();

    const handleMove = (e: MouseEvent) => {
      const rect = leftRef!.getBoundingClientRect();
      const mx = e.clientX - rect.left;
      const my = e.clientY - rect.top;

      if (spotlightRef) {
        spotlightRef.style.left = `${mx}px`;
        spotlightRef.style.top = `${my}px`;
      }

      showTiles(mx, my);
    };

    const handleEnter = () => setMouseIn(true);
    const handleLeave = () => {
      setMouseIn(false);
      hideTiles();
    };

    leftRef.addEventListener('mousemove', handleMove);
    leftRef.addEventListener('mouseenter', handleEnter);
    leftRef.addEventListener('mouseleave', handleLeave);
    onCleanup(() => {
      leftRef?.removeEventListener('mousemove', handleMove);
      leftRef?.removeEventListener('mouseenter', handleEnter);
      leftRef?.removeEventListener('mouseleave', handleLeave);
    });
  });

  const taglineWords = ['See', 'Everything.', 'Miss', 'Nothing.'];

  return (
    <div class="auth-page">
      {/* ── Left Panel ──────────────────────────────────────── */}
      <div class="auth-left" ref={leftRef}>
        {/* Background layers */}
        <div class="auth-grid-bg" />
        <div class="auth-scan-lines" />
        <div class="auth-radar" />

        {/* Mouse spotlight glow */}
        <div
          ref={spotlightRef}
          class="auth-spotlight"
          classList={{ 'is-active': mouseIn() }}
        />

        {/* ── Grid of hidden tiles — revealed by spotlight ─── */}
        <div class="auth-tile-grid">
          <For each={GRID_TILES}>
            {(tile) => (
              <div class={`auth-tile auth-tile--${tile.severity}`}>
                <div class="auth-tile-icon">
                  <TileIcon type={tile.icon} />
                </div>
                <div class="auth-tile-label">{tile.label}</div>
                <div class="auth-tile-severity">{tile.severity}</div>
              </div>
            )}
          </For>
        </div>
        <div ref={apertureRef} class="auth-tile-aperture" />

        {/* Logo — top left */}
        <div class="auth-logo-wrap">
          <div class="auth-logo-glow">
            <LogoMark size={40} />
          </div>
          <div>
            <div class="auth-logo-text">VYRA</div>
            <div class="auth-logo-sub">Visual Intelligence</div>
          </div>
        </div>

        {/* Tagline — large, centered */}
        <div class="auth-tagline-wrap">
          <h1 class="auth-tagline">
            <For each={taglineWords}>
              {(word, i) => (
                <>
                  <span
                    class="auth-tagline-word"
                    style={{ 'animation-delay': `${0.6 + i() * 0.15}s` }}
                  >
                    {word}
                  </span>
                  {i() === 1 ? <br /> : ' '}
                </>
              )}
            </For>
          </h1>
        </div>

        {/* Spacer for flexbox centering */}
        <div style={{ height: '40px' }} />
      </div>

      {/* ── Right Panel ─────────────────────────────────────── */}
      <div class="auth-right">
        <div class="auth-right-bg" aria-hidden="true">
          <div class="auth-right-grid" />
          <div class="auth-right-radar" />
          <div class="auth-right-scan" />
          <div class="auth-right-vignette" />
        </div>

        <div class="auth-card">
          <div style={{ display: 'none' }} class="auth-mobile-logo">
            <div class="auth-logo-wrap" style={{ 'margin-bottom': '24px' }}>
              <LogoMark size={34} />
              <div>
                <div class="auth-logo-text" style={{ 'font-size': '18px' }}>VYRA</div>
                <div class="auth-logo-sub">Visual Intelligence</div>
              </div>
            </div>
          </div>

          <div style={{ 'margin-bottom': '32px' }}>
            <h2 class="auth-card-title">{props.panelTitle}</h2>
            <p class="auth-card-subtitle">{props.panelSubtitle}</p>
          </div>

          {props.children}
          {props.footer}
        </div>
      </div>
    </div>
  );
};

export default AuthFrame;
