import { createSignal, lazy, Suspense, type Component } from 'solid-js';
import { FiSave, FiSettings } from 'solid-icons/fi';
import MagneticButton from '../motion/MagneticButton';
import RevealOnScroll from '../motion/RevealOnScroll';

const TabSettings = lazy(() => import('./TabSettings'));

const TabSystem: Component = () => {
  const [isSaving, setIsSaving] = createSignal(false);

  const handleSave = async () => {
    setIsSaving(true);
    await new Promise((resolve) => setTimeout(resolve, 500));
    setIsSaving(false);
  };

  return (
    <div class="h-full min-h-0 overflow-hidden flex flex-col gap-4 p-4 md:p-5">
      <RevealOnScroll class="flex-none">
        <div class="depth-panel rounded-2xl p-4 md:p-5 border border-neu-700/60">
          <div class="flex items-start justify-between gap-4">
            <div>
              <div class="heading-display text-[12px] tracking-[0.1em] text-neu-500">System</div>
              <h2 class="text-xl md:text-2xl font-semibold text-neu-100 mt-1 leading-tight">Runtime and Platform Settings</h2>
              <p class="text-sm text-neu-400 mt-2 max-w-3xl leading-relaxed">
                Tune alerting, runtime parameters, and frame-rate controls with responsive, feedback-rich interactions.
              </p>
            </div>
            <MagneticButton
              class="saas-cta lift-on-hover interaction-layer disabled:opacity-60 disabled:cursor-not-allowed"
              onClick={handleSave}
              disabled={isSaving()}
            >
              <FiSave size={14} />
              {isSaving() ? 'Saving...' : 'Save Changes'}
            </MagneticButton>
          </div>
          <div class="mt-3 inline-flex items-center gap-2 px-2.5 py-1 rounded-lg border border-neu-700/70 bg-neu-900/55 text-[11px] text-neu-400">
            <FiSettings size={12} />
            System changes are local to this workspace session until persisted by backend APIs.
          </div>
        </div>
      </RevealOnScroll>

      <div class="flex-1 min-h-0 depth-canvas rounded-2xl overflow-hidden border border-neu-800/80">
        <Suspense fallback={<div class="h-full flex items-center justify-center text-sm text-neu-500">Loading settings...</div>}>
          <TabSettings onSave={handleSave} isSaving={isSaving()} />
        </Suspense>
      </div>
    </div>
  );
};

export default TabSystem;
