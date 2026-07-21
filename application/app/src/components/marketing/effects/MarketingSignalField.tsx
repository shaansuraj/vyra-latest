import { For, type Component, onCleanup, onMount } from 'solid-js';

type SignalSeverity = 'amber' | 'cyan' | 'mint' | 'violet' | 'red';
type SignalSceneKind = 'alert' | 'camera' | 'topology' | 'timeline' | 'roster' | 'radar';

interface SignalScene {
  kind: SignalSceneKind;
  label: string;
  meta: string;
  severity: SignalSeverity;
  x: number;
  y: number;
  scale: number;
  depth: number;
  width?: number;
  mobileX?: number;
  mobileY?: number;
  mobileScale?: number;
  mobileHidden?: boolean;
  lines?: string[];
  values?: string[];
}

const SIGNAL_SCENES: SignalScene[] = [
  {
    kind: 'alert',
    label: 'PPE drift',
    meta: 'Floor A7',
    severity: 'amber',
    x: 18,
    y: 69,
    scale: 0.98,
    depth: 1.28,
    width: 190,
    mobileX: 24,
    mobileY: 70,
    lines: ['No hardhat', 'No goggles'],
  },
  {
    kind: 'radar',
    label: 'Perimeter sweep',
    meta: 'Yard 08',
    severity: 'red',
    x: 39,
    y: 64,
    scale: 0.88,
    depth: 1.08,
    width: 176,
    mobileHidden: true,
    values: ['3 active tracks'],
  },
  {
    kind: 'topology',
    label: 'Relay mesh',
    meta: 'Node fabric',
    severity: 'violet',
    x: 54,
    y: 74,
    scale: 0.98,
    depth: 1.12,
    width: 214,
    mobileX: 54,
    mobileY: 82,
    mobileScale: 0.92,
    values: ['11 nodes', '2 routes'],
  },
  {
    kind: 'camera',
    label: 'Camera cluster',
    meta: 'Bay C1',
    severity: 'cyan',
    x: 80,
    y: 63,
    scale: 1.03,
    depth: 1.34,
    width: 228,
    mobileX: 74,
    mobileY: 62,
    mobileScale: 0.96,
    values: ['4 live views', '1 blind spot'],
  },
  {
    kind: 'roster',
    label: 'Identity layer',
    meta: 'Known / unknown',
    severity: 'mint',
    x: 29,
    y: 85,
    scale: 0.9,
    depth: 0.82,
    width: 182,
    mobileHidden: true,
    values: ['Known 12', 'Unknown 03'],
  },
  {
    kind: 'timeline',
    label: 'Evidence chain',
    meta: 'Incident 42',
    severity: 'cyan',
    x: 74,
    y: 84,
    scale: 0.97,
    depth: 0.96,
    width: 224,
    mobileX: 66,
    mobileY: 90,
    mobileScale: 0.88,
    lines: ['12:03 alert', '12:04 identity', '12:05 packet'],
  },
];

const renderSceneBody = (scene: SignalScene) => {
  switch (scene.kind) {
    case 'alert':
      return (
        <div class="v3-signal-scene-alert">
          <For each={scene.lines || []}>
            {(line) => (
              <div class="v3-signal-scene-chip">
                <span />
                <strong>{line}</strong>
              </div>
            )}
          </For>
        </div>
      );

    case 'camera':
      return (
        <div class="v3-signal-scene-camera">
          <div class="v3-signal-scene-camera-top">
            <span />
            <span />
            <span />
          </div>
          <div class="v3-signal-scene-camera-grid">
            <span class="is-large" />
            <span />
            <span />
            <span />
          </div>
          <div class="v3-signal-scene-foot">
            <For each={scene.values || []}>{(value) => <small>{value}</small>}</For>
          </div>
        </div>
      );

    case 'topology':
      return (
        <div class="v3-signal-scene-topology">
          <span class="v3-signal-scene-link is-one" />
          <span class="v3-signal-scene-link is-two" />
          <span class="v3-signal-scene-link is-three" />
          <span class="v3-signal-scene-node is-center" />
          <span class="v3-signal-scene-node is-top" />
          <span class="v3-signal-scene-node is-left" />
          <span class="v3-signal-scene-node is-right" />
          <div class="v3-signal-scene-foot">
            <For each={scene.values || []}>{(value) => <small>{value}</small>}</For>
          </div>
        </div>
      );

    case 'timeline':
      return (
        <div class="v3-signal-scene-timeline">
          <div class="v3-signal-scene-track">
            <span />
            <span />
            <span />
          </div>
          <div class="v3-signal-scene-timeline-labels">
            <For each={scene.lines || []}>{(line) => <small>{line}</small>}</For>
          </div>
        </div>
      );

    case 'roster':
      return (
        <div class="v3-signal-scene-roster">
          <For each={scene.values || []}>
            {(value, index) => (
              <div class="v3-signal-scene-roster-row">
                <span class={`v3-signal-scene-avatar ${index() === 0 ? 'is-known' : 'is-unknown'}`} />
                <small>{value}</small>
                <i />
              </div>
            )}
          </For>
        </div>
      );

    case 'radar':
      return (
        <div class="v3-signal-scene-radar">
          <span class="v3-signal-radar-ring is-outer" />
          <span class="v3-signal-radar-ring is-mid" />
          <span class="v3-signal-radar-ring is-inner" />
          <span class="v3-signal-radar-sweep" />
          <span class="v3-signal-radar-point is-one" />
          <span class="v3-signal-radar-point is-two" />
          <span class="v3-signal-radar-point is-three" />
          <div class="v3-signal-scene-foot">
            <For each={scene.values || []}>{(value) => <small>{value}</small>}</For>
          </div>
        </div>
      );

    default:
      return null;
  }
};

const MarketingSignalField: Component = () => {
  let rootRef!: HTMLDivElement;
  let canvasRef!: HTMLCanvasElement;

  onMount(() => {
    const canvas = canvasRef;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;

    const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const finePointer = window.matchMedia('(hover: hover) and (pointer: fine)').matches;
    const dpr = Math.min(window.devicePixelRatio || 1, 1.5);

    let width = 1;
    let height = 1;
    let raf = 0;
    let visible = !document.hidden;
    let time = 0;
    let scrollY = window.scrollY || 0;
    let frameCount = 0;

    const pointer = {
      x: window.innerWidth * 0.62,
      y: window.innerHeight * 0.58,
      tx: window.innerWidth * 0.62,
      ty: window.innerHeight * 0.58,
    };

    const setPointerVars = () => {
      const scrollRatio = Math.min(scrollY / Math.max(height * 3.2, 1), 1);
      const shiftX = (pointer.x - width * 0.5) * 0.045;
      const shiftY = (pointer.y - height * 0.58) * 0.034 + scrollRatio * 18;
      const softX = width * 0.57 + (pointer.x - width * 0.5) * 0.5 + width * 0.12;
      const softY = height * 0.58 + (pointer.y - height * 0.56) * 0.4 - height * 0.08 + scrollRatio * 24;
      const tiltX = ((height * 0.54 - pointer.y) / Math.max(height, 1)) * 3.2;
      const tiltY = ((pointer.x - width * 0.5) / Math.max(width, 1)) * 5.4;

      rootRef.style.setProperty('--v3-signal-x', `${pointer.x}px`);
      rootRef.style.setProperty('--v3-signal-y', `${pointer.y}px`);
      rootRef.style.setProperty('--v3-signal-x-soft', `${softX}px`);
      rootRef.style.setProperty('--v3-signal-y-soft', `${softY}px`);
      rootRef.style.setProperty('--v3-signal-shift-x', `${shiftX}px`);
      rootRef.style.setProperty('--v3-signal-shift-y', `${shiftY}px`);
      rootRef.style.setProperty('--v3-signal-plane-tilt-x', `${tiltX}deg`);
      rootRef.style.setProperty('--v3-signal-plane-tilt-y', `${tiltY}deg`);
      rootRef.style.setProperty('--v3-signal-plane-shift-x', `${shiftX * 0.4}px`);
      rootRef.style.setProperty('--v3-signal-plane-shift-y', `${shiftY * 0.35}px`);
      rootRef.style.setProperty('--v3-signal-scroll-y', `${scrollRatio * 36}px`);
      rootRef.style.setProperty('--v3-signal-scroll-progress', `${scrollRatio}`);
    };

    const resize = () => {
      width = Math.max(window.innerWidth, 1);
      height = Math.max(window.innerHeight, 1);
      canvas.width = Math.round(width * dpr);
      canvas.height = Math.round(height * dpr);
      canvas.style.width = `${width}px`;
      canvas.style.height = `${height}px`;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      setPointerVars();
      schedule();
    };

    const drawChamber = (xNorm: number, depth: number, span: number, lift: number, alpha: number) => {
      const centerX = width * xNorm + (pointer.x - width * 0.5) * 0.05;
      const topY = height * depth;
      const floorY = topY + height * lift;
      const topHalf = width * span * 0.22;
      const bottomHalf = width * span * 0.48;

      const fill = ctx.createLinearGradient(centerX, topY, centerX, floorY);
      fill.addColorStop(0, `rgba(101, 222, 255, ${alpha * 0.16})`);
      fill.addColorStop(1, 'rgba(101, 222, 255, 0)');
      ctx.fillStyle = fill;
      ctx.beginPath();
      ctx.moveTo(centerX - topHalf, topY);
      ctx.lineTo(centerX + topHalf, topY);
      ctx.lineTo(centerX + bottomHalf, floorY);
      ctx.lineTo(centerX - bottomHalf, floorY);
      ctx.closePath();
      ctx.fill();

      ctx.strokeStyle = `rgba(101, 222, 255, ${alpha})`;
      ctx.lineWidth = 1;
      ctx.beginPath();
      ctx.moveTo(centerX - topHalf, topY);
      ctx.lineTo(centerX + topHalf, topY);
      ctx.lineTo(centerX + bottomHalf, floorY);
      ctx.lineTo(centerX - bottomHalf, floorY);
      ctx.closePath();
      ctx.stroke();

      ctx.beginPath();
      ctx.moveTo(centerX, topY);
      ctx.lineTo(centerX, floorY);
      ctx.stroke();
    };

    const drawGrid = () => {
      ctx.clearRect(0, 0, width, height);

      const horizonY = height * 0.28;
      const vanishX = width * 0.5 + (pointer.x - width * 0.5) * 0.025;
      const vanishY = horizonY + (pointer.y - height * 0.55) * 0.012;
      const bottomY = height * 1.06;
      const topY = -height * 0.14;

      const glow = ctx.createRadialGradient(pointer.x, pointer.y, 0, pointer.x, pointer.y, Math.max(width, height) * 0.26);
      glow.addColorStop(0, 'rgba(101, 222, 255, 0.1)');
      glow.addColorStop(0.42, 'rgba(77, 130, 255, 0.05)');
      glow.addColorStop(1, 'rgba(77, 130, 255, 0)');
      ctx.fillStyle = glow;
      ctx.fillRect(0, 0, width, height);

      ctx.save();
      ctx.lineWidth = 1;

      for (let i = -15; i <= 15; i += 1) {
        const bottomX = vanishX + i * width * 0.072;
        const topX = vanishX + i * width * 0.012;
        ctx.strokeStyle = i % 4 === 0 ? 'rgba(101, 222, 255, 0.11)' : 'rgba(101, 222, 255, 0.048)';
        ctx.beginPath();
        ctx.moveTo(topX, vanishY);
        ctx.lineTo(bottomX, bottomY);
        ctx.stroke();
      }

      for (let row = 1; row <= 13; row += 1) {
        const t = row / 13;
        const eased = t * t;
        const y = vanishY + (bottomY - vanishY) * eased;
        const spread = width * (0.12 + eased * 0.88);
        const alpha = 0.02 + eased * 0.08;

        ctx.strokeStyle = `rgba(101, 222, 255, ${alpha})`;
        ctx.beginPath();
        ctx.moveTo(vanishX - spread, y);
        ctx.lineTo(vanishX + spread, y);
        ctx.stroke();
      }

      for (let i = -13; i <= 13; i += 1) {
        const topX = vanishX + i * width * 0.012;
        const ceilingX = vanishX + i * width * 0.062;
        ctx.strokeStyle = i % 4 === 0 ? 'rgba(101, 222, 255, 0.08)' : 'rgba(101, 222, 255, 0.032)';
        ctx.beginPath();
        ctx.moveTo(topX, vanishY);
        ctx.lineTo(ceilingX, topY);
        ctx.stroke();
      }

      for (let row = 1; row <= 8; row += 1) {
        const t = row / 8;
        const eased = t * t;
        const y = vanishY - (vanishY - topY) * eased;
        const spread = width * (0.1 + eased * 0.72);
        const alpha = 0.016 + eased * 0.042;

        ctx.strokeStyle = `rgba(101, 222, 255, ${alpha})`;
        ctx.beginPath();
        ctx.moveTo(vanishX - spread, y);
        ctx.lineTo(vanishX + spread, y);
        ctx.stroke();
      }

      ctx.strokeStyle = 'rgba(101, 222, 255, 0.06)';
      ctx.beginPath();
      ctx.moveTo(vanishX, vanishY);
      ctx.lineTo(width * 0.04, height * 0.78);
      ctx.lineTo(width * 0.08, height);
      ctx.stroke();

      ctx.beginPath();
      ctx.moveTo(vanishX, vanishY);
      ctx.lineTo(width * 0.96, height * 0.78);
      ctx.lineTo(width * 0.92, height);
      ctx.stroke();

      drawChamber(0.22, 0.44, 0.12, 0.16, 0.08);
      drawChamber(0.51, 0.34, 0.16, 0.21, 0.1);
      drawChamber(0.8, 0.42, 0.12, 0.15, 0.08);

      const beamAngle = Math.sin(time * 0.13) * 0.18;
      ctx.translate(vanishX, vanishY + height * 0.03);
      ctx.rotate(beamAngle);
      const beamGradient = ctx.createLinearGradient(-width * 0.26, 0, width * 0.26, height * 0.52);
      beamGradient.addColorStop(0, 'rgba(101, 222, 255, 0)');
      beamGradient.addColorStop(0.48, 'rgba(101, 222, 255, 0.012)');
      beamGradient.addColorStop(0.5, 'rgba(101, 222, 255, 0.05)');
      beamGradient.addColorStop(0.52, 'rgba(101, 222, 255, 0.012)');
      beamGradient.addColorStop(1, 'rgba(101, 222, 255, 0)');
      ctx.fillStyle = beamGradient;
      ctx.fillRect(-width * 0.24, 0, width * 0.48, height * 0.66);
      ctx.restore();

      for (let row = 0; row < 4; row += 1) {
        const pulse = (time * 48 + row * 120) % (width * 0.8);
        const y = vanishY + (bottomY - vanishY) * ((row + 6) / 13) ** 2;
        const left = vanishX - width * (0.18 + row * 0.08);
        const right = vanishX + width * (0.18 + row * 0.08);
        const x = left + (pulse % Math.max(right - left, 1));
        const dot = ctx.createRadialGradient(x, y, 0, x, y, 18);
        dot.addColorStop(0, 'rgba(101, 222, 255, 0.65)');
        dot.addColorStop(1, 'rgba(101, 222, 255, 0)');
        ctx.fillStyle = dot;
        ctx.beginPath();
        ctx.arc(x, y, 18, 0, Math.PI * 2);
        ctx.fill();
      }
    };

    const schedule = () => {
      if (raf) return;
      raf = window.requestAnimationFrame(render);
    };

    const render = () => {
      raf = 0;
      if (!visible) return;

      if (!finePointer && !reduceMotion) {
        const scrollRatio = Math.min(scrollY / Math.max(height * 3.2, 1), 1);
        pointer.tx = width * (0.5 + Math.sin(time * 0.17 + scrollRatio * 2.4) * 0.24);
        pointer.ty = height * (0.66 + Math.cos(time * 0.11 + scrollRatio * 1.6) * 0.1 + scrollRatio * 0.05);
      }

      if (reduceMotion) {
        pointer.x = pointer.tx;
        pointer.y = pointer.ty;
      } else {
        pointer.x += (pointer.tx - pointer.x) * 0.08;
        pointer.y += (pointer.ty - pointer.y) * 0.08;
        time += 0.016;
      }

      frameCount++;
      if (frameCount % 3 === 0) setPointerVars();
      drawGrid();

      if (!reduceMotion) {
        schedule();
        return;
      }

      if (Math.abs(pointer.tx - pointer.x) > 0.4 || Math.abs(pointer.ty - pointer.y) > 0.4) {
        schedule();
      }
    };

    const handlePointerMove = (event: PointerEvent) => {
      pointer.tx = event.clientX;
      pointer.ty = event.clientY;
      schedule();
    };

    const handleVisibility = () => {
      visible = !document.hidden;
      if (visible) schedule();
    };

    const handleScroll = () => {
      scrollY = window.scrollY || 0;
      schedule();
    };

    if (!finePointer) {
      rootRef.dataset.pointer = 'coarse';
      pointer.x = window.innerWidth * 0.52;
      pointer.y = window.innerHeight * 0.7;
      pointer.tx = pointer.x;
      pointer.ty = pointer.y;
    }

    resize();
    if (finePointer) window.addEventListener('pointermove', handlePointerMove, { passive: true });
    window.addEventListener('resize', resize, { passive: true });
    window.addEventListener('scroll', handleScroll, { passive: true });
    document.addEventListener('visibilitychange', handleVisibility);
    schedule();

    onCleanup(() => {
      window.cancelAnimationFrame(raf);
      if (finePointer) window.removeEventListener('pointermove', handlePointerMove);
      window.removeEventListener('resize', resize);
      window.removeEventListener('scroll', handleScroll);
      document.removeEventListener('visibilitychange', handleVisibility);
    });
  });

  return (
    <div ref={rootRef} class="v3-signal-field" aria-hidden="true">
      <canvas ref={canvasRef!} class="v3-signal-field-canvas" />
      <div class="v3-signal-field-aura" />
      <div class="v3-signal-field-reveal">
        <div class="v3-signal-field-depth">
          <span class="v3-signal-depth-plane is-ceiling" />
          <span class="v3-signal-depth-wall is-left" />
          <span class="v3-signal-depth-wall is-right" />
          <span class="v3-signal-depth-horizon" />
        </div>
        <div class="v3-signal-field-plane">
          <div class="v3-signal-field-volumes">
            <span class="v3-signal-volume is-left" />
            <span class="v3-signal-volume is-center" />
            <span class="v3-signal-volume is-right" />
          </div>

          <div class="v3-signal-field-scenes">
            <For each={SIGNAL_SCENES}>
              {(scene) => (
                <div
                  class={`v3-signal-scene is-${scene.kind} is-${scene.severity} ${scene.mobileHidden ? 'is-mobile-hidden' : ''}`}
                  style={{
                    '--scene-x': `${scene.x}%`,
                    '--scene-y': `${scene.y}%`,
                    '--scene-scale': `${scene.scale}`,
                    '--scene-depth': `${scene.depth}`,
                    '--scene-width': scene.width ? `${scene.width}px` : undefined,
                    '--scene-mobile-x': scene.mobileX ? `${scene.mobileX}%` : undefined,
                    '--scene-mobile-y': scene.mobileY ? `${scene.mobileY}%` : undefined,
                    '--scene-mobile-scale': scene.mobileScale ? `${scene.mobileScale}` : undefined,
                  }}
                >
                  <div class="v3-signal-scene-head">
                    <span>{scene.meta}</span>
                    <strong>{scene.label}</strong>
                  </div>
                  {renderSceneBody(scene)}
                </div>
              )}
            </For>
          </div>
        </div>
      </div>
    </div>
  );
};

export default MarketingSignalField;
