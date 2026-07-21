import { lazy, Suspense, type Component } from 'solid-js';
import RevealOnScroll from '../motion/RevealOnScroll';

const TabPersons = lazy(() => import('./TabPersons'));

const TabIdentity: Component = () => {
  return (
    <div class="h-full min-h-0 overflow-hidden flex flex-col gap-4 p-4 md:p-5">
      <RevealOnScroll class="flex-none">
        <div class="depth-panel rounded-2xl p-4 md:p-5 border border-neu-700/60">
          <div class="heading-display text-[12px] tracking-[0.1em] text-neu-500">Identity</div>
          <h2 class="text-xl md:text-2xl font-semibold text-neu-100 mt-1 leading-tight">Person Registry and Cross-Camera Context</h2>
          <p class="text-sm text-neu-400 mt-2 max-w-3xl leading-relaxed">
            Maintain enrollment quality, inspect sightings, and review cross-camera moments for recognized identities.
          </p>
        </div>
      </RevealOnScroll>

      <div class="flex-1 min-h-0 depth-canvas rounded-2xl overflow-hidden border border-neu-800/80">
        <Suspense fallback={<div class="h-full flex items-center justify-center text-sm text-neu-500">Loading identity registry...</div>}>
          <TabPersons />
        </Suspense>
      </div>
    </div>
  );
};

export default TabIdentity;
